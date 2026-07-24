# Refetch lane politeness: robots re-check and per-registrable-domain spacing

The Collection Crawl's refetch/dormancy lane (`collection.RefetchProcessor`, ADR-0035) probes each
crawled Career Page for liveness once per Cycle. Unlike the crawl walk it **bypasses the Frontier**
and GETs targets directly through `retryHTTPClient` (retry/backoff only), so it inherits none of the
Frontier's politeness guarantees: it never (re-)consults robots.txt, and it fires a Career Page's
probe plus every open listing back-to-back with no per-host spacing — an `N+1` burst per page per
Cycle, and across pages the pool runs `maxWorkers` concurrently with no per-host coordination. We
close both gaps with a single **politeness decorator** on the lane's `Downloader`, and extend
ADR-0035's liveness ladder with one new rule for a fresh robots disallow.

## Decision: a `politeDownloader` decorator

A `politeDownloader` wraps the existing `retryHTTPClient` and, per `Get`, does **robots first, then
space, then fetch**: `robots.Check(url)` → on allow, `HostLimiter.Wait(key)` → inner `Get`. It is
injected as the refetch lane's `Downloader`; `RefetchProcessor`'s fetch-and-classify code is
otherwise unchanged, because this lane's whole model already is "call `Get`, then classify whatever
comes back into a `ProbeOutcome`" (`classifyStatus`). A robots block is just another `Get` result.

Robots is checked **before** the spacing wait so a disallowed URL never burns a spacing slot, and
**once per logical request** (the decorator sits *outside* the retry client): re-attempts self-space
via the retry client's existing backoff / `Retry-After`, and robots does not change across a few
seconds of retry. One shared decorator (and one shared `HostLimiter`) is built **outside** the pool's
per-worker constructor — mirroring the ATS lane's `atsLimiter` — so a Career Page's probe and its N
listings, and two workers hitting the same platform from different pages, all share one spacing
bucket. Placement: the decorator, an `ErrRobotsDisallowed` sentinel, and a local one-method
`RobotsTxtChecker` interface live in `internal/collection`; `atsingest.HostLimiter` (ADR-0022) is
reused in place — `collection → atsingest` already exists (`RouteSeeds`), so this adds no package
edge — and the concrete `*robotstxt.Checker` (already built and cache-warm from the collection
url-worker pool) satisfies the local interface.

We reject the two alternatives. **Explicit `robots` + `limiter` deps on `RefetchProcessor`** is
viable but spreads a check→wait→get dance across both GET sites and grows the processor two deps with
no domain meaning (spacing is pure mechanics), duplicating the outcome mapping the classifiers already
own. **Routing refetch through the Frontier** is wrong on identity: the Frontier is per-run transient
*walk* state (per-domain queues, visited set, depth), while refetch iterates the persisted Corpus —
forcing it through the Frontier would mint fake walk state and still wouldn't honor robots crawl-delay
(the Frontier ignores it too).

## Robots re-check → decay, not close (extends ADR-0035)

Every URL the refetch lane touches was robots-allowed when first crawled, so this is a *re-validation*
that catches a `Disallow` added after first crawl. We keep it in scope for full contract parity, and
it is cheap because the robots cache is already warm from the concurrent url-worker pool. A fresh
disallow short-circuits with `ErrRobotsDisallowed`, which `classifyStatus` / `classifyPageProbe` map
to **`ProbeInconclusive`** — *decay, never a hard close*:

- **Not `ProbeDead`.** A transient robots.txt `5xx` is treated as `disallowAll` for 5 minutes
  (`robotstxt.DefaultUnavailableTTL`), so closing on a disallow would let one robots.txt outage
  mass-close a whole host in a single Cycle.
- **Not a freeze (skip with no probe).** The crawl walk is robots-gated too, so a permanently
  disallowed URL is never re-crawled either; a frozen listing would sit Open forever with a stale
  `last_seen` — data we may not re-verify, surfaced indefinitely.
- **`ProbeInconclusive` is literally "we cannot verify."** It reuses the existing attempt-gated stale
  backstop with no new outcome type: a transient robots.txt blip is at most one streak tick at daily
  cadence, while a durable disallow retires the listing after `DefaultCrawlStaleThreshold` (3) Cycles
  — soft and reopenable if robots later re-allows and the walk re-discovers it. Robots is checked
  per-URL (path-specific), so a disallowed Career-Page path never short-circuits its listings' own
  checks.

A Career Page whose robots starts disallowing us **wholesale** is left to linger cheaply rather than
promoted to a new dormancy trigger: its page probe folds to `Inconclusive` (never counts toward
dormancy per `NextDormancy`), the decorator short-circuits before any network GET (a cache-hit robots
check), and its listings decay out individually — self-healing on re-allow. Adding
"robots-disallowed for N Cycles → dormant" would pollute ADR-0035's hard-dead dormancy definition to
save a daily cache hit; not worth it.

## Spacing keys on the registrable domain, at 1s

The `HostLimiter` keys on `catalog.RegistrableDomain(host)` — the **eTLD+1**, not the full host —
because politeness is owed to *serving infrastructure*, and for this lane's diet (self-hosted pages,
clientless-ATS boards like `*.bamboohr.com`/`*.icims.com`, unrecognized-ATS platforms like
`*.myworkdayjobs.com`, and aggregators like `getro.com`) that infrastructure is the platform, not the
tenant subdomain. Full-host keying would let us fan `num_tenants` request streams at one shared
platform — precisely the hosts that run rate-limiting/WAF. The choice is asymmetric: eTLD+1 fails only
toward *over*-politeness (serializing sibling subdomains — slower, never abusive), while full-host
fails toward *under*-politeness (abuse risk). The PSL-aware `RegistrableDomain` is a good free
approximation: it auto-separates PSL-suffixed platforms (`*.webflow.io`, `*.github.io`) while folding
registration-shared ones. The 10 ATS providers with a registered board fetcher never reach this lane
(`RouteSeeds` sends them to the ATS Fetch lane, which paces per provider), so the one layout where
full-host over-serializes co-tenants (a shared host with per-path tenants, e.g. greenhouse) is already
routed away.

The interval is **1s** (`defaultRefetchRateInterval`) — the Frontier's blessed rate for arbitrary web
servers — not the ATS lane's 250ms, which is tuned for hardened board APIs. With eTLD+1 keying this is
exact parity with the walk for a self-hosted single host and much gentler for shared platforms, so no
gentler base is needed. No jitter (the `HostLimiter` chains reservations deterministically, so there
is no per-key herd to break up). Robots `Crawl-delay` stays ignored, matching the walk (it is dead
code there today); honoring it in refetch alone would break parity and require extending the robots
interface.

## Consequences

- One counter `collection.refetch.robots_blocked` (exported `collection_refetch_robots_blocked_total`,
  following its siblings' un-prefixed instrument naming) is tapped in the decorator via an optional
  hook, giving operators a friction gauge next to `collection.listings.closed`. It conflates a real
  `Disallow` with a transient robots.txt `5xx`, so a *spike* reads as a robots.txt outage more than
  mass adoption. A new `grafana/dashboards/collection.json` (the collection lane is currently
  un-dashboarded) charts the whole lane — found/refreshed/closed/boards/cycle-duration — with
  `robots_blocked` beside `closed`.
- This makes refetch **gentler than the walk**, which still keys its cooldown on the full host and so
  still bursts a shared platform's tenant subdomains in parallel each Cycle. Reconciling the Frontier
  cooldown (and robots `Crawl-delay` honoring, a natural companion) to the registrable-domain key is
  deferred to #237 — not blocking this change, which the issue explicitly allows to "run gentler."
