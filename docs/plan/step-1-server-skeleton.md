# Step 1 — Server skeleton + single managed run

**Prerequisites:** Step 0 (domain model) done. Read `docs/plan/README.md`, `/CONTEXT.md`,
ADR-0002 (stateless monolith), ADR-0006 (embedded frontend).

## Goal

Prove the distinctive target architecture end-to-end with the **smallest scope**: a
long-running server that starts/stops a single Crawl Run from a React dashboard, with
desired-state stop and an embedded frontend — while keeping today's crawl logic (in-mem
frontier, existing processors, SQLite listings) **verbatim**. This is the first vertical
slice; it defers the risky Redis frontier and the crawler kinds.

## Scope (build this)

- `cmd/server/main.go` — long-running process; wires deps; graceful shutdown (SIGINT → set
  running crawls to stopping, drain pools, then exit; **never `os.Exit` mid-run**). Leave
  `cmd/cli/main.go` working.
- `internal/api` — net/http router (Go 1.22+ `http.ServeMux`). JSON endpoints:
  `POST /api/crawls` (create + start a Run of a Crawl Definition), `POST /api/crawls/{id}/stop`,
  `GET /api/crawls`, `GET /api/crawls/{id}`.
- `internal/runner` — wraps `orchestrator.Run` in a goroutine per Run; writes Run
  status/counters to Postgres; drains pools on stop; flips terminal status. **Shape it so
  multi-run (Step 4) is a small extension**, but a single active run is fine here.
- `internal/orchestrator/orchestrator.go` — inject a **desired-state stop poll** into the
  loop: each iteration, check `crawl_run.status == 'stopping'` → `cancel`. Do **not** change
  `ErrDone` behavior yet.
- `internal/database/postgres/` — `pgx` pool + `goose`. Migration
  `migrations/0001_crawl_definition_crawl_run.sql`: `crawl_definition` (id, name, kind,
  seed_urls, keywords, max_depth, max_domains, url_filter jsonb) and `crawl_run` (id,
  definition_id, status incl. `stopping`, started_at, finished_at, counters). A `crawl_run`
  repo.
- `web/` — Vite + React + TS. One page: list crawls (React Query, `refetchInterval: 1500`)
  + Start/Stop buttons. API base from `VITE_API_BASE_URL` (default `/api`). Vite dev proxy
  `/api` → `:8080`.
- Embed: the built `web/dist` is embedded via `//go:embed all:dist` in a small `web`
  package (`web/web.go`, exposing `web.DistFS`) because `go:embed` cannot traverse parent
  directories from `cmd/server`; `cmd/server` imports it and serves it with SPA fallback.
- `docker-compose.yml` — add a Postgres service (the compose file already runs Grafana/
  Prometheus/Loki).

## Interface changes

None yet — defer `MarkDone`/`Save` changes to Steps 2–3.

## Out of scope

Redis, Discovery/Keyword kinds, Catalog, listing schema changes, multi-run.

## Verify

`docker compose up postgres`; `go run ./cmd/server`; open the dashboard via Vite dev.
Create + start a crawl → a `crawl_run` row goes `running`, counters tick every ~1.5s.
Click Stop → status `stopping` → `stopped`, pools drain cleanly (no goroutine leak, no
`os.Exit`). Confirm `go run ./cmd/cli` still works unchanged. `go test ./...` green.
