# Retire the keyword-crawl lane in favor of the Corpus + SavedSearches

`CrawlKindKeyword`, the keyword `CrawlDefinition`s, and `CrawlDefinition.Keywords`/`Countries`
are removed. Keyword and country move from crawl-time pruning to query-time filtering: the
Collection Crawl harvests everything into the Corpus, and a SavedSearch answers "which jobs
match?" as a live query. Keeping a keyword lane would mean maintaining two write models
(definition-scoped rows vs. the global Corpus) and two result UIs for a strictly-dominated
feature — you cannot keyword-crawl a Company that Discovery has not already catalogued anyway.

The **Country Resolver survives**, but only in its ingest-tagging role (it tags each listing's
Country at save); the crawl-time country *gate* (`KeepForCountry`, ADR-0028) dies with the
lane.

## Migration — clean cutover, no backfill

A single-instance POC on one-shot goose migrations, so no dual-write coexistence period. Old
definition-scoped Job Listing rows are **dropped** — not re-keyed and deduped into the Corpus,
since they carry no canonicalization or lifecycle and the first Collection Cycle repopulates
the Corpus comprehensively from the Catalog. Old keyword definitions are **removed outright**,
not converted to SavedSearches. SavedSearches are created fresh.
