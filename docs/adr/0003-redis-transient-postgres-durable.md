# Redis for transient crawl state, Postgres for durable data

Per-run crawl state — the Frontier (per-Politeness-Domain scheduling) and the visited set —
lives in Redis, keyed by run id, so Crawl Runs are resumable across restarts and can later
be shared by multiple worker processes. All durable data (Crawl Definitions, Crawl Runs, the
Catalog, Job Listings) lives in Postgres via `pgx` with `goose` migrations.

## Considered options

Keeping visited/frontier in Postgres couples transient hot-path state to the durable store;
keeping them in memory (the original POC) loses resumability. Splitting them — Redis for
throwaway per-run state, Postgres for the durable record — costs a second datastore to
operate in exchange for resumability and horizontal-scale readiness.
