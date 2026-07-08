# Job Crawler

A long-running Go service that discovers company career pages across the web and
extracts job listings from them by keyword. It serves a REST API and an embedded
React dashboard from a single binary, and runs crawls as managed background jobs
with all state in Postgres and Redis.

Built as a portfolio project to demonstrate system design, concurrency patterns,
and idiomatic Go. AI is used strictly for code review and documentation — every
line of code is written by hand.

## Domain

The crawler works in two phases (see `CONTEXT.md` for the full glossary):

- **Discovery Crawl** — a perpetual, bounded-broad crawl that finds **Career
  Pages** and attributes them to **Companies**, filling a durable **Catalog**.
- **Keyword Crawl** — a bounded crawl seeded from the Catalog's Career Pages that
  extracts **Job Listings** matching a set of OR-matched keywords.

A **Crawl Definition** is the re-runnable configuration for a crawl; each
execution is a **Crawl Run** with a live status and counters.

## Architecture

A stateless Go monolith serves a REST API + the embedded dashboard on `:8080` and
runs crawl goroutines. No authoritative state lives in Go memory:

- **Postgres** holds durable state: the Catalog (companies, career pages), crawl
  definitions, runs, and extracted job listings.
- **Redis** holds transient per-run state: the URL **Frontier** (per-domain FIFO
  queues with cooldown), the visited set, and in-flight leases — all keyed per run
  so a restarted process resumes an in-progress run.

Because state is external, a stopped or crashed process loses nothing: on startup
the server adopts and resumes any run a previous process left running. Stopping a
run is a desired-state flip (`crawl_run.status = 'stopping'`) that the crawl loop
polls — SIGINT drains active runs before the process exits.

```
Seed URLs → Frontier (Redis) → Orchestrator → Worker Pool → Robots.txt → Downloader → Parser
                 ↑                                                                       │
                 │                                          URL Filter ←─ Extract URLs ←─┘
                 │                                               │
                 └───────────────────────────────────────────────
                                                                 │
                              Discovery: Career-Page gate → Catalog (Postgres)
                              Keyword:   Relevance filter → LLM Extractor → Job Listings (Postgres)
```

## Getting Started

### Prerequisites

