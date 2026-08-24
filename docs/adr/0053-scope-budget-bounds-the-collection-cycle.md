# A Scope Budget bounds what a Collection Cycle admits to its Frontier

## Context / Decision

A Collection Cycle is **bounded by construction** (ADR-0036) — it is supposed to finish.
It does not. Measured live on run `8f4e231e` (2026-08-23/24, `maxDepth` 7, with the Career
Surface of ADR-0051 already on) at 13h41m:

| | |
|---|---|
| Pending in the Frontier | ~3,521,969 |
| Pages crawled | 1,478,031 |
| New crawl-lane Job Listings | 13,877 |
| `visited` cardinality | **5,000,000 — pinned at the cap** |
| `visited` evicted | **506,555** |

**64% of that pending set sits in eight hosts, all yielding zero Job Listings** —
`beck.de` (845k across three subdomains), `dblp.dagstuhl.de` (524k), `gettyimages.com`
(498k across two), `dxr.mozilla.org` (242k), `www.rp-online.de` (153k). These are not
fence failures: every one is inside a correct eTLD+1 Scope. They are single sites with a
huge crawlable surface — a publication database, a legal database, a stock-image
catalogue — whose main region is *made of* links, so the Career Surface does not thin them.

Worse, the Frontier's only existing bound now works against the Cycle. ADR-0027 caps
`visited` per run and accepts **Re-admission** as the price, reasoning correctly that a
perpetual Discovery Crawl has no termination condition to lose. A Cycle does: once
`visited` saturates, already-crawled URLs are forgotten, re-linked, and crawled again, so
the Cycle can never drain (#310). The memory bound and the termination guarantee are in
direct conflict, and today the memory bound wins silently.

**A Cycle therefore bounds what it admits, rather than relying on forgetting what it has
done.** Each **Scope** gets a **Scope Budget** — a number of URLs it may contribute to the
Cycle's Frontier — and once spent, further links from that Scope are dropped
(**Scope Truncation**).

- **Keyed on `Scope`, and only on the Collection lane.** The budget's whole point is a
  computable ceiling, `Σ_scopes budget`, and only the Scope key has a bounded denominator:
  a Cycle's Scopes are resolved from the Catalog at cycle start and frozen there, so the
  count is exact before the first URL is admitted. `Scope` is empty on a Discovery Crawl
  (ADR-0021), which switches the budget off with no second code path — the same "empty
  Scope means roam" convention `catalog.InScope` already uses.

- **A monotonic count of admissions, held per run.** One Cycle is one `CrawlRun`
  (ADR-0036), so the budget resets per Cycle for free, survives a pause/resume that reuses
  the run, and is swept by the existing `DeleteRun`. It is never decremented: a counter
  that pops give back is a concurrency window, not a budget, and a trap would refill it
  forever.

- **The budget gate runs *before* the `visited` insert, and that ordering is a
  correctness invariant, not a micro-optimisation.** `addScript`'s first mutation is
  `ZADD NX visited`. Gate after it and a rejected URL still consumes a `visited` slot —
  a trap mints unbounded distinct URLs, each rejected and each recorded, `visited`
  saturates anyway, and the ceiling argument collapses while the budget appears to work.
  Gating first leaves a rejected URL with no trace, so
  `ZCARD visited ≤ Σ_scopes budget + the ADR-0035 visited pre-pass` holds by construction.
  The sole exception is the truncation marker below, written at most once per Scope on the
  reject path — safe because the marker is keyed on Scope, whose count is fixed at cycle
  start, not on URL, so a trap cannot grow it; a rejection still touches nothing URL-keyed,
  and the bound is unchanged.

- **A spent budget hard-drops the link**, surfaced as `frontier.ErrScopeBudget` exactly
  as `ErrMaxDepth` already is: an expected client-side rejection both processors log at
  debug and skip.

- **The number is derived per Cycle, never configured.** With `scopes` = the distinct
  Scopes among the Cycle's crawl seeds:

  ```
  budget      = CRAWL_VISITED_CAP × 0.8      // headroom for the ADR-0035 visited pre-pass
  scopeBudget = clamp(budget / scopes, 500, 10_000)
  ```

  There is no tuning knob, only a kill switch (`COLLECTION_SCOPE_BUDGET_ENABLED`, default
  on) that restores the unbounded walk. A knob would reintroduce exactly the guesswork the
  derivation exists to remove.

- **Observability names Scopes, never labels by them.** A
  `crawler.frontier.scope.truncated` counter (`run_id`-labeled) increments **once** per
  Scope, on that Scope's **first actually-dropped link**, which the add script detects by
  claiming a per-`(run, Scope)` marker field with `HSETNX`; the Scope's *name* goes to a
  single WARN log. A per-URL drop counter would be dominated by re-sees of the same
  rejected URLs and would measure link-graph density rather than truncation; a Scope label
  would mint a series per Company. A `crawler.frontier.scope.budget` gauge carries the
  effective derived number, mirroring `visited.cap` so a panel can align the two.

  The transition must **not** be derived from the charge landing on the budget. The spend
  lives per run in Redis while the number is re-derived per process from a growing Catalog
  (the run factory re-derives on every resume and adopt), so a restart that *shrinks* it
  skips the announcement forever — the charge path is never reached again once
  `spent ≥ budget` — and one that *grows* it announces the same Scope twice. Keyed on the
  Scope, the announcement is once per Scope per run however the number moves, and it also
  survives a transient retry re-running the script. Announcing on the first dropped link
  rather than on the last admission is deliberate: that is what Scope Truncation *is*
  (`CONTEXT.md`), and a Scope whose budget runs out on its final link truncated nothing.

## Considered options

- **Key on hostname, or on the link's eTLD+1.** Would cover the Discovery lane too, and
  targets the observed shape more directly (the traps *are* hosts). Rejected: the host
  count is itself the runaway quantity — 20,832 hosts in one run, and `beck.de` spread its
  845k across three subdomains — so there is no bounded denominator to divide by. You
  cannot derive a ceiling from a number the adversary chooses.

- **Apply the same budget to the Discovery Crawl.** Rejected: on a perpetual run a
  monotonic per-key budget is a slow-motion shutdown. Every domain Discovery ever touches
  eventually exhausts its allowance, and the failure is indistinguishable from "the web ran
  out of Career Pages". A perpetual run needs a decaying or windowed allowance — a
  different mechanism with a different correctness argument. Discovery also has no
  termination guarantee to restore; ADR-0027 already accepts Re-admission there by design.

- **Enforce in the URL processor, before `AddURL`.** Keeps the hot Lua path untouched.
  Rejected on correctness, not cost: an in-process counter is per-worker, so
  `CRAWL_MAX_WORKERS` workers give an effective budget of N × the intended one, and it is
  lost on restart — a resumed Cycle would restart every budget at zero, defeating the
  per-Cycle bound precisely when it matters. `AddURL` is also the only choke point all four
  admission paths share.

- **Gate after the `visited` insert.** The natural reading, since the dedup short-circuit
  is the script's cheapest exit. Rejected — see the invariant above; it silently forfeits
  the ceiling. Recorded here because it is the mistake most likely to be reintroduced by
  someone tidying the script.

- **FIFO-evict the Scope's oldest queued URL instead of dropping the new one**, mirroring
  what ADR-0027 does for `visited`. Rejected three times over: it bounds the pending set
  but not the admissions, so newest-wins churn means the Cycle still never drains; the
  oldest entries in a Scope are the seed's own Career Page links, i.e. it discards exactly
  the shallow high-value URLs worth keeping; and `LREM` on a per-Politeness-Domain list is
  O(N) against an unknown list, needing a new per-Scope index maintained in the hot path.

- **A configured cap value.** Rejected: the number's authority comes from the arithmetic
  that ties it to `CRAWL_VISITED_CAP`. A knob invites tuning it against a yield curve that
  cannot honestly be measured — for the same reason ADR-0051's A/B was abandoned, an
  uncapped arm never drains, so it only ever explores a prefix of the link graph while the
  capped arm would have spent its budget elsewhere.

- **A yield-based circuit breaker** — stop admitting for a Scope that has crawled N pages
  and saved no Job Listing. Targets the real signal (all eight traps yield zero) and would
  not truncate a large, productive employer. Rejected for now: yield is a *lagging* signal,
  and the admission is the cost — `beck.de` must crawl thousands of pages before "zero" is
  meaningful, by which point the 845k are already in. It also needs a per-Scope save count
  threaded back from the save processor across the durable extract stream. Worth revisiting
  as a refinement once the budget makes Cycles drain.

- **Raise `CRAWL_VISITED_CAP` alone** (#310's fourth option). Rejected as a solution, but
  see the consequences: with a derived budget it becomes the correct *scaling* response
  rather than cliff-chasing, because the required cap is now a formula.

## Consequences

- **Scope Truncation is the accepted price**, and it can in principle cost a real Job
  Listing: one host inside a Scope can spend the whole budget and starve its siblings —
  `beck.de`'s legal-database subdomains burning the allowance before the walk reaches its
  career subdomain. We accept it on the strength of the Frontier's own ordering: the seed's
  Career Page is admitted first and its links land before a deep database crawl reaches
  critical mass, so "first N admitted" skews shallow. That is a prior, not a guarantee. If
  it bites, the repair is a fairness rule **inside** the Scope budget (reserving a share
  for the seed's own host), never a switch to host keying, which would forfeit the ceiling.
  Deliberately deferred rather than designed now.

- **The floor and the ceiling collide once the Catalog passes ~8,000 Scopes, and the floor
  wins.** Discovery is perpetual, so the Scope count only rises between Cycles:

  | Scopes | `budget / scopes` | effective | ceiling | under a 5M cap? |
  |---|---|---|---|---|
  | 1,100 | 3,636 | 3,636 | 4.0M | yes |
  | 4,000 | 1,000 | 1,000 | 4.0M | yes |
  | 8,000 | 500 | 500 | 4.0M | yes — exactly at the floor |
  | 20,000 | 200 | **500** | **10.0M** | **no** |

  Past the crossover the floor, not the division, sets the ceiling, and the termination
  guarantee lapses. The floor wins anyway, because a 200-URL allowance fails *every*
  Company's walk while an evicting `visited` degrades a *few*. It must not be silent: the
  Cycle logs a WARN at start when `floor × scopes > budget`, naming the
  `CRAWL_VISITED_CAP` that would restore the guarantee (`scopes × floor ÷ 0.8`, i.e.
  `scopes × 625` — 12.5M at 20,000 Scopes, ~1.06 GB at ADR-0027's ~85 B/entry, to be read
  against the 6 GiB seatbelt of #182 and the fact that a concurrent Discovery run holds its
  own separate cap).

- **The floor of 500 is provisional.** It is the one judgement call left in the number, and
  it is load-bearing — it decides when the guarantee expires. It can be pinned from data
  without shipping anything: `encodeMember` already writes `Scope` into every queued member
  (`depth\x1fhostname\x1fscope\x1fowner\x1furl`), so the live per-Scope distribution is
  readable off a running Cycle's Frontier. The floor should sit above the p99 of a real
  Company's contribution and the ceiling of 10,000 below the observed 100k–800k traps;
  both should be re-pinned against that read.

- **This does not close #310.** It removes the *cause* of the saturation, but #310's
  request to surface eviction as a run-level signal remains the right backstop for the
  derivation being wrong — including the case above where the floor overrides it. #310
  narrows to that.

- **Per-Politeness-Domain pending lists remain unbounded in both lanes.** ADR-0027 bounded
  `visited` only. At ~3.5M pending × ~100 B that is ~350 MB per run, and Discovery has no
  Scope to budget against. That wants a global per-run pending budget and is explicitly out
  of scope here; filed separately.

- **Seeds are charged against their own Scope**, since the orchestrator admits them through
  the same `AddURL`. One seed against a budget in the thousands is noise, so there is no
  depth-0 exemption.

- **A resumed Cycle shares the budget it had already spent**, because the counter lives
  under the run's Frontier namespace. That is the intended reading: the budget belongs to
  the Cycle, not to the process. The derived number may shift across a resume if the
  Catalog changed; the spend does not, and nothing may key off the number's exact value —
  the truncation announcement is keyed on the Scope for exactly this reason.

- **Validation is a live Cycle, not a benchmark.** The load-bearing unit test is that a
  truncated admission mutates *nothing* URL-keyed — `ZCARD visited` unchanged — which is
  what catches a regression of the ordering invariant; the second is that a Scope truncated
  across a resume that re-derives a *different* budget is still named exactly once. Live, against run `8f4e231e`'s baseline:
  `crawler_frontier_visited_evicted_total` stays at **0** (the direct test of the
  derivation, and #310's symptom gone), the pending peak falls far below 3.5M, crawl-lane
  new Job Listings hold near 13,877, and the WARN log names roughly the eight known
  offenders. The listing count cannot be A/B'd for the reason given above; what is testable
  is the one-sided claim that the Frontier collapses while the yield does not.
