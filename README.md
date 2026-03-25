# Job Crawler POC

A web crawler written in Go that discovers job listings across the web and filters them for relevance. Built as a portfolio project to demonstrate system design, concurrency patterns, and idiomatic Go.

## Motivation

I'm a backend engineer transitioning into Go-focused roles. Rather than building something abstract, I wanted a tool that solves a real problem for me: finding remote Go backend positions available in Germany. The crawler automates the tedious process of checking multiple job boards and company career pages.

## Architecture

The system follows a pipeline architecture with two decoupled responsibilities:

**Page Discovery** — a concurrent crawl loop that fetches pages, extracts URLs, and feeds them back into a frontier for further crawling.

**Relevance Evaluation** — a filter chain that runs against crawled content to identify job listings matching specific criteria (tech stack, location, remote availability).

```
Seed URLs → Frontier → Orchestrator → Worker Pool ──→ Downloader → Parser
                ↑                                                      │
                │                         Content Filter ←─────────────┘
                │                              │
                └── URL Filter ← URL Dedup ← Extract URLs
                                                │
                                        Relevance Filter
                                                │
                                        Processor Pool → Job Store
```

### Key Design Decisions

- **Concurrent worker pool**: configurable number of workers process URLs in parallel via a buffered channel. Worker count and channel buffer size are tunable.
- **URL Frontier with per-domain queuing**: prevents overwhelming any single server. Each domain has its own FIFO queue with configurable cooldown between requests.
- **In-flight tracking**: the frontier tracks URLs currently being processed by workers. It only signals crawl completion (`ErrDone`) when all queues are empty AND no workers are in-flight, preventing premature termination.
- **Atomic URL deduplication**: `INSERT OR IGNORE` + `RowsAffected()` in SQLite provides lock-free, atomic dedup. Workers skip URLs already seen without requiring a separate `Visited()` check.
- **Composable filter chains**: filters use a generic `CheckFn[T]` type. Individual checks are composed via `Chain` and `Every` combinators, making it trivial to add or remove filtering logic.
- **Retry with exponential backoff**: the HTTP client is wrapped in a retry decorator that handles transient failures without leaking retry logic into the core pipeline.
- **OpenTelemetry metrics**: frontier size, in-flight URLs, hostnames, and URLs processed are all instrumented and exported via Prometheus.

## Project Structure

```
job-crawler-poc/
├── cmd/
│   └── cli/
│       └── main.go                    # CLI entry point, wires dependencies
├── internal/
│   ├── content.go                     # Content domain type
│   ├── url.go                         # URL domain type + URLRepository interface
│   ├── job.go                         # Job domain type + JobRepository interface
│   ├── config/
│   │   ├── config.go                  # Config struct
│   │   └── json_loader/              # JSON config loader
│   ├── database/
│   │   └── sqlite/                    # SQLite repositories (URL dedup, job storage, WAL mode)
│   ├── filter/
│   │   ├── filter.go                  # Generic CheckFn[T], Chain, Every combinators
│   │   ├── content.go                 # Content filter helpers
│   │   ├── job/                       # Job-specific filters (title, main content keywords)
│   │   └── url/                       # URL filters (TLD, subdomain, path, hostname)
│   ├── frontier/
│   │   ├── frontier.go                # Frontier interface + sentinel errors
│   │   └── inmem/                     # In-memory frontier (per-domain queues, cooldown, in-flight tracking)
│   ├── downloader/
│   │   ├── client.go                  # HTTP client with timeout
│   │   ├── retry.go                   # Retry decorator with exponential backoff
│   │   ├── downloader.go              # Downloader interface
│   │   └── response.go               # Response type
│   ├── orchestrator/
│   │   └── orchestrator.go            # Crawl loop: seeds frontier, dispatches to worker pool
│   ├── otel/
│   │   └── otel.go                    # OpenTelemetry + Prometheus metrics + pprof endpoints
│   ├── parser/
│   │   └── parser.go                  # HTML parser (goquery)
│   ├── pool/
│   │   └── pool.go                    # Generic worker pool
│   └── processor/
│       ├── processor.go               # Processor interface
│       ├── url_processor/             # URL processor (download, parse, filter, discover URLs)
│       └── job_listing_processor/     # Job listing processor (relevance filtering, job storage)
├── config.json                        # Runtime config (seeds, filters, limits)
├── docker-compose.yml                 # Prometheus + Grafana
├── prometheus.yml                     # Prometheus scrape config
├── data/                              # SQLite database (gitignored)
├── go.mod
└── go.sum
```

Domain types and repository interfaces live at the `internal/` root. Infrastructure implementations (SQLite, HTTP, in-memory frontier) live in their own packages and depend inward toward the domain — never the reverse.

## Getting Started

### Prerequisites

- Go 1.26+

### Build & Run

```bash
# Clone the repository
git clone https://github.com/nicholasbraun/job-crawler-poc.git
cd job-crawler-poc

# Run the crawler
go run ./cmd/cli
```

Configuration is loaded from `config.json`. Seed URLs, filters, and crawl limits are all configurable there.

### CLI Flags

| Flag  | Default              | Description                      |
| ----- | -------------------- | -------------------------------- |
| `-db` | `./data/database.db` | Path to the SQLite database file |

### Observability

```bash
# Start Prometheus + Grafana
docker-compose up

# Metrics endpoint (served by the crawler)
curl localhost:2223/metrics

# pprof endpoints
curl localhost:2223/debug/pprof/goroutine?debug=2  # goroutine dump
curl localhost:2223/debug/pprof/heap?debug=1        # heap profile
```

