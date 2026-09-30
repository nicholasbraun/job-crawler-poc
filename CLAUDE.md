# CLAUDE.md

This file provides guidance for AI coding agents working in this repository.

## Project Overview

Go server application implementing a web crawler for job listings, exposing a
REST API with an embedded React dashboard. Uses clean architecture
(ports-and-adapters): domain types and interfaces in `internal/`, infrastructure
implementations in sub-packages. Persists to PostgreSQL and holds transient
per-run crawl state (frontier queues, visited sets, durable LLM work streams) in
Redis. No web framework -- relies on the Go standard library for HTTP, logging,
and concurrency.

The domain has three parts:

- **Discovery Crawl** -- one perpetual, bounded-broad crawl that finds Career
  Pages, attributes them to Companies, and fills the **Catalog**.
- **Collection Crawl** -- a periodic **Collection Cycle** over the whole Catalog
  that harvests Job Listings into the **Corpus** and keeps their Liveness
  current, via two lanes (ATS board-API fetch, or crawl-and-extract).
- **SavedSearches** -- stored queries over the Corpus. Keyword and country filter
  at query time, never at crawl time (ADR-0038 retired the old keyword-crawl
  lane; do not reintroduce crawl-time keyword pruning).

## Domain Language (read before writing code or prose)

`CONTEXT.md` is the binding glossary. Every domain term used in code, comments,
commit messages, issues, and docs must match it -- including its `_Avoid_` list.
Never use an avoided word as a substitute for the term it lists (e.g. "ad",
"vacancy", or a bare "posting" for a **Job Listing**; "job board" for an
**Aggregator**) -- the terms `CONTEXT.md` itself builds on those words, like
**Posting Body** and "posting page", are fine. If a definition is stale, fix
`CONTEXT.md` rather than working around it.

`docs/adr/NNNN-slug.md` is the decision record. Decisions are cited inline in the
code they govern (`// ... (ADR-0035)`) -- ~500 such references across the tree.
When changing behavior an ADR describes, update the ADR or write a new one; when
adding a non-obvious constraint, cite the ADR that justifies it.

## Build / Run / Test Commands

The single binary embeds the compiled dashboard, so the dashboard must be built
before the server. A `Makefile` wires this together.

```bash
# Build everything: dashboard (web/dist) then the server binary (bin/crawler)
make build

# Build only the server binary (embeds the current web/dist)
make server-build      # == go build -o bin/crawler ./cmd/server

# Build only the dashboard
make web-build         # == cd web && npm ci && npm run build

# Run the server (needs Postgres + Redis reachable; see env vars below)
go run ./cmd/server

# Run the Vite dev server (proxies /api to a locally running server)
make dev

# Bring up the full stack (Postgres, Redis, crawler, observability) in Docker
make docker-up         # == docker compose up --build

# Run all tests
go test ./...          # or: make test

# Run tests with verbose output
go test -v ./...

# Run a single test by name (regex match against Test function names)
go test -v -run TestParseURL ./internal/

# Run a single subtest
go test -v -run TestParseURL/valid_url ./internal/

# Run all tests in one package
go test -v ./internal/database/postgres/
go test -v ./internal/frontier/redis/
go test -v ./internal/collection/
go test -v ./internal/pagegate/
go test -v ./internal/ats/
go test -v ./internal/downloader/
go test -v ./internal/parser/
go test -v ./internal/filter/

# Run tests with race detector
go test -race ./...     # or: make test-race

# Lint (same version CI runs; install with: brew install golangci-lint)
make lint              # == golangci-lint run ./...

# Format code
gofmt -w .
goimports -w .

# Catalog Doctor: replay today's URL-structural rules over the stored Catalog
go run ./cmd/doctor              # dry-run report
go run ./cmd/doctor --apply      # execute the plan
```

The Gate benchmarks (`go run ./cmd/llmbench <verb>`) are not listed here. Their
verbs are in `.claude/rules/llmbench.md`, a path-scoped rule that loads when a
file under `cmd/llmbench/` or `internal/pagegate/` is read; the full list is the
package comment in `cmd/llmbench/main.go`.

