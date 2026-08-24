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
//
// A Collection Cycle's Frontier also carries a Scope Budget (ADR-0053): each
// Scope may contribute at most scopeBudget URLs to the run, held as a monotonic
// per-Scope count in Redis, so the Cycle's visited set never has to forget and
// the walk can finish. The gate runs BEFORE the visited insert, so a rejected
// URL leaves no trace in the seen-memory, the domain schedule, or any queue; the
// one thing a rejection may write is a per-Scope truncation marker, at most once
// per Scope. It is inert for the Discovery Crawl, whose URLs carry no Scope.
package redis

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
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

// addScript fuses the Scope Budget gate with dedup and enqueue. KEYS: visited (a
// hashed ZSET, member = 8-byte xxhash64(RawURL), score = insertion ms), domains,
// scopeSpend (a HASH of Scope -> admissions charged this run), scopeTruncated (a
// HASH of Scope -> 1: one field per Scope this run has already announced as
// truncated; the value is never read, only the field's existence). ARGV:
// queuePrefix, domain, member, visitedKey(8-byte xxhash64), now(ms), cap, scope,
// scopeBudget.
//
// The Scope Budget gate (ADR-0053) runs first and rejects having touched nothing
// but the scopeTruncated marker. Dedup is then ZADD NX, so an already-resident URL
// short-circuits as DUP with no further work. On a NEW insert it charges the
// Scope's budget, then FIFO-evicts by rank down to cap (ADR-0027), pinning visited
// at the cap with no overshoot. Returns bare DUP; the table {'BUDGET', announce},
// where announce is 1 only on the FIRST link this run drops for this Scope — the
// once-per-Scope transition the scope.truncated counter and the WARN log key off;
// or the table {'NEW', evicted, size} — the count this insert FIFO-evicted and the
// post-eviction ZCARD visited (feeding visited.size / visited.evicted, #162).
var addScript = redis.NewScript(`
local visited        = KEYS[1]
local domains        = KEYS[2]
local scopeSpend     = KEYS[3]
local scopeTruncated = KEYS[4]
local queuePrefix    = ARGV[1]
local domain         = ARGV[2]
local member         = ARGV[3]
local visitedKey     = ARGV[4]   -- 8-byte xxhash64(RawURL), binary; never parsed
local now            = tonumber(ARGV[5])
local cap            = tonumber(ARGV[6])
local scope          = ARGV[7]            -- empty on a Discovery Crawl (ADR-0021)
local budget         = tonumber(ARGV[8])  -- <= 0 disables the gate

-- Scope Budget gate (ADR-0053). THE ORDER HERE IS A CORRECTNESS INVARIANT, not a
-- micro-optimisation: this must stay ABOVE the ZADD NX below. Gate after the dedup
-- insert and a rejected URL still consumes a visited slot, so a trap minting
-- unbounded distinct URLs saturates the ADR-0027 cap anyway, the Cycle starts
-- forgetting, and the ceiling argument collapses while the budget appears to work.
-- Gating first leaves a rejection with no trace in ANY URL-keyed structure -- not
-- the visited ZSET, not the domain schedule, not a queue -- so
-- ZCARD visited <= sum(Scope Budgets) + the ADR-0035 visited pre-pass holds by
-- construction.
--
-- The ONE thing a rejection may write is the scopeTruncated marker, and only via
-- HSETNX, which adds at most one field per Scope. That is safe precisely because
-- the marker is keyed on SCOPE -- a count fixed at cycle start from the Catalog,
-- the same bounded denominator the budget divides -- and NOT on URL: a trap
-- minting a million distinct links creates the field once and re-reads it a
-- million times. Never add a URL-keyed write to this branch; that is the mutation
-- the ceiling argument above forbids.
--
-- The marker, not arithmetic, is what makes the announcement once-per-Scope.
-- Detecting the transition as "the charge landed exactly on the budget" looks
-- equivalent and is not: the spend lives per RUN in Redis while the budget is
-- re-derived per PROCESS from a Catalog that keeps growing (cmd/server's run
-- factory runs again on every resume and adopt). A restart that shrinks the number
-- skips the transition forever -- the charge path is never reached again -- and one
-- that grows it fires a second time for the same Scope. Keyed on the Scope, the
-- announcement survives both, and survives a transient retry re-running this
-- script.
--
-- An empty scope (Discovery) or a budget <= 0 switches the gate off with no second
-- code path, mirroring the cap's own "<= 0 disables" fail-safe.
local budgeted = scope ~= '' and budget > 0
if budgeted then
  local spent = tonumber(redis.call('HGET', scopeSpend, scope)) or 0
  if spent >= budget then
    -- Announced on the FIRST link actually dropped, which is what Scope Truncation
    -- IS (a walk stopping short) -- not on the admission that spent the last unit:
    -- a Scope whose budget runs out on its very last link truncates nothing.
    -- HSETNX reports 1 only when it created the field, so this is exactly once per
    -- (run, Scope) however the derived number moves.
    return {'BUDGET', redis.call('HSETNX', scopeTruncated, scope, 1)}
  end
end

-- Dedup: ZADD NX reports how many NEW members it added. 0 => the URL is already
-- resident, so short-circuit as a no-op with NO score bump (the hot duplicate
-- path stays a single write; FIFO order is not disturbed by re-sees).
if redis.call('ZADD', visited, 'NX', now, visitedKey) == 0 then
  return 'DUP'
end

-- Charge the Scope Budget, and ONLY for a genuinely new admission: the dedup
-- short-circuit above means a re-sighting never reaches here, and lease reclaim /
-- member push-back live in the pop script, so queue churn cannot charge either.
-- Never decremented -- a counter that pops give back is a concurrency window a
-- trap refills forever, not a budget. The charge is a plain count and nothing keys
-- off its exact value: the truncation announcement is claimed on the Scope in the
-- gate above, deliberately NOT from this number.
if budgeted then
  redis.call('HINCRBY', scopeSpend, scope, 1)
end

-- Make the domain eligible immediately; NX so an active cooldown is not reset.
redis.call('ZADD', domains, 'NX', now, domain)
redis.call('LPUSH', queuePrefix .. domain, member)

-- FIFO eviction runs ONLY on a NEW insert: shed everything above the cap by
-- rank (rank 0 = lowest score = oldest inserted), pinning visited at the cap
-- with no overshoot and no background sweeper. The NEW reply then carries the
-- number of entries this insert evicted and the post-eviction ZCARD, both
-- derived from the single ZCARD above (no second ZCARD): size = card - evicted.
local card    = redis.call('ZCARD', visited)
local evicted = 0
-- A cap <= 0 DISABLES capping (unbounded; never evict): the fail-safe for a
-- misconfigured CRAWL_VISITED_CAP=0/negative, which would otherwise make over =
-- card and shed the WHOLE visited set on every insert, unbounding the re-crawl.
if cap > 0 then
  local over = card - cap
  if over > 0 then
    redis.call('ZREMRANGEBYRANK', visited, 0, over - 1)
    evicted = over
  end
end
return {'NEW', evicted, card - evicted}
`)

