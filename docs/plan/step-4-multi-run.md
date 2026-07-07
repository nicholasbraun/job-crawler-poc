# Step 4 — Multi-run + statelessness

**Prerequisites:** Step 3. Read ADR-0002 and `internal/pool/pool.go`.

## Goal

Run N concurrent Crawl Runs, each isolated, with all state externalized so any process can
adopt a Run (the seam to a future API + worker split).

## Scope (build this)

- `internal/runner` — manage N concurrent Runs. **Per-run pool lifecycle**: construct the
  url + job_listing worker pools when a Run starts, `Close()` them when it ends. Each Run
  uses its own Redis frontier namespace (`frontier:{run}:…`, already from Step 3).
- **Boot-time reconcile loop:** on server start, adopt runs whose Postgres status ∈
  {`running`, `stopping`} and resume them from Redis/Postgres.
- Ensure there is **no shared mutable in-memory state** across runs; all counters go through
  Redis/Postgres.
- `GET /api/crawls` lists all runs with live status.

## Interface changes

None (shapes were established in Steps 1–3).

## Out of scope

The Discovery/Keyword crawler kinds.

## Verify

Start 2 runs concurrently → both progress independently. Stop one → the other is
unaffected. Kill + restart the server → in-flight runs resume from externalized state (no
lost progress). `go test -race ./...` green.