The server reads configuration from the environment (a `.env` file is loaded via
`godotenv` if present): `DATABASE_URL` (Postgres DSN), `REDIS_ADDR` (defaults to
`localhost:6379`), and `LLM_API_KEY` for the LLM classifier/extractor. The
classifier/extractor speak the OpenAI-compatible chat-completions API; override
`LLM_BASE_URL` and `LLM_MODEL` (defaulting to OpenRouter) to target any
compatible server, e.g. a local Ollama. Locally, prefer a non-reasoning instruct
model (e.g. `qwen2.5:3b`); reasoning models spend a hidden think phase the crawler
discards. `LLM_CLASSIFY_MAX_CHARS` / `LLM_EXTRACT_MAX_CHARS` (default 1500 / 8000)
cap the page text sent to each LLM call, keeping a local model fast.

Every knob is read in one block at the top of `cmd/server/main.go`, through a
single `env.Loader` (ADR-0045), each with a comment explaining why it exists.
`README.md` carries the full table. Use the typed accessor that matches the
knob's contract (`PositiveInt`, `PositiveDuration`, `Bool`, `Fraction`, `String`,
`Text`) and pass the default as a **typed value owned by the package it
configures** (`postgres.DefaultURL`, `robotstxt.DefaultCacheTTL`), never a string
literal. The Loader accumulates failures rather than exiting, so add reads to the
block and leave the single `ld.Err()` gate where it is -- nothing may act on a
knob above that line. The one exception is `LLM_*`, read by
`openrouter.ConfigFromEnv` so `cmd/server` and `cmd/llmbench` cannot drive the
model through different settings.

Several knobs are deliberate **kill switches** for paths that can go wrong
silently and at scale (`EXTRACT_FROM_JSONLD`, `EXTRACT_REQUIRE_POSITIVE_EVIDENCE`,
`COLLECTION_ENABLED`): when adding one, default it to the live behavior and say in
the comment what pulling it restores.

The repo has a `Makefile`, `Dockerfile`, and `docker-compose.yml`. CI
(`.github/workflows/ci.yml`) gates every push to `main` and every PR on: `gofmt
-l` (must be empty), `golangci-lint` v2.12.2 (config in `.golangci.yml`: the
standard set -- govet, staticcheck, errcheck, ineffassign, unused), `go build
./...`, `go test -race ./...` against real Postgres/Redis via testcontainers, plus
a dashboard typecheck + build. **Run `make lint` before pushing** -- `go vet` and
`gofmt` alone miss errcheck findings, the usual cause of a red build.

### Enforced Gates

Part of the above is enforced mechanically, so it holds whether or not an agent
has read this file:

- **Commit gate.** `.githooks/pre-commit` is a git hook, so git itself runs it
  for every `git commit` and every merge commit -- yours as well as an agent's,
  from any worktree, however the command is spelled. When the commit touches a
  Go file, `go.mod`, `go.sum`, `.golangci.yml`, or any file under a directory
  that holds Go code (a possible `go:embed` target), it exports the **staged
  snapshot** to a temporary directory and runs `gofmt -l .`, `go build ./...`
  and `golangci-lint run ./...` there: it checks what is being committed, not
  what happens to be on disk. When one fails the commit is aborted; fix it,
  stage the fix and commit again. It takes a few seconds. It does **not** run
  tests: `go test -race ./...` needs Docker and takes minutes, so it stays the
  committer's job. Each run appends one line to `.git/commit-gate.log`. Enable
  it once per clone with `git config core.hooksPath .githooks`.
- **Gate guard.** A PreToolUse hook (`.claude/hooks/commit-gate-guard.sh`) keeps
  an agent from switching the gate off. It refuses `--no-verify`, `git commit
  -n`, and any command naming `core.hooksPath` other than the enabling one above
  and `git config --get core.hooksPath`; and it refuses a commit in a clone
  where the gate is not enabled yet. It matches command text, so a commit
  message that quotes one of those is refused too -- pass the message with
  `git commit -F <file>`. Commits created by `git cherry-pick`, `git revert` or
  a rebase do not run the gate.
- **`ask` rules.** The usual spellings of these commands prompt the human, even
  in auto mode: `git push` (the `git -c ... push` form under Commit Messages and
  `git -C <dir> push` included), `gh pr merge`, the Catalog Doctor with its
  apply flag (`go run ./cmd/doctor --apply`), `go generate` (it runs the weights
  trainer), and llmbench's `goldset-refit` and `train-scorer` (through `go run`
  or `bin/llmbench`). They publish, merge, or rewrite stored or committed state.
  The rules match command text, so they are a prompt on the usual forms, not a
  boundary around the program. An agent that cannot get the prompt answered
  stops and reports; it does not look for another spelling of the command.
