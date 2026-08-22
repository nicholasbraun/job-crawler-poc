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
| `EXTRACT_LEARNED_VETO`   | `false`                                 | Learned Veto (ADR-0049): the Extract Gate withholds the call from a page that carries Positive Evidence but whose Posting Score is below the threshold compiled in beside the weights. `false` — the default — is today's gate exactly, and off the score is never computed at all. Setting it `true` withheld 50 of 177 calls (28.2%) at zero `detail` loss over the committed Extract Gold Set, but that is an in-sample figure: flipping it is owed the capture-window pass in *Turning the Learned Veto on* under [Tests](#tests) (ADR-0049) |
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

### Turning the Learned Veto on (ADR-0049)

The Extract Gate's last rung ships **off**. `EXTRACT_LEARNED_VETO=false` is today's gate
exactly, and off the **Posting Score** is never computed at all, so a crawl that has not
turned the rung on pays nothing for it. Turning it on is a separate act, and this is its
runbook.

The offline evidence already stands, and it is **in-sample**: over the committed Extract Gold
Set — the same 442 rows the shipped weights were fitted on — the Learned Veto withholds
**50 of 177** of the calls the Positive Evidence rung accepts (**28.2%**) and loses **none** of
that rung's 127 `detail` rows, taking precision from 0.7175 to 1.0000. That 1.0000 is a
memorisation ceiling, not a forecast; ADR-0049's out-of-fold ladder is the generalisation read
and it costs 16 `detail` rows at a comparable depth.
`TestExtractGoldSetFalseDropGuardUnderTheLearnedVeto` holds the in-sample figure page for page
under a plain `go test ./...`. Reproduce the scorecard any time:

```bash
echo '{"LearnedVeto": true}' > /tmp/veto.json     # -gate-config takes a PATH, not inline JSON
go run ./cmd/llmbench score-capture -in cmd/llmbench/extract-goldset/goldset.jsonl -gate-config /tmp/veto.json
```

What is missing is evidence about pages that set does not contain. That is what the flip is
gated on.

**The condition, registered in ADR-0049 before the measurement existed:** the Learned Veto
is turned on only if it vetoes **at least 10% of the Positive Evidence rung's accepts while
losing none of the `detail` rows that rung accepts today**. `llmbench train-scorer` reports
both figures restricted to that population and to no other, so a number over all scorable
rows can never be mistaken for it. The run on the committed set met the condition at 28.2%
— but read that as in-sample: out of fold, host-grouped, the two reads agree to a 10% cut
and diverge past it, costing 16 `detail` rows at a 30% depth. Merging the rung was never
gated on the condition; flipping its default is.

**Do not grade the Learned Veto against the extractor's own verdict, and do not add an
observe mode to production in order to.** That verdict runs at precision **0.454** against
human labels (0.816 on the Random Stratum) and its errors concentrate on exactly the pages
that decide an operating point, so grading the veto against it would report the rung as safe
precisely where it is not. The absence of an observe mode is a decision, not an omission.
The offline path is better and cheaper: the capture tap stores the full parsed content of
every page that reaches the extract stage, so the share a threshold would veto is computable
over a real stream frame with **no labels involved at all**, and the pages it would drop are,
by construction, the Boundary Stratum for this rule.

**1. Run a capture window with the rung off.**

```bash
# .env, for the duration of the window
EXTRACT_LEARNED_VETO=false                              # the window records what the rung WOULD judge
EXTRACT_CAPTURE_PATH=<repo>/capture/veto-window.jsonl   # gitignored; mkdir the directory first
EXTRACT_CAPTURE_MAX=0                                   # uncapped; see below
```

The tap sits downstream of the Extract Gate and fires once per completed extraction, so what
it records is exactly the population the veto judges: the pages Positive Evidence accepted.
Leave `EXTRACT_CAPTURE_MAX` at `0` — the default caps records *per verdict*, which makes the
file a sampling design rather than a stream frame, and a depth computed over a capped file is
a number about the cap. Leave `PARSE_STRUCTURAL_RENDERING` wherever production has it: the
Posting Score reads the page's Flattened Text, so that switch cannot move it (ADR-0046). Note
the window's start time — the drawing verbs take it as `-since` and never reconstruct it.
Under Docker this is a gitignored `docker-compose.override.yml` carrying the two variables and
a `./capture` bind mount, the shape #116 used.

**2. Score the window offline against the shipped weights.** The number to compute is the
**veto depth** on that frame: of the pages today's gate extracts, the share whose Posting
Score falls below `pagegate.VetoThreshold` (`0.605395`, compiled in beside the weights). It
needs no labels, and the pages below the cut are the drop set step 3 confirms.

