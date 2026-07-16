# Singleton Discovery Crawl; one active Run per Definition

There is exactly one Discovery Crawl and it runs perpetually, so we enforce two
invariants in the database with partial unique indexes rather than in application
code: at most one `crawl_definition` of kind `discovery`, and at most one
non-terminal `crawl_run` per `definition_id` (all kinds). A conflicting write
surfaces as `409 Conflict` (sentinel `ErrActiveRunExists`); the second invariant
also forbids running the same Keyword Definition concurrently, which we want.

## Considered Options

- **App-level check in the runner** — rejected: racy under two concurrent starts,
  and scoping it to discovery would leave the "one active run" invariant
  unenforced for keyword crawls.
- **DB partial unique indexes** — chosen: race-proof, and Reconcile/Resume mutate a
  run's status in place rather than inserting a row, so neither trips the index.

## Consequences

Terminal runs still accumulate as append-only history — the index covers only the
non-terminal statuses (`running`, `stopping`, `pausing`, `paused`) — preserving
ADR-0005. The status list is explicit (not `NOT IN (terminal)`) so a future status
defaults to non-blocking until deliberately added. `buildDiscovery` no longer hedges
over several discovery definitions. Re-authoring the seed set by deleting and
recreating the discovery definition is deferred to its own decision.
