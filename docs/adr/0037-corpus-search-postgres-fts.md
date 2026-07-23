# Corpus search via Postgres full-text search, not Elasticsearch

SavedSearches query the Corpus through Postgres FTS — a generated weighted `tsvector` + GIN,
`ts_rank` for ranking, `pg_trgm` for fuzzy — behind a narrow `SearchListings(ListingQuery)`
port (a concrete query struct, not a search-engine DSL) so an engine can be swapped in later
without touching callers. At POC corpus size (tens of thousands → low millions) FTS handles
keyword match, ranking, and structured Country/Work-Arrangement/liveness filters; Elasticsearch
would add a second stateful service and a perpetual sync problem for no benefit.

The text-search config is deliberately **`simple`** (no stemming, no stopwords), **not**
`english`: the Corpus mixes German and English postings, and an English stemmer mangles German
and silently shifts match semantics away from the current word-boundary matching. `pg_trgm`
covers the fuzzy tail. Per-language stemming is a later upgrade behind the same port — don't
"fix" `simple` to `english` without first handling the mixed-language corpus.