// markVisitedScript adds members to the visited ZSET without enqueuing them, so a
// later AddURL of the same URL short-circuits as DUP. KEYS: visited. ARGV: now(ms),
// cap, then the 8-byte hashed members. ZADD NX + the same FIFO-by-rank eviction as
// addScript, so the visited-cap invariant (ADR-0027) still holds. It deliberately
// touches neither the domains schedule nor the queues — a seeded URL is marked
// seen, not scheduled to crawl. Returns the post-eviction ZCARD visited.
var markVisitedScript = redis.NewScript(`
local visited = KEYS[1]
local now = tonumber(ARGV[1])
local cap = tonumber(ARGV[2])
for i = 3, #ARGV do
  redis.call('ZADD', visited, 'NX', now, ARGV[i])
end
-- A cap <= 0 disables capping (unbounded; never evict), matching addScript's
-- fail-safe so a misconfigured CRAWL_VISITED_CAP never wipes the visited set.
if cap > 0 then
  local card = redis.call('ZCARD', visited)
  local over = card - cap
  if over > 0 then
    redis.call('ZREMRANGEBYRANK', visited, 0, over - 1)
  end
end
return redis.call('ZCARD', visited)
`)

// markVisitedChunk bounds how many hashed members a single markVisitedScript call
// carries, keeping each script's work sub-millisecond and the ARGV bounded.
const markVisitedChunk = 1000

