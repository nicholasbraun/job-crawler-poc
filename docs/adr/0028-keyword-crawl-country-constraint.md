# Keyword crawls constrain Job Listings by resolved Country at save time

## Context

A Keyword Crawl should be limitable by job location — "only jobs in Germany",
"only in the US". Unlike Scope (ADR-0021), which fences a crawl by the seed's
URL-derived Company identity and can therefore reject a link before it is fetched,
a job's location is a content property of the individual posting: it is known only
after the page is downloaded and extracted (crawl lane) or the provider board is
fetched (ATS lane). So a location constraint cannot prune the Frontier — it can
only decide whether an already-acquired listing is kept.

## Decision

A Keyword Crawl Definition carries a **Country Constraint**: a set of target ISO
3166-1 alpha-2 Countries (empty = anywhere, the prior behavior). Each acquired Job
Listing's location is resolved to a Country (ADR-0029), and the listing is
discarded before persistence unless one of these holds:

- the target set is empty (anywhere), or
- its Country is in the target set, or
- its Country is unresolved (the empty Country), or
- its Work Arrangement is Remote (ADR-0030).

The discard is applied at save time on both acquisition lanes — the crawl lane's
job-listing processor and the ATS ingest processor — the location counterpart to,
and sitting beside, the existing keyword relevance match.

## Considered options / why discard, keep-unknown, and remote-override

**Discard on save vs. persist-everything and filter at read.** Discard mirrors the
keyword gate and the immutable-Definition model: a Definition constrains its own
output, and a different target is a different Definition. Persist-and-filter would
make one run's stored rows re-interpretable per viewer — something nothing else in
the model does — and would desynchronize the `ListingsFound` counter (tapped on
save) from the filtered view.

**Keep-unknown and remote-override bias to over-inclusion.** A listing whose
location does not resolve, or that is Remote, is kept rather than dropped. The
failure direction is deliberately safe: a weak resolver or an ambiguous location
*under*-filters (shows some extra jobs) instead of losing a real match, the same
false-drop-averse stance the Extract Gate takes (ADR-0020).

## Consequences

Retargeting a crawl's Countries means a new Definition and a re-crawl; the full
download/extract cost is still paid, so the constraint saves storage, not crawl
effort. And because the Catalog is Discovery-seeded toward Germany/EU, a
distant-country constraint (e.g. US) yields few results until the Discovery Seeds
reach companies that hire there — a known boundary of this feature, not a bug.