- Prometheus: http://localhost:9090
- Grafana: http://localhost:3000

### Run Tests

```bash
go test ./...
```

## Technical Highlights

### Generic Filter Chain

Filters use a generic function type that composes via `Chain` and `Every` combinators:

```go
type CheckFn[T any] func(T) error

relevanceFilter := filter.Chain(
    filter.Every(
        jobfilter.TitleContains(
            filter.Contains("developer", "engineer"),
            filter.Contains("golang", "go", "backend"),
        ),
        jobfilter.MainContentContains(
            filter.Contains("remote"),
            filter.Contains("germany", "europe"),
        ),
    ),
    filter.Reject[*crawler.Content](),
)
```

### Concurrent Frontier

The in-memory frontier uses per-domain queues with deadline-based cooldowns. An in-flight counter tracks URLs being processed by workers — the frontier only returns `ErrDone` when all queues are empty and no workers are still processing. A signal channel allows `AddURL` and `MarkDone` to wake up a blocked `Next` call without polling.

### Worker Pool

Workers run as goroutines reading from a buffered channel. The orchestrator dispatches URLs from the frontier to the pool. Each worker downloads, parses, filters, discovers new URLs (with atomic dedup via SQLite), and feeds them back into the frontier:

```go
pool := urlworker.NewInMemURLWorkerPool(ctx, workerCfg,
    urlworker.WithMaxWorkers(8),
)
```

### Retry Decorator

The retry client wraps any `Downloader` implementation, adding exponential backoff and context-aware cancellation:

```go
httpClient := http.NewClient()
downloader := http.NewRetryClient(httpClient)
```

## Configuration

All runtime configuration lives in `config.json`:

| Field                 | Description                                          |
| --------------------- | ---------------------------------------------------- |
| `maxWorkers`          | Number of concurrent URL workers                     |
| `maxDepth`            | Maximum crawl depth from seed URLs                   |
| `maxDomains`          | Maximum number of unique domains in the frontier     |
| `seedURLs`            | Starting URLs for the crawl                          |
| `allowedTLDs`         | Only crawl URLs with these TLDs                      |
| `passSubdomains`      | Subdomains that bypass blocking (e.g. jobs, careers) |
| `passPathSegments`    | Path segments that bypass blocking                   |
| `blockedSubdomains`   | Subdomains to skip (e.g. blog, docs, shop)           |
| `blockedPathSegments` | Path segments to skip (e.g. login, pricing, api)     |
| `blockedHostnames`    | Specific hostnames to never crawl                    |
| `logLevel`            | Log level (DEBUG, INFO, WARN, ERROR)                 |

## Dependencies

- [goquery](https://github.com/PuerkitoBio/goquery) — HTML parsing with CSS selectors
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — Pure Go SQLite driver (no CGO)
- [OpenTelemetry](https://opentelemetry.io/) — Metrics instrumentation
- [Prometheus client](https://github.com/prometheus/client_golang) — Metrics export

## Production Readiness TODO

### Reliability & Resilience

- [x] Add body size limit — wrap `io.ReadAll(res.Body)` with `io.LimitReader` to prevent OOM from large responses
- [x] Check Content-Type header before reading body — skip non-HTML responses (PDFs, images, videos)
- [x] Retry loop retries non-retryable errors — `ErrNoHTML` and similar errors from `Client.Get` get retried `maxTries` times; `isRetryable`'s `err != nil` branch is dead code
- [ ] Fix retry client treating non-2xx as success — 403/451 responses get parsed as valid content; return error for non-retryable non-2xx
- [ ] RetryClient.Get swallows error on non-retryable path — `return res, nil` should be `return res, err`; currently discards errors like `ErrNoHTML`
- [ ] Retry loop swallows last error — on exhaustion returns generic message without wrapping the underlying error (DNS, TLS, status code info lost)
- [ ] Off-by-one wait in retry loop — after the last failed attempt, waits for backoff before returning error; adds unnecessary latency
- [ ] Classify errors — distinguish infrastructure errors (DB unreachable, disk full) from domain errors (404, parse failure); infrastructure errors should halt the crawl

### Correctness

- [ ] URL normalization — canonicalize URLs before dedup (lowercase scheme+host, strip trailing slashes, sort query params, remove fragments)

### Legal/Ethical

- [ ] Add robots.txt support — check and cache per-domain robots.txt before downloading pages
- [ ] Honest User-Agent — replace fake Firefox UA with `JobCrawler/1.0 (+https://yoursite.com/bot)`
- [ ] Honor Crawl-delay — adapt per-domain politeness from robots.txt instead of hardcoded 1s cooldown

### Observability

- [ ] Record metric on Content-Type rejection — currently no metric is emitted when Content-Type check fails; add a "rejected" or "non_html" status
- [ ] Fix 404 test asserting success — test asserts 404 returns no error; update when non-2xx handling is fixed

### Operability

- [ ] Graceful shutdown — two-phase: stop accepting new work, drain in-flight with deadline
- [ ] Handle metrics server errors — check and log error from `ListenAndServe` if port is taken
- [ ] Structured error context in workers — log URL, domain, HTTP status, and failure stage (download/parse/filter)
- [ ] Health check endpoint — expose a `/healthz` for process managers to detect deadlocks/stalls

### Scalability

- [ ] Batch SQLite writes — collect discovered URLs per page, insert in single transaction
- [ ] Cap per-domain queue size — prevent unbounded growth from large sites

## License

MIT
