// Package redis implements a crash-safe, resumable URL frontier backed by
// Redis, keyed per crawl run. It preserves the semantics of the in-memory
// reference frontier (per-domain FIFO queues with a cooldown — best-effort,
// enforced only while a domain has continuously-queued work (ADR-0026) — a
// maxDepth reject, and dedup) while surviving process restarts: queued URLs,
// the visited set, and in-flight leases all live in Redis under a
// frontier:{runID}: namespace.
//
// Next and AddURL are each a single Lua script so concurrent workers can never
// double-pop a URL or race the dedup. In-flight URLs are tracked as leases in a
// processing ZSET (member=url, score=expiry); a worker that crashes without
// calling MarkDone has its lease reclaimed by a later Next once the lease TTL
// elapses, so no URL is lost or duplicated.
//
// The visited set is a FIFO-capped hashed ZSET (member = xxhash64(RawURL),
// score = insertion ms) rather than an unbounded full-URL SET (ADR-0027), so a
// perpetual Discovery run's per-run footprint stays bounded: past the cap the
// oldest-inserted entries are evicted inline in the add script.
package redis

import (
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// memberSep separates the fields of an encoded queue/inflight member
// (depth, hostname, scope, owner, url). It is the ASCII unit separator (0x1f), a
// control byte that never appears in a normalized URL, hostname, or CompanyKey.
const memberSep = "\x1f"

// maxJitter is the upper bound on the random delay added to every computed Next
// sleep, spreading out workers that would otherwise wake on the same deadline.
const maxJitter = 50 * time.Millisecond

// DefaultVisitedCap is the per-run ceiling on the visited ZSET's cardinality
// (ADR-0027): ~5M distinct URLs (~425 MB/run at ~85 B/entry) before FIFO
// eviction fires, so for most runs it never does. Overridable per Frontier via
// WithVisitedCap and process-wide via CRAWL_VISITED_CAP.
const DefaultVisitedCap = 5_000_000

// addScript fuses dedup with enqueue. KEYS: visited (a hashed ZSET, member =
// 8-byte xxhash64(RawURL), score = insertion ms), domains. ARGV: queuePrefix,
// domain, member, visitedKey(8-byte xxhash64), now(ms), cap. Dedup is ZADD NX,
// so an already-resident URL short-circuits as DUP with no further work. On a
// NEW insert it FIFO-evicts by rank down to cap (ADR-0027), pinning visited at
// the cap with no overshoot. Returns DUP (already visited) or NEW.
var addScript = redis.NewScript(`
local visited     = KEYS[1]
local domains     = KEYS[2]
local queuePrefix = ARGV[1]
local domain      = ARGV[2]
local member      = ARGV[3]
local visitedKey  = ARGV[4]   -- 8-byte xxhash64(RawURL), binary; never parsed
local now         = tonumber(ARGV[5])
local cap         = tonumber(ARGV[6])

-- Dedup: ZADD NX reports how many NEW members it added. 0 => the URL is already
-- resident, so short-circuit as a no-op with NO score bump (the hot duplicate
-- path stays a single write; FIFO order is not disturbed by re-sees).
if redis.call('ZADD', visited, 'NX', now, visitedKey) == 0 then
  return 'DUP'
end

-- Make the domain eligible immediately; NX so an active cooldown is not reset.
redis.call('ZADD', domains, 'NX', now, domain)
redis.call('LPUSH', queuePrefix .. domain, member)

-- FIFO eviction runs ONLY on a NEW insert: shed everything above the cap by
-- rank (rank 0 = lowest score = oldest inserted), pinning visited at the cap
-- with no overshoot and no background sweeper.
local over = redis.call('ZCARD', visited) - cap
if over > 0 then
  redis.call('ZREMRANGEBYRANK', visited, 0, over - 1)
end
return 'NEW'
`)

// nextScript reclaims expired leases, then hands out the earliest eligible URL.
// It relies on the non-empty-domains invariant: the domains schedule holds a
// domain only while its queue is non-empty (AddURL and lease-reclaim re-add it,
// a pop ZREMs it the moment its queue drains). So the earliest eligible domain
// is an O(log N) indexed lookup (ZRANGEBYSCORE ... LIMIT 0 1) rather than a scan
// of every domain ever seen. Both loops are bounded per call by Lua-local
// constants (maxReclaim, maxPrune) so no single pop can exceed Redis's Lua time
// limit (the #145 BUSY); leftover work is handled on later pops.
//
// KEYS: domains, processing, inflight. ARGV: queuePrefix, now(ms), leaseTTL(ms),
// cooldown(ms), pollInterval(ms). Every reply carries a uniform trailing element
// — the post-mutation domain-schedule cardinality (ZCARD domains) — appended by
// the Lua reply helper on every path, so Next records the domains.size gauge
// without inspecting the tag. It is appended (trailing), so all tag/payload
// index reads below are unaffected. The leading tag and payload are:
//
//	{'URL', member, card}   — a URL to crawl
//	                          (member is depth\x1fhostname\x1fscope\x1fowner\x1furl)
//	{'BADMEMBER', m, card}  — a queued member predating the #118 provenance format
//	                          (fewer than four separators); pushed back with a
//	                          fresh cooldown (its domain stays scheduled, its
//	                          queue being non-empty) and surfaced by Next as an
//	                          error, so stale state is flushed rather than silently
//	                          crawled unfenced.
//	{'WAIT', wakeMs, card}  — nothing ready; caller sleeps until wakeMs then
//	                          retries. For a real deadline wakeMs is bounded by
//	                          now+pollInterval, the earliest future domain
//	                          deadline, and the earliest in-flight lease expiry, so
//	                          reclaim latency tracks leaseTTL rather than a domain
//	                          cooldown. When the defensive prune cap is hit before
//	                          a poppable member is found, wakeMs is now itself — an
//	                          immediate retry that resumes stale-domain cleanup on
//	                          the next pop.
//	{'DONE', card}          — queues empty and no leases in flight
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

-- Per-pop bounds keep every atomic pop within Redis's Lua time limit (the #145
-- BUSY root cause). 256 keeps a pop's worst-case work sub-millisecond, while a
-- large pre-fix backlog (tens of thousands of stale domains) self-heals over a
-- few rapid cleanup pops rather than one BUSY-tripping script. Overflow of
-- either loop is handled on subsequent pops. These are Lua-local constants,
-- never plumbed configuration.
local maxReclaim = 256   -- expired leases reclaimed per pop
local maxPrune   = 256   -- stale/empty domains pruned from the schedule per pop