- **Attribution.** `attribution.commit` is empty, so the harness adds no
  `Co-Authored-By` trailer to commits.
- **Worktrees.** Agent worktrees branch from the current HEAD
  (`worktree.baseRef: "head"`), so they start from the commits made locally so
  far.

## Development Workflow

The two lanes are described in the user-level `~/.claude/CLAUDE.md`, which loads
alongside this file and is language-agnostic: the **feature / fix lane**
(optional `/research`, then `/grilling`, then `/deliver <spec#>` in a fresh
session) and the **one-off lane** (one fresh-context implementer, then review).
This section adds only what is specific to this repo.

- **The repo's checks.** Where a lane says "the repo's format, build, test and
  lint checks", that means `gofmt -l` (must print nothing), `go build ./...`,
  `go test -race ./...` and `make lint`. The commit gate above enforces all of
  them except the tests.
- **The one-off lane commits directly to `main`.** No branch, no PR. Push as
  described under Commit Messages.
- **Glossary and decision records.** Domain language goes to `CONTEXT.md`,
  decisions to `docs/adr/NNNN-slug.md` (see Domain Language above), and
  `/research` briefs to `docs/research/`.
- **Review checklist.** This repo's own review criteria are the
  `review-checklist` skill: a checklist, not a second review procedure. Nothing
  loads it automatically, so when you launch a reviewer in this repo, tell it in
  the prompt to load `review-checklist` alongside `/code-review`. Anything specific to this repo
  or to Go belongs there or elsewhere under this repo's `.claude/`, never in the
  shared skills and agents under `~/.claude/`.

## Project Structure

```
cmd/server/main.go           # Entry point: wires deps, serves REST API + dashboard, manages runs
cmd/doctor/                  # Catalog Doctor CLI: replays URL-structural rules over the Catalog
cmd/llmbench/                # Offline gate benchmarks + Gold-Set / Extract-Gold-Set tooling
web/                         # React/Vite dashboard; web/dist is embedded into the binary
grafana/dashboards/          # Provisioned dashboards (frontier, downloader, collection, llm, system)
docs/adr/                    # Architecture decision records (cited inline from the code)
internal/
  doc.go                     # Package "crawler" -- domain root
  url.go, job_listing.go, content.go, company.go, career_page.go, corpus_search.go,
    saved_search.go, crawl_definition.go, crawl_run.go, seed.go, liveness.go,
    dormancy.go, posting_body.go, structured_posting.go, source_hash.go, import_job.go
                             # Domain types + repository interfaces
  api/                       # REST API handlers over the repositories + runner
  ats/                       # Board-API clients for 10 ATS providers + the registry
  atsingest/                 # ATS Fetch lane: pool, per-tenant dedup, per-provider limiter
  catalog/                   # ATS-aware Company identity, Name Ladder, company snapshot
  catalogdoctor/             # Catalog repair engine (plan + apply)
  collection/                # Collection Cycle: seed routing, refetch/liveness, scheduler, politeness
  database/postgres/         # Postgres repositories, Corpus + FTS search, goose migrations
  downloader/                # Downloader interface, HTTP client, caching transport, retry decorator
  env/                       # Domain-free env lookup + accumulating config Loader
  extractcapture/            # Extract-decision tap feeding the Extract Gold Set
  filter/                    # Generic filter chain (CheckFn[T], Chain)
  filter/job_listing_filter/ # Job listing filters (title, main content keywords)
  filter/url/                # URL filters (TLD, subdomain, path, hostname)
  freeextraction/            # LLM-free extraction from unambiguous structured data
  frontier/                  # Frontier interface + sentinel errors
  frontier/redis/            # Redis-backed frontier (per-run queues, bounded visited set, leases)
  geo/                       # Deterministic location -> ISO country resolver + gazetteer
  importer/                  # Catalog import jobs (async, idempotent merge)
  listingid/                 # Canonical source-URL identity for Corpus rows
  llmobs/                    # LLM-stage metrics, stats, content-duplication probe
  llmstream/                 # Durable per-run LLM work stage over Redis Streams
  openrouter/                # LLM career-page classifier + job-listing extractor
  orchestrator/              # Crawl loop wiring all components
  otel/                      # OpenTelemetry + Prometheus metrics + pprof
  pagegate/                  # Pre-LLM Gate + Extract Gate (graded score, Positive Evidence)
  parser/                    # HTML parser (goquery): main content, links, structured data
  pool/                      # Generic worker pool
  processor/                 # Processor interface
  processor/url_processor/                 # URL processor (download, parse, filter, discover)
  processor/discovery_processor/           # Discovery crawl processor (fills the catalog)
  processor/career_page_processor/         # Career-page classification processor
  processor/job_listing_processor/         # Job listing processor (Corpus save)
  processor/shadow_extraction_processor/   # Measures Extract-Gate false-drops; never saves
  robotstxt/                 # robots.txt fetching, bounded caching, and matching
  runner/                    # Multi-run lifecycle: start, stop, pause, resume, adopt, drain
```

