# Step 7 — React dashboard buildout

**Prerequisites:** Step 6. Read ADR-0006.

## Goal

Build the full dashboard UI over the REST API.

## Scope (build this)

- `web/` pages:
  - **Crawl Definitions + Runs** — list, create, start, stop.
  - **Catalog** — Companies + their Career Pages.
  - **Job Listings** — filterable by Definition / keyword.
  - **Live Run status** — counters (pages crawled, listings found, frontier size) via React
    Query polling (~1.5s).
- Finalize the `embed.FS` wiring and `VITE_API_BASE_URL` handling for the single-binary
  build.

## Interface changes

None (API is stable from earlier steps; add read endpoints as needed for the views).

## Verify

`vite build` embeds into the single Go binary; every view reflects Postgres/Redis state
within ~1.5s; start/stop from the UI works end-to-end against real crawls.
