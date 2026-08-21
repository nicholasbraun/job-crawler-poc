# Job Crawler

A long-running Go service that discovers company career pages across the web,
continuously collects their job listings into a queryable corpus, and answers
"which jobs match?" as a live query over that corpus. It serves a REST API and an
embedded React dashboard from a single binary, and runs crawls as managed
background jobs with all state in Postgres and Redis.

Built as a portfolio project to demonstrate system design, concurrency patterns,
and idiomatic Go.

## Domain

The system has three parts (see `CONTEXT.md` for the full glossary and
`docs/adr/` for the decision record):

- **Discovery Crawl** — a single, perpetual, bounded-broad crawl that finds
  **Career Pages** and attributes them to **Companies**, filling a durable
  **Catalog**.
- **Collection Crawl** — a perpetual crawl seeded from the whole Catalog. Each
  **Collection Cycle** (daily by default) harvests every open **Job Listing**
  from every Career Page into the **Corpus** and re-checks the ones already
  there. Each seed stays confined to its Company by **Scope**.
- **SavedSearches** — named, stored queries over the Corpus (keywords,
  countries, work arrangement), rendered as live dashboard panels.

Keyword and country are query-time filters, not crawl-time pruning: the
Collection Crawl collects everything it can reach and a SavedSearch decides what
matters (ADR-0038 retired the former keyword-crawl lane).

A **Crawl Definition** is the re-runnable configuration for a crawl; each
execution is a **Crawl Run** with a live status and counters. Discovery and
Collection are each a singleton definition with at most one active run.

## Architecture

A Go monolith serves a REST API + the embedded dashboard on `:8080` and runs
crawl goroutines. No authoritative state lives in Go memory:

- **Postgres** holds durable state: the Catalog (companies, career pages), crawl
  definitions and runs, saved searches, import jobs, and the Corpus of collected
  job listings.
- **Redis** holds transient per-run state: the URL **Frontier** (per-domain
  queues with cooldown, plus a bounded visited set and in-flight leases) and the
  durable LLM work streams — all keyed per run, so a restarted process resumes an
  in-progress run.

Because state is external, a stopped or crashed process loses nothing: on startup
the server adopts and resumes any run a previous process left running, and fails
any import job left mid-flight. Stopping a run is a desired-state flip
(`crawl_run.status = 'stopping'`) that the crawl loop polls; pausing parks a run
across restarts until a human resumes it. SIGINT drains active runs before the
process exits.

```
Discovery Crawl — perpetual, roams
  Seeds → Frontier (Redis) → Worker Pool → robots.txt → Download → Parse
             ↑                                                       │
             └───── URL Filter ←── Extract URLs ←────────────────────┤
                                                                     ↓
                                                              Career-Page Gate
                                                    ┌────────────────┴────────────────┐
                                              certain accept                     uncertain
                                                    │                                 ↓
                                                    │                 LLM classify (Redis Stream)
                                                    └────────────────┬────────────────┘
                                                                     ↓
                                                     Catalog: Companies + Career Pages
                                                                (Postgres)

Collection Cycle — scheduled, whole-Catalog, Scope-fenced per seed
  Catalog seeds ─┬─→ ATS Fetch lane → board API ──────────────────────────────────┐
                 │      (recognized ATS tenant, no LLM)                           │
                 │                                                                │
                 ├─→ Crawl walk → Frontier → Download → Parse → Extract Gate      ├─→ Corpus
                 │                                                  │             │  (Postgres)
                 │                                     ┌────────────┴──────┐      │
                 │                              Free Extraction     LLM extract ──┤
                 │                             (structured data)      (Stream)    │
                 │                                     └───────────────────────────┤
                 │                                                                │
                 └─→ Refetch lane → Liveness: reopen / close / re-extract ────────┘
                       + ATS absence sweep + Career-Page dormancy
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
  LLM_MODEL=qwen2.5:3b ./bin/crawler
```

