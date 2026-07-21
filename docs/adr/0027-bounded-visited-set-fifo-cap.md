# The visited set is a FIFO-capped hashed ZSET, trading exact dedup for a bounded footprint

## Context / Decision

A perpetual Discovery Crawl Run's Frontier keeps a `visited` set — one entry per
distinct URL ever seen — added before a URL is enqueued and never trimmed. It
grows monotonically for the life of a run that by design never ends, marching
Redis toward its `maxmemory 2gb` / `noeviction` ceiling, at which point every
write is rejected and the crawl stalls. Since #145/#153 (ADR-0026) the two other
per-run vectors self-bound — the `domains` schedule and the per-domain queues now
drain on empty — so `visited` is the sole remaining unbounded footprint. #11
removed `maxDomains` as a false backstop and deferred a real one to #75; this is
that backstop.

We cap `visited` at a per-run cardinality and evict when it is exceeded:

- `visited` becomes a **ZSET**: member = `xxhash64(RawURL)` (8 bytes), score =
  insertion time in ms. Dedup is `ZADD NX` — an already-seen URL short-circuits
  as `DUP` with no further work, so the hot duplicate path stays a single write.
- Eviction is **FIFO** (drop the oldest-inserted) and runs **inline** in the
  atomic add script: after a `NEW` insert, `ZREMRANGEBYRANK` sheds the overflow,
  pinning `visited` at the cap with no overshoot and no background machinery.
- The cap is **per-run**, default **5,000,000** (~425 MB/run at ~85 B/entry),
  set by `WithVisitedCap` and the `CRAWL_VISITED_CAP` env override, applied
  consistently across every `Frontier` construction for a run.

It is a backstop, not a routine mechanism: 5 M distinct URLs is a lot of crawling
before the cap ever fires, so for most runs it never does.

## Considered options

- **Probabilistic filter (Bloom / Cuckoo).** Fixed memory with no eviction logic,
  but a false positive silently drops a never-seen URL — an unfixable coverage
  hole for a crawler whose whole job is discovery — and a plain Bloom's error
  *grows* as it saturates, precisely under the perpetual workload we are
  designing for. Cuckoo fixes both but needs the RedisBloom module, which the
  stock `redis:7` image does not carry. Rejected: trades a memory problem for a
  silent-correctness problem.
- **LRU eviction (bump score on every re-see).** Better at keeping hot hubs
  resident, but re-seeing a URL is the *majority* of `AddURL` calls in discovery,
  so LRU turns the cheap dup short-circuit into a `ZADD` write on the busiest
  path against a single-threaded Redis already shared across concurrent runs.
  Rejected: write-amplification where we can least afford it, for a marginal
  targeting gain.
- **Random eviction on the existing SET.** Keeps `visited` a plain SET, but
  evicts blindly — it can forget a URL from a currently-active domain about to be
  re-linked. Rejected: FIFO's oldest-inserted ≈ "domain no longer active" ≈ safe
  to forget, at the same order of cost.
- **Full-URL members.** Simpler and debuggable, but a ZSET of full URLs is a
  ~30% per-entry regression over today's SET, and hashing roughly doubles the cap
  headroom per MB. Rejected in favor of a 64-bit hash, accepting a negligible,
  non-growing collision risk (~10⁻⁶ that the run *ever* drops one URL at 5 M
  resident — the cap holds cardinality flat, unlike a saturating Bloom).
- **Per-run TTL / time-window dedup.** Expire the namespace or age out members.
  Rejected: a whole-namespace TTL never fires for a run that never idles, and a
  time window is a *soft* bound (memory tracks crawl-rate × window), not the hard
  ceiling a truly perpetual run needs.
- **Background sweeper.** Rejected: between sweeps `visited` overshoots the cap —
  at discovery insert rates, by a lot — under the very `noeviction` ceiling we
  are removing, and it adds a per-run goroutine and a non-atomic gap the inline
  version does not have.

## Consequences

- **Re-admission is the accepted price.** An evicted URL that is later
  re-discovered is treated as new and re-crawled. Politeness is intact — a
  re-admitted URL is paced by the same per-Politeness-Domain cooldown and
  re-checked against robots.txt at pop, so there is no re-crawl burst.
  Correctness is intact — the crawl is already at-least-once and idempotent
  (upserts under UNIQUE constraints). The only real cost is a re-run LLM classify
  when a re-admitted page *also* re-passes the pre-LLM Gate — compounded-rare
  (evicted **and** re-linked **and** gate-passing), and only past 5 M URLs. We
  **measure it rather than mitigate it**: a persistent catalog-stage dedup guard
  is a deliberate non-goal here, revisited only if the eviction metric shows the
  cap firing.
- **Observability:** a `crawler.frontier.visited.size` gauge and a
  `crawler.frontier.visited.evicted` counter, both `run_id`-labeled (the ADR-0026
  gauge-flap rule), fed from the add script's `NEW`-path reply. If `evicted`
  stays at zero the re-admission cost is provably nil.
- **The cap is per-run; the memory budget is global.** One Redis serves every
  concurrent run's `visited` plus its streams and schedule. A per-run cap bounds
  the box only alongside a bound on concurrent runs — deferred to "limit
  concurrent runs" and helped by ADR-0017 (Discovery is singleton per
  Definition). A cross-run global cap is out of scope.
- **Deploy step: flush/restart Redis once.** The new hashed ZSET is incompatible
  with the legacy full-URL SET in both type (`ZADD` on a SET is `WRONGTYPE`) and
  member format, and no dedup-preserving conversion is worth an O(N) startup
  stall. There is no in-code migration; `--save ""` already makes `visited`
  ephemeral across a Redis restart, so flushing once at this deploy is the whole
  procedure.
- `cespare/xxhash/v2` (already an indirect dependency, compiled into the binary)
  is promoted to a direct dependency. The hash function is pinned: changing it is
  a dedup-breaking migration, never a casual swap.
