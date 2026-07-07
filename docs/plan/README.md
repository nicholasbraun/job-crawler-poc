# Implementation plan: CLI crawler → long-running server + React dashboard

This directory breaks the conversion into independent, self-contained steps. Each
`step-N-*.md` is written to be executed by a **fresh session** with no prior conversational
context: it states its prerequisites, scope, key files, interface changes, and how to
verify. Before starting any step, read this README, `/CONTEXT.md`, and the ADRs it
references.

## Target architecture (short)

A stateless Go monolith serves a REST API + an embedded React dashboard and runs crawl
goroutines, with **all state in Redis/Postgres**. Two crawler kinds: a perpetual
**Discovery Crawl** fills a **Catalog** of Companies + Career Pages; a few **Keyword
Crawls** seed from the Catalog and extract **Job Listings**. See `docs/adr/0001`–`0006`.

## Canonical terms

See `/CONTEXT.md` — use those words in code, API, and UI (Crawl Definition/Run, Discovery/
Keyword Crawl, Company, Politeness Domain, Career Page, Job Listing, Catalog, Frontier, Seed).

## Cross-cutting invariants (every step must honor)

- **Stateless (ADR-0002):** no authoritative state in Go memory. Run status, counters, the
  Catalog, and listings live in Redis/Postgres. **Stop = desired-state** `crawl_run.status
  = 'stopping'` that the crawl loop polls — not an in-process `cancel` alone.
- **The server (`cmd/server`) must NEVER `log.Fatalf`/`os.Exit` after boot.** SIGINT → set
  runs to stopping, drain pools, flip to terminal status. (Today Ctrl-C bypasses drain.)
- **Per-run pool lifecycle:** construct worker pools when a Run starts, `Close()` when it
  ends. `internal/pool` starts workers at construction and is single-use, so a global pool
  is a dead end for multi-run.
- **Land interface changes early and once:** `frontier.MarkDone(ctx, url)` (Step 3);
  `JobListingRepository.Save(..., definitionID, ...)` upsert (Step 2).
- **Frontier has a bounded vs perpetual mode:** Keyword Crawls end on `ErrDone`; Discovery
  Crawls never do (stop only via desired-state).
- **Keep `cmd/cli` working until Step 8.** Don't break the existing CLI while building the
  server path.
- `go test ./...` and `go test -race ./...` stay green after every step. Module
  `github.com/nicholasbraun/job-crawler-poc`, Go 1.26.

## Steps

0. **Domain model — DONE** (`/CONTEXT.md`, `docs/adr/0001`–`0006`).
1. [Server skeleton + single managed run](./step-1-server-skeleton.md) ← first vertical slice
2. [Postgres durable repositories](./step-2-postgres-repositories.md)
3. [Redis frontier + visited set](./step-3-redis-frontier.md)
4. [Multi-run + statelessness](./step-4-multi-run.md)
5. [Discovery Crawl kind (perpetual)](./step-5-discovery-crawl.md)
6. [Keyword Crawl kind (bounded)](./step-6-keyword-crawl.md)
7. [React dashboard buildout](./step-7-dashboard.md)
8. [Config + cleanup](./step-8-config-cleanup.md)

Each step is independently shippable and leaves the tree green. Steps are ordered by
dependency — later steps assume earlier ones are merged.
