# AGENTS.md

This file provides guidance for AI coding agents working in this repository.

## Project Overview

Go server application implementing a web crawler for job listings, exposing a
REST API with an embedded React dashboard. Uses clean architecture
(ports-and-adapters): domain types and interfaces in `internal/`, infrastructure
implementations in sub-packages. Persists to PostgreSQL and holds transient
per-run crawl state (frontier queues, visited sets) in Redis. No web framework
-- relies on the Go standard library for HTTP, logging, and concurrency.

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
go test -v ./internal/frontier/inmem/
go test -v ./internal/downloader/
go test -v ./internal/parser/
go test -v ./internal/filter/

# Run tests with race detector
go test -race ./...     # or: make test-race

# Format code
gofmt -w .
goimports -w .
```

The server reads configuration from the environment (a `.env` file is loaded via
`godotenv` if present): `DATABASE_URL` (Postgres DSN), `REDIS_ADDR` (defaults to
`localhost:6379`), and `LLM_API_KEY` for the LLM classifier/extractor. The
classifier/extractor speak the OpenAI-compatible chat-completions API; override
`LLM_BASE_URL` and `LLM_MODEL` (defaulting to OpenRouter) to target any
compatible server, e.g. a local Ollama.

The repo has a `Makefile`, `Dockerfile`, and `docker-compose.yml`. There is no
CI/CD pipeline or linter configuration.

## Development Workflow

Two lanes, chosen by the size of the change. Each lane is a fixed sequence of
skills; the session breaks in the feature/fix lane are deliberate human gates.

### Feature / fix lane (non-trivial work — spans several sessions)

Session 1 — align & specify:
1. `/grilling` — reach a shared understanding of what to build before any code.
2. `/domain-modeling` — record any new decisions and terms the grilling surfaced:
   ADRs to `docs/adr/NNNN-slug.md`, domain language to `CONTEXT.md`.
3. `/to-spec` — publish the spec / tracking GitHub issue.
4. `/to-tickets` — break it into vertical sub-issues with their blocking edges.

Session 2 — deliver (fresh session; hand it the spec issue number):

5. `/deliver <spec#>` — create the `feat/…` or `fix/…` branch, orchestrate the
   sub-issues (parallel by dependency: a sub-issue runs once its blockers are
   done), and open the PR that closes the spec and its sub-issues. Stops at the
   PR; it does not self-review or merge.

Session 3 — review (fresh session):

6. `/code-review` the PR — fix small, contained issues in place; open follow-up
   issues for anything larger; then merge.

### One-off lane (small, contained changes — no ceremony)

Plan → `/handoff` → implement, committing directly to `main`. No spec, no branch,
no PR.

## Project Structure

```
cmd/server/main.go           # Entry point: wires deps, serves REST API + dashboard
web/                         # React/Vite dashboard; web/dist is embedded into the binary
internal/
  doc.go                     # Package "crawler" -- domain root
  url.go, job_listing.go, content.go, company.go, career_page.go,
    crawl_definition.go, crawl_run.go  # Domain types + repository interfaces
  api/                       # REST API handlers over the repositories
  runner/                    # Adopts/resumes runs, drives orchestrators per run
  catalog/                   # Company/career-page catalog identity helpers
  database/postgres/         # Postgres repositories + goose migrations
  downloader/                # Downloader interface, HTTP client, retry decorator
  filter/                    # Generic filter chain (CheckFn[T], Chain)
  filter/job_listing_filter/ # Job listing filters (title, main content keywords)
  filter/url/                # URL filters (TLD, subdomain, path, hostname)
  frontier/                  # Frontier interface + sentinel errors
  frontier/inmem/            # In-memory frontier implementation
  frontier/redis/            # Redis-backed frontier (per-run queues, visited sets)
  openrouter/                # LLM career-page classifier + job-listing extractor
  orchestrator/              # Crawl loop wiring all components
  otel/                      # OpenTelemetry + Prometheus metrics + pprof
  parser/                    # HTML parser (goquery)
  pool/                      # Generic worker pool
  processor/                 # Processor interface
  processor/url_processor/           # URL processor (download, parse, filter, discover)
  processor/discovery_processor/     # Discovery crawl processor (fills the catalog)
  processor/career_page_processor/   # Career-page classification processor
  processor/job_listing_processor/   # Job listing processor
  robotstxt/                 # robots.txt fetching, caching, and matching
```

## Code Style

### Formatting and Imports

- Standard `gofmt` formatting. No custom linter rules.
- Import groups: (1) stdlib, (2) third-party / internal packages, separated by
  a blank line.
- The root `internal/` package is always aliased on import:
  `crawler "github.com/nicholasbraun/job-crawler-poc/internal"`
- Avoid import collisions with aliases (e.g., `myHttp` for the internal HTTP
  package when `net/http` is also imported).

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

- `sync.Mutex` for shared state protection.
- Signal channels (`chan struct{}`) for goroutine wakeup.
- Mutex is intentionally released before blocking operations (not deferred)
  in the frontier implementation.
- Use `select` with `ctx.Done()` for context-aware blocking.

### Package Documentation

- Every package has a doc comment, either in `doc.go` or at the top of the
  main file: `// Package <name> ...`.

## Testing Conventions

- **Framework:** Standard library `testing` only. No testify, no gomock.
- **Test packages:** Use external test packages (e.g., `package crawler_test`,
  `package inmem_test`) to test the public API.
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

| Package | Purpose |
|---|---|
| `github.com/PuerkitoBio/goquery` | HTML parsing with CSS selectors |
| `github.com/jackc/pgx/v5` | PostgreSQL driver + connection pool |
| `github.com/pressly/goose/v3` | SQL schema migrations |
| `github.com/redis/go-redis/v9` | Redis client (per-run frontier state) |
| `github.com/google/uuid` | UUID generation for run/entity IDs |
| `github.com/temoto/robotstxt` | robots.txt parsing/matching |
| `github.com/joho/godotenv` | Load `.env` into the environment |
| `github.com/prometheus/client_golang`, `go.opentelemetry.io/otel/*` | Metrics + observability |
| `github.com/testcontainers/testcontainers-go` (+ postgres/redis modules) | Throwaway Postgres/Redis for tests |

All other functionality (HTTP, logging, testing, concurrency) uses the Go
standard library.

## Commit Messages

Follow Conventional Commits: `type: description` (lowercase, imperative mood).
Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`.
Scoped variants allowed: `test(inmem_frontier): add cooldown tests`.
