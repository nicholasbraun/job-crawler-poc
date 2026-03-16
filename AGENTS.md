# AGENTS.md

This file provides guidance for AI coding agents working in this repository.

## Project Overview

Go CLI application implementing a web crawler for job listings. Uses clean
architecture (ports-and-adapters): domain types and interfaces in `internal/`,
infrastructure implementations in sub-packages. No frameworks -- relies on the
Go standard library for HTTP, logging, concurrency, and CLI flags.

## Build / Run / Test Commands

```bash
# Build
go build ./cmd/cli

# Run
go run ./cmd/cli

# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run a single test by name (regex match against Test function names)
go test -v -run TestParseURL ./internal/

# Run a single subtest
go test -v -run TestParseURL/valid_url ./internal/

# Run all tests in one package
go test -v ./internal/database/sqlite/
go test -v ./internal/frontier/inmem/
go test -v ./internal/http/
go test -v ./internal/parser/
go test -v ./internal/filter/

# Run tests with race detector
go test -race ./...

# Format code
gofmt -w .
goimports -w .
```

There is no Makefile, Dockerfile, CI/CD pipeline, or linter configuration.

## Project Structure

```
cmd/cli/main.go              # Entry point, wires all dependencies
internal/
  doc.go                     # Package "crawler" -- domain root
  url.go, job.go, content.go # Domain types + repository interfaces
  database/sqlite/           # SQLite repository implementations
  filter/                    # Generic filter chain (CheckFn[T], Chain)
  frontier/                  # Frontier interface + sentinel errors
  frontier/inmem/            # In-memory frontier implementation
  http/                      # Downloader interface, HTTP client, retry decorator
  orchestrator/              # Crawl loop wiring all components
  parser/                    # HTML parser (goquery)
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
- `URL` is a value type; `Content` and `Job` are used as pointer types.
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
- **Database tests:** Use in-memory SQLite (`:memory:`).
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
| `modernc.org/sqlite` | Pure-Go SQLite driver (no CGO) |

All other functionality (HTTP, logging, testing, CLI flags, concurrency) uses
the Go standard library.

## Commit Messages

Follow Conventional Commits: `type: description` (lowercase, imperative mood).
Types: `feat`, `fix`, `refactor`, `test`, `docs`, `chore`.
Scoped variants allowed: `test(inmem_frontier): add cooldown tests`.