The dashboard's build output is gitignored except `web/dist/index.html`, which IS
tracked so the `//go:embed` compiles before a first `vite build`. When a web
source change ships, rebuild (`make web-build`) and commit the bumped asset hash
in `web/dist/index.html` alongside the source.

## Code Style

### Formatting and Imports

- Standard `gofmt` formatting, enforced by CI. Linting is `golangci-lint` with
  the v2 standard set (`.golangci.yml`); no custom rules beyond a few errcheck
  exclusions (`fmt.Fprint*`, `(io.Closer).Close`, and unchecked errors in tests).
- Import groups: (1) stdlib, (2) third-party / internal packages, separated by
  a blank line.
- The root `internal/` package is always aliased on import:
  `crawler "github.com/nicholasbraun/job-crawler-poc/internal"`
- Snake_case sub-packages are aliased to a run-together name on import:
  `careerpageprocessor ".../processor/career_page_processor"`,
  `redisfrontier ".../frontier/redis"`.
- Avoid import collisions with aliases (e.g., `myotel` for `internal/otel` when
  the OpenTelemetry SDK is also imported).

### Naming Conventions

- **Constructors:** `New<Type>(...)` -- e.g., `NewClient()`, `NewFrontier()`.
- **Interfaces:** Named by role/behavior, never prefixed with `I` -- e.g.,
  `Frontier`, `Downloader`, `Parser`, `URLRepository`.
- **Sentinel errors:** `var Err<Name> = errors.New("<package>: description")`.
- **Functional options:** `<Type>Option` type with `With<Option>()` functions --
  e.g., `FrontierOption`, `RetryClientOption`.
- **Unexported helpers:** camelCase -- e.g., `getTitle`, `isRetryable`.

### Types and Generics

- Domain types are simple structs with exported fields.
- `URL` is a value type; `Content` and `JobListing` are used as pointer types.
- Generics are used sparingly (e.g., `filter.CheckFn[T any]`, `filter.Chain[T]`).
- Initialize empty slices with `[]Type{}` literals, not `make`.

### Interface Compliance

- Assert interface satisfaction at package level with blank identifier:
  ```go
  var _ frontier.Frontier = &Frontier{}
  ```

### Error Handling

- Wrap errors with `fmt.Errorf("context message: %w", err)` using the `%w` verb.
- Error messages are lowercase.
- Use `errors.Is()` to check sentinel errors.
- Non-fatal errors in the crawl loop are logged and skipped (`continue`).
- Fatal startup errors use `log.Fatalf` in `main.go` only.

### Logging

- Use `log/slog` for all production logging: `slog.Info(...)`, `slog.Error(...)`.
- Structured key-value pairs: `slog.Error("msg", "err", err, "key", value)`.
- Do not use `log.Println` in library code.

### Context

- All interface methods accept `context.Context` as the first parameter.
- Tests use `t.Context()` instead of `context.Background()`.

### Concurrency

- `sync.Mutex` for shared in-process state (run registry, robots cache, ATS
  limiter and tenant dedup, worker pool).
- Signal channels (`chan struct{}`) for goroutine wakeup and done-signalling; a
  buffered one doubles as a semaphore (see `importer`).
- Use `select` with `ctx.Done()` for context-aware blocking.
- Cross-process coordination is Redis-mediated, not lock-mediated: the frontier's
  pop/add are single Lua scripts (atomic by construction, no in-process mutex),
  and the LLM stage uses a Stream consumer group with pending-list reclaim. When
  touching either, preserve atomicity in the script rather than adding a lock
  around it.
- Anything crossing a process restart must be idempotent: LLM-stage entries are
  redelivered on crash, so processors upsert on natural keys.

