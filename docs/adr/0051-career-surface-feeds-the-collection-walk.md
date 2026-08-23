# The Collection walk follows a page's Career Surface

A Collection Cycle's walk does not drain. Measured live on run `4eefb7af` (2026-08-23,
`maxDepth` 7): **428,154 URLs added to the Frontier** against **51,383 pages crawled**,
with the Frontier growing faster than the workers empty it. The Cycle is bounded by
construction (ADR-0036) — it is supposed to finish — so a walk that never converges is a
defect, not a tuning problem.

The walk's job is narrow. Every seed is a **Career Page**: 1,446 non-dormant pages and
**zero** Pageless-Company Websites in the Catalog today, so from each seed the walk needs
exactly two things — that Company's Job Listings, and the pagination that reaches more of
them. Nothing else on the host is in scope.

But `getUrls` harvests `doc.Find("a[href]")` over the **whole document**
(`internal/parser/parser.go:53`), so every Career Page also contributes its host's entire
Site Chrome. Of the 24,739 depth-1 URLs in that Frontier — by definition the links
found *on* a Career Page — **67.4% lead nowhere near an opening**:

```
en 1314   de 691   product-table 484   themen 299   portfolio 293
design-resources 258   solutions 205   company 203   resources 198   about 177
```

That is **Site Chrome**, and it is the Frontier's dominant mass. This decision names the part
of a page the walk *does* follow — its **Career Surface** — and stops following the rest.

## The decision

On the Collection lane the walk enqueues a link when the link is on the page's **Career
Surface**. Three additive rules, any one of which puts a link on it:

1. **The main region.** `mainRegion` already picks the region `MainContent` is read from
   (`nav, header, footer, aside` stripped from the body, or a semantic container taken
   whole); the link harvest is pointed at that same selection, exactly as-is.
2. **Career-shaped, wherever it sits.** A link the crawl definition's own `passSubdomains`
   / `passPathSegments` already allow — `jobs`, `karriere`, `stellenangebote`,
   `vacancies`, `positions` and their kin.
3. **A same-path variant, wherever it sits.** A link that differs from the page it sits on
   only by query string.

Rule 1 is the lever; rules 2 and 3 exist to cover the two ways rule 1 can lose the very
links the walk is for.

**Rule 2 covers openings listed in the Site Chrome** — the failure `withoutChrome`'s own
comment names. Chrome is dropped for being the site tree, not for being a `<nav>`, and an
opening a site lists in its menu is still `/karriere/...` or `jobs.acme.de`. The
vocabulary is the one already stored on the Collection definition and already applied by
the URL filter chain, so this rule maintains nothing new.

**Rule 3 covers pagination.** `<nav aria-label="Pagination">` is the HTML spec's
recommended markup for a paging control and what every CSS framework ships, so rule 1 on
its own can drop the second of the two things this walk exists for: a board renders its
first 20 roles and the walk stops. A link differing from its own page only by query string
is another slice of one list by construction — structural, so unlike rule 2 it needs no
vocabulary in any language.

The harvest is carried as a **new field beside `Content.URLs` — never as a narrowing of
it**. The separate field is the load-bearing part, and the reason is not obvious from the
call site. `Content.URLs` has a reader that is not an enqueue loop: `countJobPostingLinks`
(`internal/pagegate/pagegate.go:503`) feeds **both** the Career Page Confidence Score's
job-link weight (ADR-0016) and the Extract Gate's rung 7 job-link saturation (ADR-0019).
Narrowing `URLs` in place would silently move two Gate rungs — a hub whose openings index
sits in the Site Chrome would stop saturating and start reaching the extractor — and would invalidate
every Gold Set fixture, whose stored `Content` was captured under the wide harvest
(ADR-0043). Gate changes are measured, not argued. Keeping the fields separate is what
makes this not a Gate change at all.

**Discovery keeps the whole-document harvest** and has no Career Surface: it finds
Companies *through* the editorial and Site Chrome links this drops, the same asymmetry ADR-0036
already grants Collection in its URL filter. Scoping the change to Collection costs
nothing structurally — the lanes already have separate enqueue loops
(`discovery_processor.go:123`, `url_processor.go:206`) — and the parser populates the new
field only for the lane that reads it, so Discovery does not pay a second walk over the
region on every page it fetches.

It ships **on**, behind `COLLECTION_CAREER_SURFACE_LINKS` (default `true`); pulling the
switch restores today's whole-document harvest.

## Considered options

- **Cap the crawl depth.** The obvious first lever, measured and rejected: `depth <= 4`
  cuts **22.0%** of the Frontier while losing **8.49%** of open crawl-sourced Job
  Listings — worse on *both* axes than any shape rule below. 39% of the Frontier sits at
  depth >= 4, and so do 14% of real listings; depth barely separates payload from waste.
  `maxDepth` 7 is not the problem.
