# Two-phase crawling: broad Discovery, targeted Keyword Crawls

Crawling is split into a perpetual **Discovery Crawl** that finds Career Pages and Companies
(bounded-broad, reusing the URL-filter config) to fill the Catalog, and a small number of
**Keyword Crawls** that seed from the Catalog and extract Job Listings. Each Keyword Crawl
runs one crawl per keyword-set (e.g. `{golang, backend}`), with the cheap keyword filter
pruning pages *before* the expensive LLM extraction.

## Considered options

The alternative was to extract every Job Listing once and treat keywords as queries over
stored listings (crawl-once, query-many). We chose per-keyword crawls because keyword sets
are few and this minimizes LLM cost via pre-extraction pruning. Trade-off: adding a new
keyword set means a new crawl over the Catalog, not an instant query.