### Package Documentation

- Every package has a doc comment, either in `doc.go` or at the top of the
  main file: `// Package <name> ...`.

## Testing Conventions

- **Framework:** Standard library `testing` only. No testify, no gomock.
- **Test packages:** Use external test packages (e.g., `package crawler_test`,
  `package redis_test`) to test the public API.
- **Table-driven tests:** Use `t.Run("description", ...)` subtests.
- **Mocks:** Define mock/spy structs inline in test files. No code generation.
- **HTTP tests:** Use `httptest.NewServer` for integration tests.
- **Database tests:** Run against a real PostgreSQL instance spun up per test via
  `testcontainers-go` (`internal/database/postgres/helpers_test.go`), with all
  goose migrations applied. These require a running Docker daemon; a missing
  daemon surfaces as a test failure rather than a silent skip.
- **Time-dependent tests:** Use `testing/synctest` package:
  ```go
  synctest.Test(t, func(t *testing.T) {
      // ...
      synctest.Wait()
  })
  ```
- **Test helpers:** Use `t.Helper()` and place shared helpers in
  `helpers_test.go`.
- **Gate changes are measured, not argued:** a change to `internal/pagegate` (the
  career-page Gate or the Extract Gate) is scored offline against the labelled
  fixture sets via `cmd/llmbench` before it ships. The hard failures are
  irrecoverable ones -- a **Leak** or **False-Certain** on the Gold Set, a
  **false-drop** on the Extract Gold Set -- and the benchmark exits non-zero on
  any of them. Ground-truth labels are human-owned: never edit a label to make a
  run pass.

### Config / Dependency Injection Pattern

- Group dependencies in a `Config` struct, pass to constructor:
  ```go
  type Config struct { /* dependencies */ }
  func NewOrchestrator(cfg Config) *Orchestrator
  ```
- Use functional options for optional configuration:
  ```go
  func NewFrontier(opts ...FrontierOption) *Frontier
  ```

## Dependencies

| Package                                                                  | Purpose                               |
| ------------------------------------------------------------------------ | ------------------------------------- |
| `github.com/PuerkitoBio/goquery`                                         | HTML parsing with CSS selectors       |
| `github.com/jackc/pgx/v5`                                                | PostgreSQL driver + connection pool   |
| `github.com/pressly/goose/v3`                                            | SQL schema migrations                 |
| `github.com/redis/go-redis/v9`                                           | Redis client (frontier + LLM streams) |
| `github.com/google/uuid`                                                 | UUID generation for run/entity IDs    |
| `github.com/temoto/robotstxt`                                            | robots.txt parsing/matching           |
| `github.com/cespare/xxhash/v2`                                           | Hashed visited-set keys (ADR-0027)    |
| `golang.org/x/net/publicsuffix`                                          | eTLD+1 for Company identity + Scope   |
| `golang.org/x/sync/singleflight`                                         | Collapse concurrent robots.txt fetches |
| `github.com/joho/godotenv`                                               | Load `.env` into the environment      |
| `github.com/prometheus/client_golang`, `go.opentelemetry.io/otel/*`      | Metrics + observability               |
| `github.com/testcontainers/testcontainers-go` (+ postgres/redis modules) | Throwaway Postgres/Redis for tests    |

All other functionality (HTTP, logging, testing, concurrency) uses the Go
standard library.

## Commit Messages

Follow Conventional Commits as the user-level `git-commit` skill describes them:
`type: description` (lowercase, imperative mood). Types: `feat`, `fix`,
`refactor`, `test`, `docs`, `chore`, `perf`, `style`, `ci`. Scoped variants
allowed: `test(redis_frontier): add cooldown tests`.

GitHub issue titles are plain descriptions, NOT Conventional-Commit prefixed --
so a PR title (which mirrors the issue) is not conventional either. When squash
merging, pass the conventional subject explicitly so `main`'s history stays
conventional: `gh pr merge --squash --subject "feat(scope): ..."`.

Never add a `Co-Authored-By` trailer, or any other AI-attribution line, to a
commit.

Pushing over SSH fails from the agent shell, because the key is confirm-on-use.
Push over HTTPS with `gh`'s credential helper instead:

```bash
git -c credential.helper= -c credential.helper='!gh auth git-credential' push https://github.com/nicholasbraun/job-crawler-poc.git <branch>:<branch>
```
