# Step 8 — Config + cleanup

**Prerequisites:** Step 7.

## Goal

Retire the CLI-era config and SQLite; finalize the deployment surface.

## Scope (build this)

- Env-var DSNs (Postgres/Redis) + config; migrate the `config.json` filter lists into
  `crawl_definition.url_filter` defaults; retire `internal/config/json_loader`.
- Retire `internal/database/sqlite`; reduce `cmd/cli` to a thin API client or delete it.
- README / deploy docs; single-binary build pipeline (`vite build` → `go build`, e.g. a
  Makefile target).

## Interface changes

None.

## Out of scope

New features.

## Verify

Fresh clone → `docker compose up` (Postgres + Redis) → `make build` produces a single
binary serving API + dashboard. No `sqlite` / `config.json` references remain in the server
path. `go test ./...` green.