// MarkVisited seeds rawURLs into the run's visited set so the discovery walk skips
// them: the crawl-lane refetch pass owns liveness of known-open postings, so the
// walk should only surface NEW postings (ADR-0035). Each URL is hashed to the same
// 8-byte visited member AddURL uses, ZADDed NX under the same FIFO cap, so a later
// AddURL of the same URL returns DUP. Idempotent (ZADD NX) and wrapped in withRetry
// like AddURL, so a resumed Cycle re-seeds harmlessly. A no-op for an empty slice.
// It records visited.size and visited.cap from the script's post-eviction
// cardinality, so the pre-pass's claim on the ADR-0027 ceiling is visible on the same
// panel as the admissions' (ADR-0053).
func (f *Frontier) MarkVisited(ctx context.Context, rawURLs []string) error {
	if len(rawURLs) == 0 {
		return nil
	}
	keys := []string{f.key("visited")}
	attrs := metric.WithAttributes(attribute.String("run_id", f.runID))
	for start := 0; start < len(rawURLs); start += markVisitedChunk {
		end := start + markVisitedChunk
		if end > len(rawURLs) {
			end = len(rawURLs)
		}
		args := make([]any, 0, 2+(end-start))
		args = append(args, time.Now().UnixMilli(), f.visitedCap)
		for _, u := range rawURLs[start:end] {
			args = append(args, visitedMember(u))
		}
		res, err := f.withRetry(ctx, opAdd, func() (any, error) {
			return markVisitedScript.Run(ctx, f.client, keys, args...).Result()
		})
		if err != nil {
			return fmt.Errorf("frontier: mark visited: %w", err)
		}
		// The reply is the post-eviction ZCARD visited, recorded onto the same gauge (and
		// beside the same cap gauge) AddURL feeds: the pre-pass is the OTHER claim on the
		// seen-memory ceiling (ADR-0053), and without this a Cycle that seeds more than
		// the ceiling has room for is invisible — this script's own FIFO eviction happens
		// inside Lua, and visited.evicted is fed only from the add script. Both are
		// recorded together so the vs-cap panel always has the pair; chunked, so a long
		// pre-pass refreshes the last-value as it runs.
		if size, perr := strconv.ParseInt(fmt.Sprint(res), 10, 64); perr == nil {
			f.visitedSize.Record(ctx, size, attrs)
			f.visitedCapG.Record(ctx, int64(f.visitedCap), attrs)
		}
	}
	return nil
}

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
	client         *redis.Client
	keyPrefix      string
	queuePrefix    string
	cooldown       time.Duration
	leaseTTL       time.Duration
	pollInterval   time.Duration
	maxDepth       int
	mode           frontier.Mode
	visitedCap     int                     // per-run visited ZSET ceiling; FIFO-evicted past this (ADR-0027)
	retryMin       time.Duration           // first backoff before a transient-error retry (default 100ms)
	retryMax       time.Duration           // backoff cap; retries continue at this interval (default 5s)
	retries        metric.Int64Counter     // crawler.frontier.transient_retries, op-attributed
	runID          string                  // run's UUID string; the run_id attribute on domainsSize
	popLatency     metric.Float64Histogram // crawler.frontier.next.time (ms), label-free
	domainsSize    metric.Int64Gauge       // crawler.frontier.domains.size, run_id-labeled
	visitedSize    metric.Int64Gauge       // crawler.frontier.visited.size, run_id-labeled (ADR-0027 / #162)
	visitedEvicted metric.Int64Counter     // crawler.frontier.visited.evicted, run_id-labeled (ADR-0027 / #162)
	visitedCapG    metric.Int64Gauge       // crawler.frontier.visited.cap, run_id-labeled effective cap (ADR-0027 / #162)
	scopeBudget    int                     // per-Scope admission ceiling for this run; <= 0 disables (ADR-0053)
	scopeTruncated metric.Int64Counter     // crawler.frontier.scope.truncated, run_id-labeled (ADR-0053)
	scopeBudgetG   metric.Int64Gauge       // crawler.frontier.scope.budget, run_id-labeled effective budget (ADR-0053)
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
// script (ADR-0027). Defaults to DefaultVisitedCap. A value <= 0 DISABLES capping
// (unbounded; the visited set never evicts) — the fail-safe so a misconfigured
// CRAWL_VISITED_CAP=0 never evicts the whole set on every insert (which would
// unbound the re-crawl), mirroring the "<= 0 leaves the signal silent" convention.
func WithVisitedCap(n int) Option {
	return func(f *Frontier) { f.visitedCap = n }
}

