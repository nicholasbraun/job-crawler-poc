# Additive runtime Seed injection into the Discovery Crawl

The perpetual Discovery Crawl grows its Seed set over its lifetime. Adding a Seed
is one action that writes both durable and transient state: it appends
(idempotently) to the Crawl Definition's `seed_urls` so the Seed survives a
restart, and injects the URL into the running Run's Redis Frontier at depth 0 so it
takes effect immediately. Seed addition is additive-only (no remove or replace) and
Discovery-only — Keyword Crawls seed from the Catalog, never by hand.

## Consequences

This relaxes "a Crawl Definition is immutable once created" to allow additive edits
of a Discovery Definition's Seeds; keyword definitions stay fully immutable. The
Frontier interface is unchanged — injection reuses the ordinary depth-0 `AddURL`
(which already marks the domain immediately eligible), so an added Seed is crawled
promptly without a priority path, and the existing visited-set dedup silently
ignores a Seed already crawled this Run. Removing or replacing Seeds, and live
Discovery→Keyword seed propagation, are deferred to their own decisions.