Use a **non-reasoning instruct** model locally. `qwen2.5:3b` is the runnable
default (~3.2 GB, fits a 16 GB laptop alongside the rest of the stack); step up to
`qwen2.5:7b` for a more precise career-page gate if you have the RAM. Avoid
reasoning models (e.g. `qwen3.5`): they run a hidden think phase the crawler
discards, a large latency tax for a one-line classification verdict.

For frontend development, `make dev` runs the Vite dev server (proxying `/api` to
a locally running `go run ./cmd/server`) with hot reload.

### Using it

The dashboard has four screens — Overview, Discovery, Catalog, Searches — and
everything below is reachable from there. The equivalent API calls:

```bash
# 1. Start Discovery. It is a singleton: create it once, then start its run.
#    GET /api/definitions/defaults returns the built-in seed list, max depth,
#    and URL-filter config the dashboard's start modal prefills from.
curl -s localhost:8080/api/definitions/defaults

curl -X POST localhost:8080/api/definitions -H 'Content-Type: application/json' -d '{
  "name": "discovery",
  "kind": "discovery",
  "seedUrls": ["https://www.eu-startups.com/directory/"]
}'
# → 201 with the definition (omitted urlFilter/maxDepth get the defaults above);
#   409 if a Discovery definition already exists.

curl -X POST localhost:8080/api/definitions/{id}/runs   # start the perpetual run
curl -X POST localhost:8080/api/definitions/{id}/seeds \
  -H 'Content-Type: application/json' -d '{"url":"https://example.com/portfolio"}'
# → seeds can be appended while the run is live; they land in its Frontier

# 2. Collection needs no setup. Its singleton definition ships with the schema
#    at a fixed id, and a scheduler starts a Cycle immediately on first boot and
#    then every COLLECTION_INTERVAL (default 24h). To force one now:
curl -X POST localhost:8080/api/definitions/00000000-0000-0000-0000-00000c011ec7/runs

# 3. Query the Corpus with a SavedSearch.
curl -X POST localhost:8080/api/saved-searches -H 'Content-Type: application/json' -d '{
  "name": "backend in Germany",
  "keywords": ["golang", "backend"],
  "countries": ["DE"],
  "workArrangements": ["remote", "hybrid"]
}'
curl -s localhost:8080/api/saved-searches/{id}/results
```

Runs are controlled with `POST /api/crawls/{id}/{stop,pause,resume}`; the Catalog
can be exported and re-imported via `GET /api/catalog/export` and
`POST /api/catalog/import`.

## Configuration

