# Crawl Definition vs Run, and Job Listing grain

We model a durable **Crawl Definition** separately from each **Crawl Run**, so
configurations can be re-run (and later scheduled) while Runs form an append-only execution
history. A **Job Listing** is keyed `(definition_id, url)` and updated in place (upsert on
`last_seen` / `content_hash`); a per-run sighting/history table is deliberately deferred as
a purely additive change.

## Consequences

Two entities and an upsert write-path now, instead of a flat "crawl" row that would be
painful to split after Job Listings already reference it. Deferring the sighting table keeps
the first migration small while leaving room to add change- and disappearance-detection
later without rewriting `job_listing`.