- Go 1.26+
- Node 24+ (`.nvmrc`) to build the dashboard
- Docker (for Postgres + Redis, or the full stack via Compose)
- An LLM endpoint: an [OpenRouter](https://openrouter.ai/) API key, or any
  OpenAI-compatible server (e.g. a local [Ollama](https://ollama.com/))

### Run the whole stack with Docker

```bash
# Provide your LLM credentials (used for career-page classification and
# job-listing extraction). LLM_BASE_URL/LLM_MODEL are optional and default to
# OpenRouter; set them to use any OpenAI-compatible server instead.
echo 'LLM_API_KEY=your-key-here' > .env

# Build the image (dashboard + server) and start crawler + Postgres + Redis +
# the observability stack.
docker compose up --build   # or: make docker-up
```

Then open the dashboard at http://localhost:8080.

### Run locally

```bash
# Start just Postgres + Redis (plus observability) from Compose, or point
# DATABASE_URL / REDIS_ADDR at your own instances.
docker compose up postgres redis

# Build the dashboard and server into a single binary (vite build → go build),
# then run it. Migrations are applied automatically on startup.
make build
LLM_API_KEY=your-key-here ./bin/crawler

# …or run fully local against Ollama (no API key needed). LLM_TIMEOUT and
# LLM_MAX_WORKERS already default to local-friendly values (5m / 2), so this is
# all you need:
LLM_API_KEY=ollama \
  LLM_BASE_URL=http://localhost:11434/v1/chat/completions \
  LLM_MODEL=qwen3.5:9b ./bin/crawler
```

For frontend development, `make dev` runs the Vite dev server (proxying `/api` to
a locally running `go run ./cmd/server`) with hot reload.

### Kick off a crawl

Create a Discovery definition and start a run (or do the same from the dashboard):

```bash
curl -X POST localhost:8080/api/definitions -H 'Content-Type: application/json' -d '{
  "name": "startup discovery",
  "kind": "discovery",
  "seedUrls": ["https://news.ycombinator.com/jobs"]
}'
# → returns the definition; omitted urlFilter/maxDepth are filled with the
#   built-in defaults (see internal/crawl_definition.go DefaultURLFilterConfig).

curl -X POST localhost:8080/api/definitions/{id}/runs   # start a run
```

## Configuration

All configuration is via environment variables (loaded from `.env` in local dev
via [godotenv](https://github.com/joho/godotenv)):

| Variable             | Default                                     | Description                          |
| -------------------- | ------------------------------------------- | ------------------------------------ |
| `LLM_API_KEY`        | —                                           | Bearer token for the LLM API (any value for Ollama) |
| `LLM_BASE_URL`       | `https://openrouter.ai/api/v1/chat/completions` | OpenAI-compatible chat completions endpoint     |
| `LLM_MODEL`          | `openai/gpt-5.4-nano`                       | Model name to request               |
| `LLM_TIMEOUT`        | `5m`                                        | Per-request timeout (Go duration); covers time queued on the server |
| `LLM_MAX_WORKERS`    | `2`                                         | Concurrent LLM calls; keep low for a serial local model, raise for a parallel cloud API |
| `DATABASE_URL`       | `postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable` | Postgres DSN |
| `REDIS_ADDR`         | `localhost:6379`                            | Redis `host:port`                    |
| `LOG_LEVEL`          | `INFO`                                      | slog level (DEBUG/INFO/WARN/ERROR)   |

Crawl tuning defaults (max depth, worker count, and the URL-filter lists that
steer crawls toward career pages) live in Go — `defaultMax*` constants in
`cmd/server/main.go` and `crawler.DefaultURLFilterConfig()` in
`internal/crawl_definition.go`. Depth and the URL filters are overridable per
Crawl Definition via the API/dashboard.

## Observability

The Compose stack includes Prometheus, Grafana, and Loki. The server exposes
metrics and pprof on `:2223`:

```bash
curl localhost:2223/metrics                          # Prometheus metrics
curl localhost:2223/debug/pprof/goroutine?debug=2    # goroutine dump
```

- Prometheus: http://localhost:9090
- Grafana: http://localhost:3000

## Tests

```bash
make test          # go test ./...
make test-race     # go test -race ./...
```

Postgres and Redis repository tests spin up real instances via
[testcontainers](https://testcontainers.com/); Docker must be running.

## Project Structure

```
cmd/server/main.go             # Entry point: wires deps, serves API + dashboard, manages runs
internal/
  *.go                         # Domain types + repository interfaces (crawler package)
  api/                         # REST API handlers over the repositories + runner
  catalog/                     # ATS-aware Company identity
  database/postgres/           # Postgres repositories + goose migrations
  downloader/                  # HTTP client + retry decorator
  filter/                      # Generic filter chain; url/ and job_listing_filter/ rules
  frontier/redis/              # Redis-backed, crash-safe, resumable URL frontier
  openrouter/                  # LLM career-page classifier + job-listing extractor
  orchestrator/                # Crawl loop: frontier → worker pool
  pool/                        # Generic worker pool
  processor/                   # discovery_/career_page_/url_/job_listing_ processors
  robotstxt/                   # robots.txt checker (per-host cache + singleflight)
  runner/                      # Multi-run lifecycle: start, stop, resume, drain
web/                           # React + Vite + Tailwind dashboard (embedded via //go:embed)
```

Domain types and repository interfaces live at the `internal/` root; infrastructure
implementations depend inward toward the domain, never the reverse.

## Technical Highlights

### Crash-safe, resumable frontier

The Redis frontier keeps per-domain FIFO queues with deadline-based cooldowns, a
visited set for dedup, and in-flight leases. `Next` and `AddURL` are each a single
Lua script, so concurrent workers can never double-pop a URL or race the dedup. A
worker that crashes mid-URL has its lease reclaimed once the TTL elapses, so no URL
is lost or duplicated across restarts.

### Composable filter chains

Filters use a generic `CheckFn[T]` composed via `Chain` and `Every`. URL filtering
short-circuits: hiring-related subdomains/paths (`careers`, `jobs`, …) pass, while
blogs, docs, shops, auth, and social hosts are blocked — steering crawls toward
career pages before any expensive work.

### LLM-based structured extraction

Rather than brittle per-site scrapers, the crawler delegates career-page
classification and job-listing extraction to an LLM via OpenRouter, behind small
`Confirmer` / `JobListingExtractor` interfaces. Extraction instructions are sent as
a `system` message and response fields are HTML-stripped before storage to reduce
prompt-injection surface.

### Robots.txt compliance

Before fetching or enqueueing a URL, the crawler checks the host's `robots.txt`.
Rules are cached per hostname and concurrent first-time fetches are collapsed with
`singleflight`. Status handling follows RFC 9309 §2.3.1.3 (404/410 → allow-all,
5xx → disallow-all).

### Retry with backoff

The HTTP client is wrapped in a retry decorator adding exponential backoff and
context-aware cancellation, keeping retry logic out of the core pipeline.

## Dependencies

- [pgx](https://github.com/jackc/pgx) — PostgreSQL driver + pool
- [goose](https://github.com/pressly/goose) — SQL migrations
- [go-redis](https://github.com/redis/go-redis) — Redis client
- [goquery](https://github.com/PuerkitoBio/goquery) — HTML parsing
- [temoto/robotstxt](https://github.com/temoto/robotstxt) — robots.txt parsing
- [godotenv](https://github.com/joho/godotenv) — `.env` loading
- [OpenTelemetry](https://opentelemetry.io/) + [Prometheus client](https://github.com/prometheus/client_golang) — metrics
- [React](https://react.dev/) + [Vite](https://vitejs.dev/) + [Tailwind](https://tailwindcss.com/) — dashboard

## License

MIT