-- wakeDeadline bounds a WAIT sleep to now+pollInterval and to the earliest
-- in-flight lease expiry. It is only ever reached when the reclaim loop found no
-- expired lease this pop (an expired lease would have re-scheduled its domain at
-- now and made it eligible, avoiding this branch), so every processing score
-- here is in the future. candidate is a concrete future domain deadline, or nil.
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

-- reply appends the current domain-schedule cardinality as a uniform trailing
-- element on every tag, so Next can record the domains.size gauge without
-- knowing the tag. It is evaluated at each return site (after that path's
-- ZADD/ZREM), so the value is the post-mutation schedule cardinality.
local function reply(...)
  local r = {...}
  r[#r + 1] = redis.call('ZCARD', domains)
  return r
end

-- 1. Reclaim up to maxReclaim expired leases: re-enqueue the exact member (with
-- its depth) onto its domain queue and re-schedule the domain (NX preserves an
-- active cooldown; adds a drained domain back at now). Leases beyond the cap are
-- reclaimed on later pops.
local expired = redis.call('ZRANGEBYSCORE', processing, '-inf', now, 'LIMIT', 0, maxReclaim)
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

-- 2. Select the earliest-eligible non-empty domain by indexed lookup. Under the
-- non-empty-domains invariant a scheduled domain has queued work; a domain whose
-- queue is unexpectedly empty (pre-fix bloat or an invariant slip) is pruned and
-- the next candidate tried, up to maxPrune per pop.
local candidate, member
local pruned = 0
while pruned < maxPrune do
  local sel = redis.call('ZRANGEBYSCORE', domains, '-inf', now, 'LIMIT', 0, 1)
  if #sel == 0 then
    -- Nothing eligible now. Wake at the earliest FUTURE domain deadline; with no
    -- domains at all, wait on in-flight leases, or finish.
    local future = redis.call('ZRANGE', domains, 0, 0, 'WITHSCORES')
    if #future > 0 then
      return reply('WAIT', tostring(wakeDeadline(tonumber(future[2]))))
    end
    if redis.call('ZCARD', processing) > 0 then
      return reply('WAIT', tostring(wakeDeadline(nil)))
    end
    return reply('DONE')
  end
  candidate = sel[1]
  member = redis.call('RPOP', queuePrefix .. candidate)
  if member then
    break
  end
  -- Queue empty: prune the stale/drained domain and try the next.
  redis.call('ZREM', domains, candidate)
  pruned = pruned + 1
end

-- redis.call RPOP on a missing key returns Lua false (not nil), so this tests
-- falsiness with "not member" rather than an equality against nil.
if not member then
  -- Prune cap hit without a poppable member: WAIT with a past deadline, i.e. an
  -- immediate retry that continues stale-domain cleanup on the next pop.
  return reply('WAIT', tostring(now))
end

-- url is the last field: walk past depth, hostname, scope, owner to the 4th
-- separator, then take the rest. Consecutive separators (empty scope/owner)
-- are handled because string.find advances one position per call. Each step is
-- guarded so a member with fewer than four separators -- one written before the
-- #118 provenance format -- yields an actionable BADMEMBER instead of crashing
-- on arithmetic over a nil find; it is pushed back and its domain kept scheduled
-- with a fresh cooldown (queue non-empty) so the error loop is throttled.
local p = string.find(member, sep, 1, true)                 -- after depth
if p then p = string.find(member, sep, p + 1, true) end     -- after hostname
if p then p = string.find(member, sep, p + 1, true) end     -- after scope
if p then p = string.find(member, sep, p + 1, true) end     -- after owner
if not p then
  redis.call('LPUSH', queuePrefix .. candidate, member)
  redis.call('ZADD', domains, now + cooldown, candidate)
  return reply('BADMEMBER', member)
end
local url = string.sub(member, p + 1)

-- Non-empty-domains invariant: if this pop drained the queue, remove the domain
-- from the schedule (a later URL re-enters it as immediately eligible -- the
-- accepted cooldown-reset-on-redrain, ADR-0026); otherwise re-schedule it a
-- cooldown out so its remaining work stays politely spaced.
if redis.call('LLEN', queuePrefix .. candidate) == 0 then
  redis.call('ZREM', domains, candidate)
else
  redis.call('ZADD', domains, now + cooldown, candidate)
end
redis.call('ZADD', processing, now + leaseTTL, url)
redis.call('HSET', inflight, url, member)
return reply('URL', member)
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
	visitedCap   int                     // per-run visited ZSET ceiling; FIFO-evicted past this (ADR-0027)
	retryMin     time.Duration           // first backoff before a transient-error retry (default 100ms)
	retryMax     time.Duration           // backoff cap; retries continue at this interval (default 5s)
	retries      metric.Int64Counter     // crawler.frontier.transient_retries, op-attributed
	runID        string                  // run's UUID string; the run_id attribute on domainsSize
	popLatency   metric.Float64Histogram // crawler.frontier.next.time (ms), label-free
	domainsSize  metric.Int64Gauge       // crawler.frontier.domains.size, run_id-labeled
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

// WithVisitedCap sets the per-run ceiling on the visited ZSET's cardinality;
// once exceeded, the oldest-inserted entries are FIFO-evicted inline in the add
// script (ADR-0027). Defaults to DefaultVisitedCap.
func WithVisitedCap(n int) Option {
	return func(f *Frontier) { f.visitedCap = n }
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
		visitedCap:   DefaultVisitedCap,
		retryMin:     100 * time.Millisecond,
		retryMax:     5 * time.Second,
		runID:        runID.String(),
	}

	for _, opt := range opts {
		opt(f)
	}

	// Created after options so the instruments are always present even if a test
	// swaps the backoff bounds; nil-safe (no-op instruments on a registration
	// error), so Record/Add is always unconditional.
	f.retries = newTransientRetryCounter()
	f.popLatency = newPopLatencyHistogram()
	f.domainsSize = newDomainsSizeGauge()

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
	// At-least-once safe: addScript is one atomic Lua script whose first mutation
	// is ZADD NX visited. If a prior attempt fully applied but its reply was lost
	// to a transient blip, the retry sees ZADD NX -> 0 and returns DUP, so LPUSH
	// never doubles. Because FIFO eviction lives only on the NEW path, that
	// short-circuited retry also never evicts a second time. time.Now() is read
	// inside the closure so that a retry whose prior attempt did NOT apply stamps
	// a current domain-eligibility timestamp into ZADD domains NX; a fully-applied
	// retry short-circuits at ZADD NX -> DUP and never reaches that ZADD.
	res, err := f.withRetry(ctx, opAdd, func() (any, error) {
		return addScript.Run(ctx, f.client, keys,
			f.queuePrefix, url.Hostname, encodeMember(url), visitedMember(url.RawURL),
			time.Now().UnixMilli(), f.visitedCap,
		).Result()
	})
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
			start := time.Now()
			r, rerr := nextScript.Run(ctx, f.client, keys,
				f.queuePrefix, time.Now().UnixMilli(),
				f.leaseTTL.Milliseconds(), f.cooldown.Milliseconds(),
				f.pollInterval.Milliseconds(),
			).Result()
			if rerr == nil {
				// Pop-script evaluation latency in fractional ms (sub-ms pops).
				// Recorded only on a successful eval, so transient-retry backoff
				// (between closure calls) is excluded; the WAIT sleep a WAIT reply
				// triggers is in the switch below, so it too is excluded.
				f.popLatency.Record(ctx, float64(time.Since(start).Microseconds())/1000)
			}
			return r, rerr
		})
		if err != nil {
			return crawler.URL{}, fmt.Errorf("frontier: next: %w", err)
		}

		reply, ok := res.([]interface{})
		// Every real reply carries a leading tag plus the trailing schedule
		// cardinality: DONE is len 2, the others len 3.
		if !ok || len(reply) < 2 {
			return crawler.URL{}, fmt.Errorf("frontier: unexpected next result %v", res)
		}
		// Uniform trailing element = current domain-schedule cardinality; record
		// the run-scoped gauge before switching on the tag. Best-effort: a
		// malformed value is skipped, never fatal to a pop.
		if card, cerr := strconv.ParseInt(fmt.Sprint(reply[len(reply)-1]), 10, 64); cerr == nil {
			f.domainsSize.Record(ctx, card, metric.WithAttributes(attribute.String("run_id", f.runID)))
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
	// At-least-once safe: doneScript's ZREM/HDEL are idempotent, so re-running on
	// an already-cleared lease is a harmless no-op. The script's return value (1)
	// is unused, so .Result() feeds the withRetry closure shape and is discarded.
	if _, err := f.withRetry(ctx, opDone, func() (any, error) {
		return doneScript.Run(ctx, f.client, keys, url).Result()
	}); err != nil {
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

// visitedMember hashes a URL's RawURL into the 8-byte big-endian xxhash64 that
// keys it in the visited ZSET (ADR-0027). visited members are never parsed back,
// so a raw binary key is fine and halves per-entry memory versus a full URL. The
// hash is pinned: dedup across a run depends on it, so changing the function or
// its byte layout is a dedup-breaking migration, never a casual swap.
func visitedMember(rawURL string) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], xxhash.Sum64String(rawURL))
	return string(b[:])
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
