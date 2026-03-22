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
│   ├── http/
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
│   ├── processor/
│   │   ├── processor.go               # Processor interface
│   │   └── inmem/                     # In-memory processor with worker pool
│   └── worker/
│       ├── worker.go                  # Worker interface
│       └── inmem/
│           └── url_worker/            # URL worker + pool (download, parse, filter, discover URLs)
├── config.json                        # Runtime config (seeds, filters, limits)
├── docker-compose.yml                 # Prometheus + Grafana
├── prometheus.yml                     # Prometheus scrape config
├── todo.md                            # Production readiness TODO
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

# Create the data directory for SQLite
mkdir -p data

# Run the crawler
go run ./cmd/cli
```

Configuration is loaded from `config.json`. Seed URLs, filters, and crawl limits are all configurable there.

### CLI Flags

| Flag | Default              | Description                      |
| ---- | -------------------- | -------------------------------- |
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

| Field                | Description                                         |
| -------------------- | --------------------------------------------------- |
| `maxWorkers`         | Number of concurrent URL workers                    |
| `maxDepth`           | Maximum crawl depth from seed URLs                  |
| `maxDomains`         | Maximum number of unique domains in the frontier     |
| `seedURLs`           | Starting URLs for the crawl                         |
| `allowedTLDs`        | Only crawl URLs with these TLDs                     |
| `passSubdomains`     | Subdomains that bypass blocking (e.g. jobs, careers) |
| `passPathSegments`   | Path segments that bypass blocking                  |
| `blockedSubdomains`  | Subdomains to skip (e.g. blog, docs, shop)          |
| `blockedPathSegments`| Path segments to skip (e.g. login, pricing, api)    |
| `blockedHostnames`   | Specific hostnames to never crawl                   |
| `logLevel`           | Log level (DEBUG, INFO, WARN, ERROR)                |

## Dependencies

- [goquery](https://github.com/PuerkitoBio/goquery) — HTML parsing with CSS selectors
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — Pure Go SQLite driver (no CGO)
- [OpenTelemetry](https://opentelemetry.io/) — Metrics instrumentation
- [Prometheus client](https://github.com/prometheus/client_golang) — Metrics export

## License

MIT
