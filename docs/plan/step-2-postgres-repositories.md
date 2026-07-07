# Step 2 — Postgres durable repositories

**Prerequisites:** Step 1. Read `/CONTEXT.md`, ADR-0003 (Postgres durable), ADR-0005
(listing grain), ADR-0001 (Company identity — for schema shape).

## Goal

Move durable data to Postgres and settle the Job Listing write-path interface **once**.

## Scope (build this)

- Migrations:
  - `company` — `company_key` (ATS-aware identity, unique), `display_domain`, `name`,
    `first_seen`, `last_seen`.
  - `career_page` — `company_id`, `url` (main Career Page), `politeness_domain`,
    `first_seen`, `last_seen`.
  - `job_listing` — **unique `(definition_id, url)`**; `company_id`, `title`, `description`,
    `location`, `remote`, `tech_stack`, `content_hash`, `first_seen`, `last_seen`.
- `internal/database/postgres/{job_listing,company,career_page,url}.go` implementing the
  repository interfaces. `URLRepository` here is interim (visited set) — it moves to Redis
  in Step 3.
- **Interface change (do it here, once):** `crawler.JobListingRepository.Save` gains
  `definitionID` and **upserts** on `(definition_id, url)`, updating `last_seen` /
  `content_hash`. Update `internal/job_listing.go` and `job_listing_processor`.
- Thread `definition_id` / `run_id` from `internal/runner` → `orchestrator` →
  `url_processor` → `job_listing_processor`.
- Retire SQLite from the **server** path (leave the sqlite package for `cmd/cli` until
  Step 8).

## Interface changes

`JobListingRepository.Save` signature (+ `definitionID`, upsert semantics).

## Out of scope

Redis frontier; ATS classification (this step defines the `company`/`career_page` schema;
population happens in Step 5).

## Verify

Migrations apply cleanly. A Step-1 crawl now writes `job_listing` rows to Postgres with
`definition_id`; re-running the same Definition updates `last_seen` rather than inserting
duplicates. `go test ./...` green, including Postgres repo tests (test Postgres or
dockertest).