```bash
go run ./cmd/llmbench goldset-sample-veto-boundary \
    -capture capture/veto-window.jsonl -since <the window's start, RFC3339>
```

This writes **nothing**. It is `goldset-sample-boundary`'s machinery against the veto's own
pair (`LearnedVeto` off versus on), and that pair lives in `cmd/llmbench/goldsetboundary.go`
rather than behind a `-gate-config` flag, for the reason that file states: a flag would let a
later run silently redefine a boundary the committed rows claim. Three lines of the report
decide the rollout:

- **`gate extracts` against `candidate frame`.** The tap sits downstream of the Extract Gate,
  so these should be within a few rows of each other. A large gap means a reject rung moved
  since the window and the frame is stale.
- **`depth` against ADR-0049's pre-registered 10% floor**, which the report itself states as
  MET or NOT MET. Below it, **stop**: the honest outcome is an amendment to ADR-0049 and the
  rung stays off.
- **`reversed`**, which must be `0`. The Learned Veto is subtractive only, so a page it *adds*
  means the rung is not the rule this drawing assumes; the verb refuses to draw when it happens.

**3. Draw the drop set and confirm it blind.**

```bash
go run ./cmd/llmbench goldset-sample-veto-boundary \
    -capture capture/veto-window.jsonl -since <the window's start, RFC3339> -draw
go run ./cmd/llmbench goldset-ui -by "<your name>" -stratum veto-boundary
```

`-draw` appends every page below the cut — **both** the ones the live extractor accepted and
the ones it abstained on — as the `veto-boundary` stratum: a census, append-only, weight 1,
unlabelled. Both halves on purpose: ADR-0049 forbids grading this rung against the extractor's
own verdict (precision **0.454** against human labels), so filtering the drop set by it would
import that bias before a human ever reads a row. That is where this drawing differs from
ADR-0044's `boundary` stratum, which takes the accept half only — and they are separate strata
so a committed row says which pair drew it. The rows the draw appended carry no confirmer, so
the confirmation surface serves them one at a time.

The label is taken **before** anything is revealed (ADR-0048), each row's **Capture Fidelity**
is measured against a fresh fetch so a live view is admitted or refused per row (ADR-0047),
and the captured content is the authority in every case. These confirmed rows outlive the
decision, which is the point: label quality, not volume, is the binding constraint on
everything in this line of work.

**4. Commit the confirmed rows — and refit, in one command.**

```bash
go run ./cmd/llmbench goldset-refit
```

`goldset-refit` is the whole of what this step used to be. It folds the confirmed rows into
the Extract Gold Set through `goldset-apply`, recomputes and rewrites the sixteen counts the
record determines in `cmd/llmbench/goldset_test.go`, re-runs the trainer that
`go generate ./internal/pagegate` runs, and then runs `go test -count=1 ./...` over the
regenerated tree — the only step that sees the new weights *compiled*, which is why it is a
real build and not something this tool can do in its own process. It ends on the verdict, and
**exits non-zero when the result is not shippable**: a `detail` row the Positive Evidence rung
accepts lost to the cut, the pre-registered condition no longer met, or the guard suite red.
Reading that condition a second time — on the artifact you are actually about to ship rather
than the one step 2 measured — is the step a human skips, so the verb does it and turns it
into an exit code.

Remember what ADR-0049 makes true, because it is why this step is one command now: **the Gold
Set produces part of the gate as well as scoring it.** Adding rows changes the fitted weights
and re-chooses `VetoThreshold` under the same zero-`detail`-loss constraint, so the operating
point moves with the labels rather than away from them. Never edit a label and never move
`VetoThreshold` by hand.

The verb rewrites the counts, and that is deliberate — but it never makes the change
invisible: every one of the sixteen is printed, changed or not, and each rewrite lands in your
diff. It **refuses** to move a confirmation ratchet in the direction that means a signature
vanished (ADR-0048), and it stamps no provenance of its own: a confirmation is `goldset-ui`'s
act and a human's. Two things it does not own and you still do, in the same commit:
`drawnStrata` (which does not yet list `veto-boundary`) and the drawings table of
`cmd/llmbench/extract-goldset/README.md` — paste the draw's summary there, the frame, the row
count and **the `VetoThreshold` the cut was taken at**, because the threshold moves with the
refit in this very step and that table is where the other three drawings record theirs.
`cmd/llmbench/extract-goldset/README.md` carries the whole maintenance sequence.