All configuration is via environment variables (loaded from `.env` in local dev
via [godotenv](https://github.com/joho/godotenv)). Every variable is optional and
falls back to the default below; a variable set but *empty* counts as unset. A
malformed value stops the server before it starts serving, and startup reports
**every** bad variable at once rather than one per restart (ADR-0045):

```
error reading configuration:
CRAWL_MAX_WORKERS: must be a positive integer, got "-3" (default 50)
COLLECTION_INTERVAL: must be a positive Go duration (e.g. 90s, 5m, 24h), got "daily" (default 24h0m0s)
```

| Variable             | Default                                     | Description                          |
| -------------------- | ------------------------------------------- | ------------------------------------ |
| `LLM_API_KEY`        | —                                           | Bearer token for the LLM API (any value for Ollama) |
| `LLM_BASE_URL`       | `https://openrouter.ai/api/v1/chat/completions` | OpenAI-compatible chat completions endpoint     |
| `LLM_MODEL`          | `openai/gpt-5.4-nano`                       | Model name to request (locally, a non-reasoning instruct model, e.g. `qwen2.5:3b`) |
| `LLM_TIMEOUT`        | `5m`                                        | Per-request timeout (Go duration); covers time queued on the server |
| `LLM_MAX_WORKERS`    | `2`                                         | Consumers draining each per-run LLM stream; keep low for a serial local model, raise for a parallel cloud API |
| `LLM_CLASSIFY_MAX_CHARS` | `1500`                                  | Cap (runes) on page text sent to the career-page classifier; the signal is near the top of the page. Applied to the Structural Rendering when `PARSE_STRUCTURAL_RENDERING` is on (~1.1x the Flattened Text, so the same cap shows ~10% less page) |
| `LLM_EXTRACT_MAX_CHARS`  | `8000`                                  | Cap (runes) on page text sent to the job-listing extractor; likewise applied to the Structural Rendering when `PARSE_STRUCTURAL_RENDERING` is on |
| `DESCRIPTION_MAX_CHARS`  | `16000`                                 | Cap (runes) on the stored Posting Body; its own knob, independent of the extractor's prompt window |
| `EXTRACT_FROM_JSONLD`    | `true`                                  | Free Extraction kill switch (ADR-0042): read a lone structured-data posting with no model call |
| `EXTRACT_REQUIRE_POSITIVE_EVIDENCE` | `true`                       | Extract Gate must see positive evidence a page *is* one posting, not just that nothing rejected it (ADR-0044) |
| `SHADOW_EXTRACT_RATE`    | `0.01`                                  | Fraction of Extract-Gate-rejected pages extracted anyway to measure false-drops; `0` disables |
| `PARSE_STRUCTURAL_RENDERING` | `false`                             | Parser keeps page structure — headings, list items, table rows, link targets, form controls (ADR-0046); non-model consumers still read the Flattened Text derived from it, while the classifier and extractor prompts read the rendering with link targets omitted. `false` restores today's flattened output and skips the second DOM walk. Measured by `llmbench score-rendering`: the Extract Gate decides identically under both renderings (0 flips over 116 committed pages), and at the 8000-rune budget the rendering costs 1.80% of the page words the extractor sees. **The right setting depends on the model you serve** (#289): scored online over re-fetched real postings, it left a Qwen3.5-9B's recall unchanged at 92%, but on the 83 pages that actually reach the model (i.e. excluding those Free Extraction handles with no model call) it nearly doubled a Qwen2.5-3B-Instruct's recall, 28.0%→50.6%, for ~+68% wall time — turn it on for a small local model, leave it off for a large one, and re-decide it with `extractbench` whenever `LLM_MODEL` changes |
| `CRAWL_MAX_WORKERS`  | `50`                                        | Per-run crawl worker pool size (I/O-bound; raise for throughput) |
| `CRAWL_VISITED_CAP`  | `5000000`                                   | Per-run ceiling on the visited set before FIFO eviction (ADR-0027) |
| `ROBOTS_CACHE_SIZE`  | `16384`                                     | Hosts held in the shared robots.txt rules cache (ADR-0032) |
| `ROBOTS_CACHE_TTL`   | `1h`                                        | How long a cached robots.txt is trusted before a re-fetch |
| `COLLECTION_ENABLED` | `true`                                      | Whether the scheduler starts Collection Cycles (manual API starts still work) |
| `COLLECTION_INTERVAL`| `24h`                                       | Minimum time between Collection Cycle starts (ADR-0036) |
| `DATABASE_URL`       | `postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable` | Postgres DSN |
| `REDIS_ADDR`         | `localhost:6379`                            | Redis `host:port`                    |
| `LOG_LEVEL`          | `INFO`                                      | slog level (DEBUG/INFO/WARN/ERROR)   |
| `EXTRACT_CAPTURE_PATH` | —                                         | When set, taps every extract decision to a JSONL file for gold-set harvesting (ADR-0043). Each record names the renderer that produced its content, so a harvest under `PARSE_STRUCTURAL_RENDERING` is distinguishable from one without it (ADR-0046) |

Crawl tuning defaults (max depth, the baseline Discovery seed list, and the
URL-filter lists that steer crawls toward career pages) live in Go —
`defaultMax*` constants in `cmd/server/main.go` and
`crawler.DefaultURLFilterConfig()` / `crawler.DefaultDiscoverySeeds()` in
`internal/crawl_definition.go`. Depth, seeds, and the URL filters are overridable
per Crawl Definition via the API/dashboard.

## Observability

The Compose stack includes Prometheus, Grafana, and Loki, with provisioned
dashboards in `grafana/dashboards/` (frontier, downloader, collection, LLM
telemetry, system). The server exposes metrics and pprof on `:2223`:

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
make lint          # golangci-lint run ./...
```

Postgres and Redis repository tests spin up real instances via
[testcontainers](https://testcontainers.com/); Docker must be running.

Beyond the unit suite, `cmd/llmbench` scores the classification gates offline
against curated fixtures — the **Gold Set** for the career-page Gate and the
**Extract Gold Set** (real pages the live extract stage decided on) for the
Extract Gate — so a gate change is measured before it ships, with no network and
no model in the loop.

Its `score-rendering` verb is the same discipline applied to the parser: it
replays the extract path over the committed pages twice, once with the parser
flattening and once with it rendering structure, at one shared prompt budget, and
prints each arm's extract-call rate and false-drop count beside the delta between
them. It exits non-zero when the two arms disagree, never on their level.

Its `goldset-ui` verb is the confirmation surface for that set: a page on loopback
that serves the rows still owed a human confirmer one at a time, takes the label as
a single keystroke **before** revealing what the proposer said, and writes the batch
through `goldset-apply` and nothing else. The by-product is the number the set needs
— how often an independent human and the proposer reach the same answer (ADR-0048).
Each row's **Capture Fidelity** is measured against a fresh fetch of its URL and shown
beside it, so a live view is admitted or refused per row rather than assumed (ADR-0047).
Where fidelity admits it, the page itself is rendered beside the captured text — the same
Structural Rendering the pipeline produces, with link targets kept, served by the tool from
loopback in a sandboxed, script-free frame rather than framed from the site (ADR-0047). A
*drifted* page is shown behind a warning and a *gone* one is not shown at all; the captured
content is the authority in every case, and the screen says so.

## Project Structure

```
cmd/server/main.go             # Entry point: wires deps, serves API + dashboard, manages runs
cmd/doctor/                    # Catalog Doctor CLI: replay today's rules over the stored Catalog
cmd/llmbench/                  # Offline gate benchmarks + gold-set tooling
internal/
  *.go                         # Domain types + repository interfaces (crawler package)
  api/                         # REST API handlers over the repositories + runner
  ats/                         # Board-API clients for 10 ATS providers + the registry
  atsingest/                   # ATS Fetch lane: pool, per-tenant dedup, per-provider limiter
  catalog/                     # ATS-aware Company identity, name ladder, company snapshot
  catalogdoctor/               # Catalog repair engine (plan + apply)
  collection/                  # Collection Cycle: seed routing, refetch/liveness, scheduler, politeness
  database/postgres/           # Postgres repositories, Corpus + FTS search, goose migrations
  downloader/                  # HTTP client, caching transport, retry decorator
  extractcapture/              # Extract-decision tap that feeds the Extract Gold Set
  filter/                      # Generic filter chain; url/ and job_listing_filter/ rules
  freeextraction/              # LLM-free extraction from unambiguous structured data
  frontier/redis/              # Redis-backed, crash-safe, resumable URL frontier
  geo/                         # Deterministic location → ISO country resolver + gazetteer
  importer/                    # Catalog import jobs (async, idempotent merge)
  listingid/                   # Canonical source-URL identity for Corpus rows
  llmobs/                      # LLM-stage metrics, stats, content-duplication probe
  llmstream/                   # Durable per-run LLM work stage over Redis Streams
  openrouter/                  # LLM career-page classifier + job-listing extractor
  orchestrator/                # Crawl loop: frontier → worker pool
  pagegate/                    # Pre-LLM Gate + Extract Gate (graded score, positive evidence)
  parser/                      # HTML parser: main content, links, structured data (goquery)
  pool/                        # Generic worker pool
  processor/                   # discovery_/career_page_/url_/job_listing_/shadow_extraction_ processors
  robotstxt/                   # robots.txt checker (bounded cache + singleflight)
  runner/                      # Multi-run lifecycle: start, stop, pause, resume, drain
web/                           # React + Vite dashboard (embedded via //go:embed)
```

Domain types and repository interfaces live at the `internal/` root; infrastructure
implementations depend inward toward the domain, never the reverse.

## Technical Highlights

### Crash-safe, resumable frontier

The Redis frontier keeps per-domain queues with deadline-based cooldowns, a
visited set for dedup, and in-flight leases. `Next` and `AddURL` are each a single
Lua script, so concurrent workers can never double-pop a URL or race the dedup. A
worker that crashes mid-URL has its lease reclaimed once the TTL elapses, so no URL
is lost or duplicated across restarts. The visited set is a FIFO-capped hashed
sorted set rather than an unbounded set of full URLs, so a perpetual Discovery run's
memory footprint stays bounded (ADR-0027), and the pop path is indexed so scheduling
stays O(log N) as the frontier grows (ADR-0026).

### Durable LLM stage

Model calls do not run inline in the crawl loop. A gate-passing page is written to
a per-run Redis Stream and drained by a consumer group into the same processor
interface, so the crawl never blocks on the model, a slow model applies
backpressure through a bounded backlog instead of stalling workers, and a crash
redelivers the pending entry rather than losing it. Processors upsert on natural
keys, so redelivery is idempotent.

### Paying for the model only when it can change the answer

Two deterministic gates sit in front of the LLM. The **Gate** scores a candidate
page and returns reject / certain-accept / uncertain, so only the ambiguous middle
costs a classification call. The **Extract Gate** does the same for posting pages,
first rejecting hub-shaped pages and then requiring positive evidence that a page
*is* a single job listing. Where a page publishes unambiguous structured data,
**Free Extraction** reads the listing straight from it with no model at all — but
refuses pages whose structured data outlives what the page still renders. Both
gates are scored offline against labelled gold sets, and a sampled fraction of
gate rejects is extracted anyway in the background purely to measure what the gate
drops.

### Two acquisition lanes

A Company on a recognized ATS is collected through the provider's board API in one
call — ten providers (Greenhouse, Lever, Personio, Workable, Ashby,
SmartRecruiters, Recruitee, softgarden, Teamtailor, Manatal) — with no crawling and
no model. Everything else falls back to crawling and extracting posting pages. The
lane a listing came from also decides how its liveness is judged: an ATS listing by
absence from a freshly-fetched board, a crawled one by re-fetching its page.

### Composable filter chains

Filters use a generic `CheckFn[T]` composed via `Chain` and `Every`. URL filtering
short-circuits: hiring-related subdomains/paths (`careers`, `jobs`, …) pass, while
blogs, docs, shops, auth, and social hosts are blocked — steering crawls toward
career pages before any expensive work. During a Collection Cycle an additional
Scope fence, derived from each seed's own URL, keeps a crawl inside the Company it
was seeded from.

### Politeness

Before fetching or enqueueing a URL, the crawler checks the host's `robots.txt`.
Rules are cached per hostname behind a bounded LRU and concurrent first-time
fetches are collapsed with `singleflight`. Status handling follows RFC 9309
§2.3.1.3 (404/410 → allow-all, 5xx → disallow-all). The crawl walk paces itself
per host via the frontier's cooldowns; the refetch lane, which bypasses the
frontier, carries its own shared rate limiter keyed on the registrable domain, so
every tenant of a shared platform queues behind one limiter. The HTTP client is
wrapped in a retry decorator adding exponential backoff and context-aware
cancellation, keeping retry logic out of the core pipeline.

## Dependencies

- [pgx](https://github.com/jackc/pgx) — PostgreSQL driver + pool
- [goose](https://github.com/pressly/goose) — SQL migrations
- [go-redis](https://github.com/redis/go-redis) — Redis client
- [goquery](https://github.com/PuerkitoBio/goquery) — HTML parsing
- [temoto/robotstxt](https://github.com/temoto/robotstxt) — robots.txt parsing
- [xxhash](https://github.com/cespare/xxhash) — hashed visited-set keys
- [golang.org/x/net](https://pkg.go.dev/golang.org/x/net) — public suffix list (eTLD+1 identity)
- [godotenv](https://github.com/joho/godotenv) — `.env` loading
- [OpenTelemetry](https://opentelemetry.io/) + [Prometheus client](https://github.com/prometheus/client_golang) — metrics
- [React](https://react.dev/) + [Vite](https://vitejs.dev/) + [TanStack Query](https://tanstack.com/query) — dashboard

## License

MIT