// WithScopeBudget sets the per-Scope admission budget for this run: at most n
// URLs carrying a given Scope are admitted for the life of the run, and further
// ones are rejected with frontier.ErrScopeBudget (ADR-0053). The count is
// monotonic, held in Redis under the run's Frontier namespace, so it survives a
// pause and resume that reuse the run and is swept by DeleteRun. n may legitimately
// differ from the number an earlier process derived for the same run — the Catalog
// grows between derivations — so nothing keys off its exact value: the spend is a
// plain count, and the truncation announcement is claimed once per Scope (ADR-0053).
//
// The number is derived per Collection Cycle from the seen-memory ceiling and
// the Cycle's Seeds and handed in already computed; deriving it is deliberately
// NOT this package's job, which is what keeps Catalog- and Seed-shaped policy out
// of a lane-agnostic component. Defaults to 0, i.e. DISABLED — so the Discovery
// Crawl, whose URLs carry no Scope in any case, is unaffected by construction.
// A value <= 0 disables the budget entirely, mirroring WithVisitedCap.
func WithScopeBudget(n int) Option {
	return func(f *Frontier) { f.scopeBudget = n }
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
	f.visitedSize = newVisitedSizeGauge()
	f.visitedEvicted = newVisitedEvictedCounter()
	f.visitedCapG = newVisitedCapGauge()
	f.scopeTruncated = newScopeTruncatedCounter()
	f.scopeBudgetG = newScopeBudgetGauge()

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
// URL is a silent no-op (returns nil). Returns frontier.ErrMaxDepth if the URL is
// too deep, or frontier.ErrScopeBudget if its Scope has spent this run's Scope
// Budget (ADR-0053) — both expected client-side rejections, not failures. A
// budget rejection is decided inside the script before any URL-keyed mutation, so
// it leaves no trace in the visited set, the domain schedule, or any queue. Seeds
// are charged against their own Scope like any other admission; there is
// deliberately no depth-0 exemption.
func (f *Frontier) AddURL(ctx context.Context, url crawler.URL) error {
	if url.Depth > f.maxDepth {
		return frontier.ErrMaxDepth
	}

	keys := []string{f.key("visited"), f.key("domains"), f.key("scope_spend"), f.key("scope_truncated")}
	// At-least-once safe: addScript is one atomic Lua script whose first mutation
	// is ZADD NX visited. If a prior attempt fully applied but its reply was lost
	// to a transient blip, the retry sees ZADD NX -> 0 and returns DUP, so LPUSH
	// never doubles. Because FIFO eviction lives only on the NEW path, that
	// short-circuited retry also never evicts a second time. time.Now() is read
	// inside the closure so that a retry whose prior attempt did NOT apply stamps
	// a current domain-eligibility timestamp into ZADD domains NX; a fully-applied
	// retry short-circuits at ZADD NX -> DUP and never reaches that ZADD.
	//
	// The gate-first ordering also makes the retry safe at the budget boundary: if a
	// prior attempt applied and its reply was lost, the retry either short-circuits
	// at ZADD NX -> DUP (no second charge) or, when that attempt's charge landed on
	// the budget, is rejected as BUDGET — a URL that is in fact already enqueued, so
	// the caller's debug-and-skip loses nothing, and the truncation announcement is
	// not lost with it: the retry claims the Scope's marker instead (ADR-0053). One
	// window remains: if the reply of the rejection that CLAIMED the marker is itself
	// lost, the retry sees HSETNX -> 0 and the WARN is never written. That is one call
	// per Scope out of the thousands a truncated Scope rejects, against the
	// every-restart loss of keying the announcement on the budget's exact value.
	res, err := f.withRetry(ctx, opAdd, func() (any, error) {
		return addScript.Run(ctx, f.client, keys,
			f.queuePrefix, url.Hostname, encodeMember(url), visitedMember(url.RawURL),
			time.Now().UnixMilli(), f.visitedCap, url.Scope, f.scopeBudget,
		).Result()
	})
	if err != nil {
		return fmt.Errorf("frontier: add url: %w", err)
	}

	switch r := res.(type) {
	case string:
		if r == "DUP" {
			// Bare short-circuit: the hot duplicate path records nothing.
			return nil
		}
		return fmt.Errorf("frontier: unexpected add result %v", res)
	case []interface{}:
		attrs := metric.WithAttributes(attribute.String("run_id", f.runID))
		switch {
		case len(r) == 2 && fmt.Sprint(r[0]) == "BUDGET":
			// The Scope has spent its Scope Budget (ADR-0053): a hard drop of the newly
			// discovered link, shaped exactly like ErrMaxDepth so both processors handle
			// it through the path they already have. Returned bare (not wrapped) for the
			// same reason. Nothing URL-keyed was mutated — see addScript's ordering
			// invariant.
			//
			// r[1] is 1 only on the FIRST link this run dropped for this Scope: the
			// script claims a per-(run, Scope) marker with HSETNX, so the announcement is
			// keyed on the Scope, never on the budget's exact value. That is what keeps
			// it once per Scope across a resume that re-derives a different number — the
			// run factory re-derives on every resume and adopt, and the Catalog it
			// divides keeps growing.
			if announce, perr := strconv.ParseInt(fmt.Sprint(r[1]), 10, 64); perr == nil && announce == 1 {
				f.scopeTruncated.Add(ctx, 1, attrs)
				// Named once, at WARN: Scope Truncation is the accepted price of a Cycle
				// that ends, and the operator has to be able to read WHICH Companies paid
				// it, not merely how many. Once per Scope per run, so a trap host cannot
				// flood the log.
				slog.Warn("frontier: scope budget spent, truncating this scope: its links are dropped for the rest of the run",
					"scope", url.Scope, "budget", f.scopeBudget, "run_id", f.runID)
			}
			return frontier.ErrScopeBudget
		case len(r) == 3 && fmt.Sprint(r[0]) == "NEW":
			// NEW reply: {"NEW", evicted, size}. Record the visited and Scope Budget
			// instruments only here — the dup path above records nothing (#162).
			// Recording the gauge unconditionally on every NEW satisfies "gauge reflects
			// ZCARD visited"; Add(evicted) with evicted==0 under the cap still creates the
			// run_id series at 0, so a run that never evicts is observably at zero.
			if size, perr := strconv.ParseInt(fmt.Sprint(r[2]), 10, 64); perr == nil {
				f.visitedSize.Record(ctx, size, attrs)
			}
			if evicted, perr := strconv.ParseInt(fmt.Sprint(r[1]), 10, 64); perr == nil {
				f.visitedEvicted.Add(ctx, evicted, attrs)
			}
			// The effective per-run cap is static, so re-recording it on every NEW
			// only refreshes the last-value; recording it here (never on the DUP
			// path) pins it to the same NEW cadence and run_id series as
			// visited.size, so the vs-cap panel always has both to align.
			f.visitedCapG.Record(ctx, int64(f.visitedCap), attrs)
			// Scope Budget instruments (ADR-0053), on the same NEW cadence and run_id
			// series as visited.cap so a panel can align the budget with the ceiling it is
			// derived from. run_id is the ONLY label on either: a Scope label would mint a
			// metric series per Company as the Catalog grows.
			f.scopeBudgetG.Record(ctx, int64(f.scopeBudget), attrs)
			// Adding 0 on every NEW insert creates the run_id series at zero, so a run
			// that truncates nothing is observably at zero rather than absent — the same
			// reasoning as visited.evicted. The nonzero increments come from the BUDGET
			// branch above, once per truncated Scope.
			f.scopeTruncated.Add(ctx, 0, attrs)
			return nil
		}
		return fmt.Errorf("frontier: unexpected add result %v", res)
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