- **A URL-shape allowlist as the primary rule** (posting path, hub/root, career path
  token, career subdomain). Genuinely strong on its own — **88.0%** Frontier cut for
  **4.40%** leaf loss, or **84.1%** for **3.14%** with an always-allow at `depth <= 1`.
  Rejected as *primary* on two grounds. It is a vocabulary that must be maintained in every
  language the Catalog reaches: the 518-listing miss set was dominated by one staffing
  company's per-locale slugs (`szczegoly-oferty-pracy`, `podrobnosti-nabidky-zamestnani`,
  `darbo-pasiulymai-detales`). And it structurally *cannot* catch a flat-slug posting page
  on a company root — `exobiosphere.com/usa/business-development-manager`,
  `cooledmotors.com/senior-mechanical-design-engineer` — which carries no career token
  anywhere in the URL. Main-region harvesting catches exactly that class, because those
  posting pages sit in the Career Page's content while `/solutions/trustpilot` sits in its
  Site Chrome. Kept as rule 2, where neither objection bites: a missing Polish slug costs nothing
  that rule 1 would not already have lost.
- **Order the Frontier instead of filtering it** (score the per-domain queue by URL shape,
  LIST -> ZSET in the pop script). Attractive because it drops nothing permanently and so
  needs no recall proof. Rejected as primary because it does not reduce *growth* — it only
  decides what the workers reach first. Still the right move if this change underperforms.
- **Ship it off and measure first.** The original form of this decision: land the two link
  sets side by side, run one Cycle, count the Job Listings reachable only through a chrome
  link, then flip. Rejected once rules 2 and 3 were added, on three grounds. The
  measurement was owed because rule 1 alone could lose openings-in-the-chrome and paginated
  boards, and those are precisely what rules 2 and 3 now catch — so it would have sized a
  risk that had been designed out. It could not have answered the question it was for
  anyway: the walk does not drain, so one Cycle only ever observes the *prefix* of the link
  graph that the unrestricted walk happened to reach, and the restricted walk would have
  spent its budget elsewhere. And it is not free — a Cycle is a full extract bill and a
  delay, paid while a known defect compounds. Validation moved to the live run instead; see
  the consequences.

## Consequences

- **The as-is carve-out limits the risk, not the benefit.** On a page with a semantic
  container `mainRegion` takes it whole, chrome inside included — but a site's `<nav>` is a
  *sibling* of `<main>`, not a child, so selecting the container already excludes the
  global menu. Only breadcrumbs, an in-content sidebar and an `<aside>` inside the
  container survive. Stripping those too was considered and dropped: it buys little and can
  empty a compact Career Page whose roles sit in an in-`<main>` sidebar.
- **Faceted navigation is rule 3's accepted cost.** A board with `?dept=`, `?loc=`,
  `?remote=` filters hands the walk a combinatorial set of query variants off one path, and
  rule 3 follows all of them. `blockedQueryParams` catches only session and cache traps
  (`tx_*`, `PHPSESSID`), not facets. This is growth in the one place this decision is
  trying to cut, and #203's per-scope enqueue cap is what actually bounds it.
- **`maxDepth` is now load-bearing for pagination.** A same-path variant increments depth
  like any other link — deliberately, because `maxDepth` is the only thing keeping the
  facet set finite. A numbered paginator reaches page 10 at depth 1 and page 100 at
  depth 2, so the bound rarely bites; a `Next`-only paginator chains one hop per page and
  dies at eight. Lowering `max_depth` is therefore no longer only a Frontier-volume knob,
  which the next person to move it (migration 0024 already took it 10 -> 7) should know.
- **Rule 2 admits nothing the URL filter would have blocked anyway.** `PassSubdomains` and
  `PassPathSegments` sit *before* every block rule in the chain and short-circuit it
  (`filter.ErrPass`), so a career-shaped link already bypasses the blocklists today. Rule 2
  reuses that predicate rather than adding a second, looser one.
- **Validation is a live Cycle, not an offline replay.** With the measurement dropped, the
  acceptance check is two numbers off `crawl_run` and one SQL count after one Cycle: URLs
  added to the Frontier against pages crawled, read against the 428,154 / 51,383 baseline,
  and new `source='crawl'` Job Listings in the window against the previous Cycle's. If the
  walk still does not converge, rule 3's facet exposure is the first suspect and Frontier
  ordering is the next lever.
- This does **not** fix #270. It reuses that machinery for a different consumer; page
  classification still reads chrome exactly as it does today.
- This does **not** bound a single Scope's contribution to the Frontier (#203) — a deep
  career subtree on one host is still unbounded, only narrower.
