# Job Crawler POC

A web crawler written in Go that discovers job listings across the web and filters them for relevance. Built as a portfolio project to demonstrate system design, concurrency patterns, and idiomatic Go.

## Motivation

I'm a backend engineer transitioning into Go-focused roles. Rather than building something abstract, I wanted a tool that solves a real problem for me: finding remote Go backend positions available in Germany. The crawler automates the tedious process of checking multiple job boards and company career pages.

## Architecture

The system follows a pipeline architecture with two decoupled responsibilities:

**Page Discovery** — a crawl loop that fetches pages, extracts URLs, and feeds them back into a frontier for further crawling.

**Relevance Evaluation** — a filter that runs against crawled content to identify job listings matching specific criteria (tech stack, location, remote availability).

```
Seed URLs → Frontier → Downloader → Parser → Content Filter ─┐
                ↑                                              │
                └── URL Filter ← URL Dedup ← Extract URLs ←───┘
                                                               │
                                              Relevance Filter ←┘
                                                     │
                                                  Job Store
```

### Key Design Decisions

- **URL Frontier with per-domain queuing**: prevents overwhelming any single server. Each domain has its own queue with configurable cooldown between requests.
- **Composable filter chains**: filters use a generic `CheckFn[T]` type. Individual checks are composed via a `Chain` function, making it trivial to add or remove filtering logic.
- **Retry with exponential backoff**: the HTTP client is wrapped in a retry decorator that handles transient failures (5xx, 429, timeouts) without leaking retry logic into the core pipeline.
- **Decoupled relevance evaluation**: the crawl loop stores all potentially useful pages. Relevance filtering decides what to surface, not what to crawl — preventing premature branch pruning.
- **Sequential orchestrator with clean termination**: the Frontier returns `ErrDone` when all queues are empty, giving the orchestrator a clear signal to shut down gracefully.

## Project Structure

```
job-crawler-poc/
├── cmd/
│   └── cli/
│       └── main.go              # CLI entry point, wires dependencies
├── internal/
│   ├── content.go               # Content domain type
│   ├── url.go                   # URL domain type + ParseURL
│   ├── job.go                   # Job domain type + JobRepository interface
│   ├── database/
│   │   └── sqlite/              # SQLite implementations of repositories
│   ├── filter/
│   │   └── filter.go            # Generic CheckFn[T] and Chain
│   ├── frontier/
│   │   ├── frontier.go          # Frontier interface
│   │   └── inmem/               # In-memory frontier (per-domain queues, cooldown)
│   ├── http/
│   │   ├── client.go            # HTTP client with context + timeout
│   │   ├── retry.go             # Retry decorator with exponential backoff
│   │   ├── downloader.go        # Downloader interface
│   │   └── response.go          # Response type
│   ├── orchestrator/
│   │   └── orchestrator.go      # Crawl loop, wires all components
│   └── parser/
│       └── parser.go            # HTML parser (goquery)
├── data/                        # SQLite database (gitignored)
├── go.mod
└── go.sum
```

Domain types and repository interfaces live at the `internal/` root. Infrastructure implementations (SQLite, HTTP, in-memory frontier) live in their own packages and depend inward toward the domain — never the reverse.

## Getting Started

### Prerequisites

- Go 1.25+

### Build & Run

```bash
# Clone the repository
git clone https://github.com/nicholasbraun/job-crawler-poc.git
cd job-crawler-poc

# Create the data directory for SQLite
mkdir -p data

# Run the crawler
go run ./cmd/cli -seedURLs="https://news.ycombinator.com/jobs"
```

### CLI Flags

| Flag        | Default                             | Description                       |
| ----------- | ----------------------------------- | --------------------------------- |
| `-seedURLs` | `https://news.ycombinator.com/jobs` | Comma-separated list of seed URLs |
| `-db`       | `./data/database.db`                | Path to the SQLite database file  |

### Run Tests

```bash
go test ./...
```

## Technical Highlights

### Generic Filter Chain

Filters use a generic function type that composes via a `Chain` helper. Adding a new filter is just writing a function and appending it to a slice:

```go
type CheckFn[T any] func(T) error

contentFilter := filter.Chain(
    filter.MinContentLength(100),
    filter.NotPrivacyPolicy,
)
```

### Concurrent Frontier

The in-memory frontier uses per-domain queues with deadline-based cooldowns. A signal channel allows `AddURL` to wake up a blocked `Next` call without polling. The mutex is only held during state reads/writes — never during blocking operations.

### Retry Decorator

The retry client wraps any `Downloader` implementation, adding exponential backoff and context-aware cancellation. The core HTTP client stays simple (single attempt), and retry logic is layered on via the decorator pattern:

```go
httpClient := http.NewClient()
downloader := http.NewRetryClient(httpClient,
    http.WithBackoff(2 * time.Second),
    http.WithMaxTries(5),
)
```

## Status

This is a proof-of-concept. The core crawl pipeline works end to end. Planned improvements:

- [ ] URL filter — restrict crawling to seed domains
- [ ] Content filter — skip non-job pages (privacy policies, login pages)
- [ ] Relevance filter — keyword matching for Go, remote, Germany
- [ ] Job extraction — parse structured job data from relevant pages
- [ ] Circuit breaker — feed download failures back to the Frontier to pause broken domains
- [ ] Dashboard — React UI for triggering crawls, monitoring progress, and reviewing matched jobs
- [ ] Worker pool — concurrent URL processing for higher throughput

## Dependencies

- [goquery](https://github.com/PuerkitoBio/goquery) — HTML parsing with CSS selectors
- [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) — Pure Go SQLite driver (no CGO)

## License

MIT
