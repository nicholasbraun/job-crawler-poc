// Package redis implements a crash-safe, resumable URL frontier backed by
// Redis, keyed per crawl run. It preserves the semantics of the in-memory
// reference frontier (per-domain FIFO queues with a cooldown, a maxDepth reject,
// and dedup) while surviving process restarts: queued URLs,
// the visited set, and in-flight leases all live in Redis under a
// frontier:{runID}: namespace.
//
// Next and AddURL are each a single Lua script so concurrent workers can never
// double-pop a URL or race the dedup. In-flight URLs are tracked as leases in a
// processing ZSET (member=url, score=expiry); a worker that crashes without
// calling MarkDone has its lease reclaimed by a later Next once the lease TTL
// elapses, so no URL is lost or duplicated.
package redis

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/metric"
)

// memberSep separates the fields of an encoded queue/inflight member
// (depth, hostname, scope, owner, url). It is the ASCII unit separator (0x1f), a
// control byte that never appears in a normalized URL, hostname, or CompanyKey.
const memberSep = "\x1f"

// maxJitter is the upper bound on the random delay added to every computed Next
// sleep, spreading out workers that would otherwise wake on the same deadline.
const maxJitter = 50 * time.Millisecond

// addScript fuses dedup with enqueue. KEYS: visited, domains.
// ARGV: queuePrefix, domain, member, url, now(ms). Returns DUP (already visited)
// or NEW.
var addScript = redis.NewScript(`
local visited     = KEYS[1]
local domains     = KEYS[2]
local queuePrefix = ARGV[1]
local domain      = ARGV[2]
local member      = ARGV[3]
local url         = ARGV[4]
local now         = tonumber(ARGV[5])

if redis.call('SADD', visited, url) == 0 then
  return 'DUP'
end

-- Make the domain eligible immediately; NX so an active cooldown is not reset.
redis.call('ZADD', domains, 'NX', now, domain)
redis.call('LPUSH', queuePrefix .. domain, member)
return 'NEW'
`)

