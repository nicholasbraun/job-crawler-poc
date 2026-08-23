# The Collection walk follows only main-region links

A Collection Cycle's walk does not drain. Measured live on run `4eefb7af` (2026-08-23,
`maxDepth` 7): **428,154 URLs queued** against **51,383 pages crawled**, with the queue
growing faster than the workers empty it. The Cycle is bounded by construction (ADR-0036)
— it is supposed to finish — so a walk that never converges is a defect, not a tuning
problem.

The walk's job is narrow. Every seed is a **Career Page**: 1,446 non-dormant pages and
**zero** Pageless-Company Websites in the Catalog today, so from each seed the walk needs
exactly two things — that Company's Job Listings, and the pagination that reaches more of
them. Nothing else on the host is in scope.

But `getUrls` harvests `doc.Find("a[href]")` over the **whole document**
(`internal/parser/parser.go:53`), so every Career Page also contributes its host's entire
navigation menu. Of the 24,739 depth-1 URLs in that frontier — by definition the links
found *on* a Career Page — **67.4% are not on the career surface at all**:

```
en 1314   de 691   product-table 484   themen 299   portfolio 293
design-resources 258   solutions 205   company 203   resources 198   about 177
```

That is site chrome, and it is the frontier's dominant mass. The parser already knows how
to drop it: `mainRegion` / `withoutChrome` strip `nav, header, footer, aside` for
`MainContent` (#270). This decision points the **link harvest** at that same region on the
Collection lane.

## The decision

On the Collection lane the walk enqueues only the links found in the page's main region,
carried as a **new field beside `Content.URLs` — never as a narrowing of it**. Discovery
keeps the whole-document harvest.

The separate field is the load-bearing part, and the reason is not obvious from the call
site. `Content.URLs` has a reader that is not an enqueue loop: `countJobPostingLinks`
(`internal/pagegate/pagegate.go:503`) feeds **both** the Career Page Confidence Score's
job-link weight (ADR-0016) and the Extract Gate's rung 7 job-link saturation (ADR-0019).
Narrowing `URLs` in place would silently move two Gate rungs — a hub whose openings index
sits in a nav would stop saturating and start reaching the extractor — and would invalidate
every Gold Set fixture, whose stored `Content` was captured under the wide harvest
(ADR-0043). Gate changes are measured, not argued. Keeping the fields separate is what
makes this not a Gate change at all.

Scoping it to Collection costs nothing structurally: the lanes already have separate
enqueue loops (`discovery_processor.go:123`, `url_processor.go:206`). And Discovery must
keep the wide harvest — it finds Companies *through* the editorial and nav links this
drops, which is the same asymmetry ADR-0036 already grants Collection in its URL filter.

## Considered options

- **Cap the crawl depth.** The obvious first lever, measured and rejected: `depth <= 4`
  cuts **22.0%** of the frontier while losing **8.49%** of open crawl-sourced Job
  Listings — worse on *both* axes than any shape rule below. 39% of the frontier sits at
  depth >= 4, and so do 14% of real listings; depth barely separates payload from waste.
  `maxDepth` 7 is not the problem.
- **A URL-shape allowlist** (posting path, hub/root, career path token, career subdomain).
  Genuinely strong — **88.0%** frontier cut for **4.40%** leaf loss, or **84.1%** for
  **3.14%** with an always-allow at `depth <= 1`. Rejected as the *primary* rule on two
  grounds. It is a vocabulary that must be maintained in every language the Catalog
  reaches: the 518-listing miss set was dominated by one staffing company's per-locale
  slugs (`szczegoly-oferty-pracy`, `podrobnosti-nabidky-zamestnani`,
  `darbo-pasiulymai-detales`). And it structurally *cannot* catch a flat-slug posting
  page on a company root — `exobiosphere.com/usa/business-development-manager`,
  `cooledmotors.com/senior-mechanical-design-engineer` — which carries no career token
  anywhere in the URL. Main-region harvesting catches exactly that class, because those
  posting pages sit in the Career Page's content while `/solutions/trustpilot` sits in
  its nav. Kept available as a backstop, not the rule.
- **Order the frontier instead of filtering it** (score the per-domain queue by URL shape,
  LIST -> ZSET in the pop script). Attractive because it drops nothing permanently and so
  needs no recall proof. Rejected as primary because it does not reduce *growth* — it only
  decides what the workers reach first. Still the right move if this change underperforms.

## Consequences

- **The known risk is stated in `withoutChrome`'s own comment**: some sites list their
  openings in a global nav. Those links leave the walk. `mainRegion` also takes a semantic
  container (`main`, `article`, `div#content`) *as-is*, chrome inside it included, so the
  strip only bites on pages with no semantic container — which bounds the benefit and the
  risk together.
- **Neither is measured yet.** The capture files store parsed `Content`, not raw HTML, so
  this could not be replayed offline the way a Gate change would be. It therefore ships
  **off**, behind `COLLECTION_MAIN_REGION_LINKS` (default `false` = today's whole-document
  harvest), and the two link sets are recorded side by side for one Cycle first. Flipping
  the default is owed that measurement: the count of Job Listings reachable only through
  chrome links is the number that decides it.
- This does **not** fix #270. It reuses that machinery for a different consumer; page
  classification still reads chrome exactly as it does today.
- This does **not** bound a single Scope's contribution to the frontier (#203) — a deep
  career subtree on one host is still unbounded, only narrower.
