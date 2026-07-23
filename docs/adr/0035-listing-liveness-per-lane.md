# Listing liveness: absence-from-board for ATS, refetch for the crawl lane

A Job Listing's Lifecycle is driven by liveness signals that differ per Source Lane, because
the two lanes have very different completeness guarantees. Listings **soft-close** (a
`closed_at` timestamp, never a delete); status is *derived* from `closed_at` (no stored enum);
a returning posting **reopens in place and keeps its original `first_seen`**, so a flapping
source never forges a "new" listing.

## ATS lane — absence-from-board, interlocked on a complete fetch

A board fetch returns the whole open set, so any Open listing for that board not in this fetch
is Closed. The sweep runs **only after a confirmed-complete fetch**: `err == nil` means
complete *by construction* (a fetcher fully paginates or errors; `io.LimitReader` truncation
already surfaces as a decode error). A distinct `ErrBoardIncomplete` means "save what we saw,
**skip the sweep**" — a partial or failed fetch must never mass-close a board. Where a provider
reports a total (SmartRecruiters `totalFound`), the fetcher cross-checks the count.

## Crawl lane — refetch, not walk-absence

Absence in the crawl walk is ambiguous (removed vs. merely missed), and the walk has no
completeness guarantee. So crawl-lane liveness is a direct **refetch** of each known-open
posting URL: `404`/`410` closes it authoritatively; a `200` whose source content is unchanged
keeps it Open **with no LLM call** (gated on a `source_hash` over the exact extractor input —
the vestigial output `content_hash` is replaced); changed content re-extracts. This
**decouples crawl-lane liveness from walk completeness** — a `404` is authoritative regardless
of what the walk enumerated. A staleness backstop closes only the persistently *inconclusive*
tail (repeated `5xx`/timeout/soft-404), and is **attempt-gated** so a down collector never
closes anything. Known-open URLs are seeded into the walk's visited set so discovery only
surfaces *new* postings: the refetch pass owns liveness, the walk owns discovery.

## Career-page dormancy

The same ladder one level up: a Career Page found hard-dead (a `404` board, or a page that no
longer classifies) for several consecutive Cycles goes **dormant**, drops out of the seed set,
and **Closes its remaining Open listings** — necessary because a dormant page is no longer
refetched, so its listings would otherwise never close. Dormancy is soft and reversible by
re-discovery. Its threshold is higher than the per-listing one (bigger blast radius).

## Note

This deliberately overrides the tracking issue's "refetch is expensive, keep it secondary":
in this pipeline the refetch *replaces* the walk's per-leaf GET rather than adding to it, and
buys authoritative, completeness-independent liveness.