// nextScript reclaims expired leases, then hands out the earliest eligible URL.
// KEYS: domains, processing, inflight. ARGV: queuePrefix, now(ms), leaseTTL(ms),
// cooldown(ms), pollInterval(ms). Returns a 2-element table:
//
//	{'URL', member}   — a URL to crawl
//	                     (member is depth\x1fhostname\x1fscope\x1fowner\x1furl)
//	{'BADMEMBER', m}  — a queued member predating the #118 provenance format
//	                     (fewer than four separators); put back on its queue and
//	                     surfaced by Next as an error, so stale state is flushed
//	                     rather than silently crawled unfenced.
//	{'WAIT', wakeMs}   — nothing ready; caller sleeps until wakeMs then retries.
//	                     wakeMs is bounded by now+pollInterval and the earliest
//	                     in-flight lease expiry, so reclaim latency tracks
//	                     leaseTTL rather than a domain cooldown.
//	{'DONE'}           — queues empty and no leases in flight
var nextScript = redis.NewScript(`
local domains      = KEYS[1]
local processing   = KEYS[2]
local inflight     = KEYS[3]
local queuePrefix  = ARGV[1]
local now          = tonumber(ARGV[2])
local leaseTTL     = tonumber(ARGV[3])
local cooldown     = tonumber(ARGV[4])
local pollInterval = tonumber(ARGV[5])
local sep          = string.char(31)

-- wakeDeadline bounds a WAIT sleep so a live worker re-evaluates Redis at least
-- once per pollInterval (catching URLs another process added, since there is no
-- cross-process wakeup signal), and no later than the earliest in-flight lease
-- expiry so crash-reclaim tracks leaseTTL rather than a domain cooldown. It is
-- called after the reclaim loop, so every remaining processing score is in the
-- future. candidate is a concrete domain deadline, or nil when there is none.
local function wakeDeadline(candidate)
  local deadline = now + pollInterval
  if candidate ~= nil and candidate < deadline then
    deadline = candidate
  end
  local proc = redis.call('ZRANGE', processing, 0, 0, 'WITHSCORES')
  if #proc > 0 then
    local expiry = tonumber(proc[2])
    if expiry < deadline then
      deadline = expiry
    end
  end
  return deadline
end

-- 1. Reclaim leases whose expiry has passed: re-enqueue the exact member (with
-- its depth) onto its domain queue and make the domain eligible again.
local expired = redis.call('ZRANGEBYSCORE', processing, '-inf', now)
for _, u in ipairs(expired) do
  local member = redis.call('HGET', inflight, u)
  if member then
    local i1 = string.find(member, sep, 1, true)
    local i2 = string.find(member, sep, i1 + 1, true)
    local hostname = string.sub(member, i1 + 1, i2 - 1)
    redis.call('LPUSH', queuePrefix .. hostname, member)
    redis.call('ZADD', domains, 'NX', now, hostname)
  end
  redis.call('ZREM', processing, u)
  redis.call('HDEL', inflight, u)
end

-- 2. Pick the earliest-deadline domain whose queue is non-empty. domains is
-- ascending by score, so the first non-empty one has the nearest deadline.
local ranked = redis.call('ZRANGE', domains, 0, -1, 'WITHSCORES')
local bestDomain, bestScore
for i = 1, #ranked, 2 do
  if redis.call('LLEN', queuePrefix .. ranked[i]) > 0 then
    bestDomain = ranked[i]
    bestScore = tonumber(ranked[i + 1])
    break
  end
end

if bestDomain == nil then
  if redis.call('ZCARD', processing) > 0 then
    return {'WAIT', tostring(wakeDeadline(nil))}
  end
  return {'DONE'}
end

if bestScore > now then
  return {'WAIT', tostring(wakeDeadline(bestScore))}
end

local member = redis.call('RPOP', queuePrefix .. bestDomain)
if not member then
  return {'WAIT', tostring(wakeDeadline(nil))}
end

redis.call('ZADD', domains, now + cooldown, bestDomain)
-- url is the last field: walk past depth, hostname, scope, owner to the 4th
-- separator, then take the rest. Consecutive separators (empty scope/owner)
-- are handled because string.find advances one position per call. Each step is
-- guarded so a member with fewer than four separators -- one written before the
-- #118 provenance format -- yields an actionable BADMEMBER instead of crashing
-- on arithmetic over a nil find; it is pushed back so no URL is lost.
local p = string.find(member, sep, 1, true)                 -- after depth
if p then p = string.find(member, sep, p + 1, true) end     -- after hostname
if p then p = string.find(member, sep, p + 1, true) end     -- after scope
if p then p = string.find(member, sep, p + 1, true) end     -- after owner
if not p then
  redis.call('LPUSH', queuePrefix .. bestDomain, member)
  return {'BADMEMBER', member}
end
local url = string.sub(member, p + 1)
redis.call('ZADD', processing, now + leaseTTL, url)
redis.call('HSET', inflight, url, member)
return {'URL', member}
`)