Expect the suite to name things after a confirmation pass, and expect them: `RESTANDING`
entries in `extractGoldSetFalseDrops` are exceptions whose labels have just been confirmed,
and ADR-0043 requires each to be re-argued rather than inherited. A "pick a weaker fixture"
failure wants a different page, never a moved threshold — the fixtures live in three places,
`internal/pagegate/learned_veto_test.go`,
`internal/processor/url_processor/url_processor_test.go`, which carries its own copies because
an external test package cannot import them, and `cmd/llmbench/goldsetvetoboundary_test.go`,
whose capture fixtures have to land on a named side of the cut for the drawing's own cases to
mean anything. That is also why the verb runs the **whole** suite rather than
`./cmd/llmbench/... ./internal/pagegate/...`: a hand-picked list is a list that goes stale.

To re-read the numbers later without refitting, `go run ./cmd/llmbench train-scorer` prints
the same report; to re-read the scorecard, run the `score-capture` command at the top of this
section again, **after** the refit, so it scores the artifact you are about to ship rather
than the one step 2 measured.

**5. Flip the default.**

```bash
# .env
EXTRACT_LEARNED_VETO=true
docker compose up -d crawler   # `restart` re-runs the old environment; `up -d` recreates it
```

Confirm it took from the startup line, which is the only place a running crawl says which
operating point it is enforcing:

```
extract gate learned veto (ADR-0049) enabled=true threshold=0.605395
```

#### After the flip — what to read

All four readings are the **walk's**, and so is the rung: the Collection Cycle's refetch
re-gate clears the Learned Veto from its own copy of the gate config (ADR-0049), so
`collection_refetch_regate_rejected_total` stays a purely structural census of the Corpus's
existing false positives and can still be read as #208's sizing number after the flip.

The **Learned Veto (ADR-0049)** row of the LLM telemetry dashboard
(`grafana/dashboards/llm-telemetry.json`) carries all of it:

- **Extract calls the Learned Veto saved** —
  `crawler_llm_gated_total{kind="extract",reason="learned_veto"}`. Its own gated reason rather
  than the pooled structural one, so the cost of pulling the switch is readable on its own.
- **Veto depth** — the calls saved over `crawler_llm_posting_score_count`, i.e. every page the
  rung judged, its vetoes and its survivors alike. This is the live counterpart of the
  pre-registered 10% floor.
- **Learned Veto false-drop rate** —
  `crawler_llm_shadow_total{verdict="accept",rung="learned_veto"}` over that rung's completed
  Shadow Extraction verdicts. This is the rung's *only* risk: it can never add a call, so all
  of its downside sits here. The series is primed at zero, so the first false-drop is visible
  to `increase()` rather than being absorbed as the baseline, and the matching log line carries
  the score that caused it (`grep posting_score`). It needs `SHADOW_EXTRACT_RATE` non-zero, and
  at 1% of the veto's drops the first sample takes a while — an empty series is "not yet
  observed", not "none".
- **Posting Score distribution and quantiles** — `crawler_llm_posting_score_bucket`, over the
  rung's accepts *and* its vetoes. Recording only the vetoes would show where the cut is
  without showing what it is cutting into. The bucket ladder is fixed and deliberately not
  pinned to `VetoThreshold`: a boundary moving with every refit would re-bucket every
  historical series and destroy the one comparison the instrument exists for.

**What drift looks like.** The 28.2% is in-sample over a deliberately Boundary-heavy
population, so it is *not* a prediction of the live depth — the number to compare against is
the one step 2 measured on your own capture window. A live depth well away from it means the
walked stream no longer looks like the frame the operating point was chosen on. Beyond the
depth: record p10/p50/p90 on flip day and watch them walk; a p50 drifting away from where it
sat when the weights were fitted means the cut is being applied to a population nobody measured
it on. And read the shape, not just the level — a cut sitting in a steep step of the cumulative
panel means small threshold moves swing spend hard. Either reading is a reason to take another
capture window, not to nudge the threshold.

One reading that is not on the row: the veto sits upstream of the extract stage, so a page it
withholds is also a page **Free Extraction** never sees. If
`crawler_llm_gated_total{kind="extract",reason="structured_data"}` falls sharply after the
flip, the veto is cutting into Job Listings that were costing nothing.

#### Backing it out

One environment variable, no deploy — the same binary, whose compiled-in weights go inert:

```bash
# .env
EXTRACT_LEARNED_VETO=false     # or delete the line; "" reads as the built-in default
docker compose up -d crawler
```

That restores the unconditional Positive Evidence accept, and off, the Posting Score is not
computed at all. The two extract switches compose independently: pulling
`EXTRACT_REQUIRE_POSITIVE_EVIDENCE` instead restores the blanket accept, and pulling both
restores the gate as it stood before ADR-0044.

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
