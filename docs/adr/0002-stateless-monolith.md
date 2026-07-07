# Stateless monolith with externalized state

The service runs as a single Go binary that serves the REST API, the embedded dashboard,
and the crawl goroutines together — but keeps **no authoritative state in Go memory**. Run
status, counters, the Catalog, and Job Listings all live in Redis/Postgres, and stopping a
Crawl Run is a desired-state flag (`status = 'stopping'`) that the crawl loop polls rather
than an in-process `cancel` alone. We chose a monolith for POC simplicity while
externalizing all state so that lifting the crawl loop into separate worker processes later
is a refactor, not a rewrite.

## Consequences

Slightly more plumbing now — no convenient in-memory registry, all reads go to
Redis/Postgres — in exchange for a clean single-binary → distributed seam. A server restart
pauses in-flight runs but they resume from externalized state.