// doneScript clears a completed URL's lease. KEYS: processing, inflight.
// ARGV: url.
var doneScript = redis.NewScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('HDEL', KEYS[2], ARGV[1])
return 1
`)

// Option configures a Frontier.
type Option func(*Frontier)

// Frontier is a Redis-backed frontier.Frontier for a single crawl run.
type Frontier struct {
	client       *redis.Client
	keyPrefix    string
	queuePrefix  string
	cooldown     time.Duration
	leaseTTL     time.Duration
	pollInterval time.Duration
	maxDepth     int
	mode         frontier.Mode
	retryMin     time.Duration       // first backoff before a transient-error retry (default 100ms)
	retryMax     time.Duration       // backoff cap; retries continue at this interval (default 5s)
	retries      metric.Int64Counter // crawler.frontier.transient_retries, op-attributed
}

var _ frontier.Frontier = &Frontier{}

// WithCooldown sets the per-domain politeness delay between pops (default 1s,
// matching the in-mem frontier).
func WithCooldown(c time.Duration) Option {
	return func(f *Frontier) { f.cooldown = c }
}

// WithLeaseTTL sets how long an in-flight URL may be held before its lease is
// considered lost and the URL is reclaimed by a later Next (default 2m).
func WithLeaseTTL(t time.Duration) Option {
	return func(f *Frontier) { f.leaseTTL = t }
}

// WithPollInterval sets how long Next sleeps when it is waiting on in-flight
// work rather than a concrete domain deadline (default 250ms).
func WithPollInterval(p time.Duration) Option {
	return func(f *Frontier) { f.pollInterval = p }
}

// WithMaxDepth sets the maximum crawl depth; deeper URLs are rejected.
func WithMaxDepth(md int) Option {
	return func(f *Frontier) { f.maxDepth = md }
}

// WithMode selects bounded (default) or perpetual draining behavior for Next.
func WithMode(m frontier.Mode) Option {
	return func(f *Frontier) { f.mode = m }
}

// New builds a Frontier for runID against the given client. Multiple Frontiers
// constructed with the same runID and client share the same Redis state, so a
// restarted process resumes an in-progress run by re-constructing here.
func New(client *redis.Client, runID uuid.UUID, opts ...Option) *Frontier {
	keyPrefix := "frontier:" + runID.String() + ":"
	f := &Frontier{
		client:       client,
		keyPrefix:    keyPrefix,
		queuePrefix:  keyPrefix + "q:",
		cooldown:     time.Second,
		leaseTTL:     2 * time.Minute,
		pollInterval: 250 * time.Millisecond,
		maxDepth:     3,
		mode:         frontier.Bounded,
		retryMin:     100 * time.Millisecond,
		retryMax:     5 * time.Second,
	}

	for _, opt := range opts {
		opt(f)
	}

	// Created after options so the counter is always present even if a test
	// swaps the backoff bounds; nil-safe (a no-op instrument on registration
	// error), so withRetry can Add unconditionally.
	f.retries = newTransientRetryCounter()

	return f
}

func (f *Frontier) key(name string) string { return f.keyPrefix + name }

// DeleteRun removes every Redis key for a run's frontier (all keys under the
// frontier:{runID}: namespace: the per-domain queues, visited set, and lease
// bookkeeping). Used to reclaim the transient state of a run that has ended.
// It is a no-op for a run that has no keys, and uses SCAN (not KEYS) so it does
// not block Redis on a large keyspace.
func DeleteRun(ctx context.Context, client *redis.Client, runID uuid.UUID) error {
	pattern := "frontier:" + runID.String() + ":*"
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return fmt.Errorf("frontier: scanning keys to delete: %w", err)
		}
		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
				return fmt.Errorf("frontier: deleting run keys: %w", err)
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

// Len reports the size of a run's frontier: the number of URLs still waiting to
// be crawled. That is the sum of every per-domain queue (the frontier:{runID}:q:*
// LISTs) plus the in-flight leases (the processing ZSET) — URLs handed out to a
// worker but not yet marked done, which a crash would return to the queues. It
// mirrors DeleteRun: a package function (the API has no per-run Frontier
// instance) that uses SCAN, not KEYS, so it never blocks Redis on a large
// keyspace. A run with no keys reports 0.
func Len(ctx context.Context, client *redis.Client, runID uuid.UUID) (int64, error) {
	keyPrefix := "frontier:" + runID.String() + ":"
	queuePattern := keyPrefix + "q:*"

	var total int64
	var cursor uint64
	for {
		keys, next, err := client.Scan(ctx, cursor, queuePattern, 100).Result()
		if err != nil {
			return 0, fmt.Errorf("frontier: scanning queue keys: %w", err)
		}
		for _, key := range keys {
			n, err := client.LLen(ctx, key).Result()
			if err != nil {
				return 0, fmt.Errorf("frontier: measuring queue %q: %w", key, err)
			}
			total += n
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}

	inflight, err := client.ZCard(ctx, keyPrefix+"processing").Result()
	if err != nil {
		return 0, fmt.Errorf("frontier: counting in-flight leases: %w", err)
	}

	return total + inflight, nil
}

// AddURL dedups and enqueues a URL in a single atomic script. An already-seen
// URL is a silent no-op (returns nil). Returns frontier.ErrMaxDepth if the URL
// is too deep.
func (f *Frontier) AddURL(ctx context.Context, url crawler.URL) error {
	if url.Depth > f.maxDepth {
		return frontier.ErrMaxDepth
	}

	keys := []string{f.key("visited"), f.key("domains")}
	res, err := addScript.Run(ctx, f.client, keys,
		f.queuePrefix, url.Hostname, encodeMember(url), url.RawURL,
		time.Now().UnixMilli(),
	).Result()
	if err != nil {
		return fmt.Errorf("frontier: add url: %w", err)
	}

	switch res {
	case "NEW", "DUP":
		return nil
	default:
		return fmt.Errorf("frontier: unexpected add result %v", res)
	}
}

// Next blocks until a URL is ready and returns it. In bounded mode it returns
// frontier.ErrDone once all queues are empty and no leases are in flight; in
// perpetual mode it keeps polling instead. It reclaims expired leases before
// each pick, so a URL orphaned by a crashed worker is handed out again.
func (f *Frontier) Next(ctx context.Context) (crawler.URL, error) {
	keys := []string{f.key("domains"), f.key("processing"), f.key("inflight")}
	for {
		// time.Now() is read inside the closure so each retry re-computes the
		// clock, keeping lease/reclaim deadlines correct across a stalled retry.
		res, err := f.withRetry(ctx, opNext, func() (any, error) {
			return nextScript.Run(ctx, f.client, keys,
				f.queuePrefix, time.Now().UnixMilli(),
				f.leaseTTL.Milliseconds(), f.cooldown.Milliseconds(),
				f.pollInterval.Milliseconds(),
			).Result()
		})
		if err != nil {
			return crawler.URL{}, fmt.Errorf("frontier: next: %w", err)
		}

		reply, ok := res.([]interface{})
		if !ok || len(reply) == 0 {
			return crawler.URL{}, fmt.Errorf("frontier: unexpected next result %v", res)
		}

		switch reply[0] {
		case "URL":
			member, _ := reply[1].(string)
			return decodeMember(member)
		case "BADMEMBER":
			member, _ := reply[1].(string)
			return crawler.URL{}, fmt.Errorf("frontier: queue member %q predates the #118 provenance format; flush this run's frontier state before resuming on this build", member)
		case "WAIT":
			wakeMs, perr := strconv.ParseInt(fmt.Sprint(reply[1]), 10, 64)
			if perr != nil {
				return crawler.URL{}, fmt.Errorf("frontier: bad wait deadline %v: %w", reply[1], perr)
			}
			if err := f.sleepUntil(ctx, wakeMs); err != nil {
				return crawler.URL{}, err
			}
		case "DONE":
			if f.mode == frontier.Bounded {
				return crawler.URL{}, frontier.ErrDone
			}
			// Perpetual mode never finishes: poll and re-evaluate.
			if err := f.sleep(ctx, f.pollInterval); err != nil {
				return crawler.URL{}, err
			}
		default:
			return crawler.URL{}, fmt.Errorf("frontier: unexpected next tag %v", reply[0])
		}
	}
}

// MarkDone releases the in-flight lease for url. Must be called once per URL
// returned by Next.
func (f *Frontier) MarkDone(ctx context.Context, url string) error {
	keys := []string{f.key("processing"), f.key("inflight")}
	if err := doneScript.Run(ctx, f.client, keys, url).Err(); err != nil {
		return fmt.Errorf("frontier: mark done: %w", err)
	}
	return nil
}

// sleepUntil blocks until the wall-clock reaches wakeMs (plus jitter), or ctx
// is cancelled.
func (f *Frontier) sleepUntil(ctx context.Context, wakeMs int64) error {
	d := time.Until(time.UnixMilli(wakeMs))
	if d < 0 {
		d = 0
	}
	return f.sleep(ctx, d)
}

func (f *Frontier) sleep(ctx context.Context, d time.Duration) error {
	d += rand.N(maxJitter)
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// encodeMember serializes a URL into a queue member: depth, hostname, scope,
// owner, and raw URL joined by memberSep, with the raw URL last (the only field
// that can hold arbitrary bytes). Hostname is embedded at index 1 so the reclaim
// path can route an expired lease back to its domain queue without parsing the
// URL; scope and owner carry keyword-crawl provenance (ADR-0021).
func encodeMember(u crawler.URL) string {
	return strconv.Itoa(u.Depth) + memberSep + u.Hostname + memberSep +
		u.Scope + memberSep + u.Owner + memberSep + u.RawURL
}

func decodeMember(member string) (crawler.URL, error) {
	parts := strings.SplitN(member, memberSep, 5)
	if len(parts) != 5 {
		return crawler.URL{}, fmt.Errorf("frontier: malformed member %q", member)
	}
	depth, err := strconv.Atoi(parts[0])
	if err != nil {
		return crawler.URL{}, fmt.Errorf("frontier: bad depth in member %q: %w", member, err)
	}
	return crawler.URL{
		Depth:    depth,
		Hostname: parts[1],
		Scope:    parts[2],
		Owner:    parts[3],
		RawURL:   parts[4],
	}, nil
}
