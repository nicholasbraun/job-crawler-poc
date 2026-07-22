# The robots.txt cache is a bounded per-process L1, with a shared L2 deferred to horizontal scaling; DNS stays process-local

## Context / Decision

The `Checker`'s robots.txt cache (`internal/robotstxt/cache.go`) kept one parsed
`Rules` entry per hostname in an unbounded map, never evicted, built once and
shared across every run for the whole process lifetime. A Discovery Crawl Run
resolves tens of thousands of distinct hosts (link spam, dead domains, and
subdomains all earn an entry, each holding rules parsed from a body capped at
1 MB), so the map grew monotonically. With no container `mem_limit` and no
`GOMEMLIMIT`, the Go heap climbed until the ~4 GB Docker VM's kernel OOM-killer
SIGKILLed the crawler (exit 137) — leaving its active runs stuck non-terminal.
Its sibling DNS cache (`internal/downloader/resolver.go`) is populated on the
*same* per-host axis but was deliberately bounded (16384 entries + TTL +
negative caching) precisely because discovery blows that axis up. The robots
cache is the same failure mode, unbounded.

We bound the robots cache in-process, mirroring the DNS cache:

- An entry carries an **expiry**; a read past expiry is a miss. TTL default
  **1h** — robots rules change on the order of days, so an hour keeps a hot
  host's checks off the network while bounding staleness.
- The map is **size-capped**, default **16384** hosts (symmetric with the DNS
  cache — a host in one is generally a host in the other). At capacity a store
  evicts expired entries first, then arbitrary ones, pinning the map at the cap.
- Both are set by `WithCacheTTL` / `WithCacheSize` options and the
  `ROBOTS_CACHE_TTL` / `ROBOTS_CACHE_SIZE` env overrides (the `CRAWL_VISITED_CAP`
  precedent).

This is the **L1** (per-process) tier. When the crawler moves to multi-process /
horizontal scaling, a shared **L2** — a *dedicated* cache-Redis instance,
separate from the Frontier's `noeviction` store — is added behind this same L1:
`allkeys-lru`, no persistence, negative caching, long TTL, storing raw
robots.txt bytes keyed by hostname. L1 stays in front so the hot path (a robots
check before every URL) hits local memory, not the network. L2 is out of scope
here; only the L1 bound ships now, and it is the tier the L2 slots behind.

## Considered options

- **Move the cache to the Frontier's Redis instance.** Rejected: that instance
  is `maxmemory 2gb` / `noeviction` by design (#75, ADR-0027) to protect
  non-regenerable crawl state. Regenerable cache data there either starves the
  Frontier's budget or, under `noeviction`, makes Redis reject writes — trading a
  restartable crawler OOM for a wedged Frontier (a run already died exactly this
  way, MISCONF "unable to persist"). Wrong store for evictable data.
- **Move it to a dedicated cache-Redis now.** Rejected *for now*, deferred to
  horizontal scaling: single-process, a network hop on the per-URL hot path (plus
  serializing/re-parsing `Rules`) is pure cost with no cross-process dedup to pay
  for it. It earns its keep only once N processes would otherwise re-fetch and
  re-hold every host's robots independently — captured as the L2 above.
- **A `robots_cache` table in the existing Postgres.** A shared, durable
  alternative to L2. Rejected: robots data is regenerable, so durability buys
  nothing, and it adds write load to Postgres; Redis-as-LRU is the natural fit
  for ephemeral memoization when L2 lands.
- **True LRU (bump on every hit).** Marginally better retention, but adds a
  `container/list` and per-hit bookkeeping for a cache whose goal is *bounding*,
  not hit-rate tuning. Rejected in favor of the DNS cache's proven cap + TTL +
  evict-expired-then-arbitrary, keeping the two caches consistent.
- **`GOMEMLIMIT` / container `mem_limit` alone.** A necessary seatbelt, but not a
  fix: an unbounded cache is *live* (reachable) memory the GC cannot reclaim, so
  the runtime would thrash GC against the soft limit and still fail. Bounding the
  cache is the root fix; the memory ceiling is complementary, tracked separately.
- **Leave DNS as-is (do not externalize).** Kept. DNS is already bounded, its
  entries are tiny, resolution is cheap and already shared via the upstream
  resolvers (1.1.1.1/8.8.8.8) every process points at, and it is the most
  latency-sensitive lookup. A Redis hop in front of it is a latency wash and a
  reliability downgrade for negligible dedup gain — even under horizontal scaling.

## Consequences

- **Re-fetch is the accepted price.** An evicted or expired host is re-fetched on
  next visit — one HTTP GET, deduplicated by the Checker's singleflight, and
  robots fetches share the crawl's DNS cache and connection pool. At 16384 hosts
  and a 1h TTL the cap rarely fires for a typical run; when it does, the cost is a
  re-fetch, never a correctness change.
- **Staleness is bounded by the TTL.** Rules refresh at most one TTL stale. For a
  crawler that only *gates* on robots, an hour of staleness is immaterial.
- **The bound is per-process; a fleet-wide bound is L2's job.** Until L2 lands,
  each process holds its own ≤16384-host cache — acceptable memory, redundant
  fetches across processes. Deduplicating those fetches is the reason L2 exists.
- **No new dependencies, no migration, no deploy step.** Pure in-process change;
  the cache was always ephemeral.
