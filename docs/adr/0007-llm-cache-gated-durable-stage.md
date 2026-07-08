# LLM as a cache-gated, durable, decoupled stage

The career-page classifier and job-listing extractor are treated as a distinct
crawl **stage** behind a durable queue seam, not as synchronous per-page calls
made inline from the crawl worker pools. The stage is optimized for **call
volume first**: a page reaches the LLM only after cheap pre-LLM gating
(URL/ATS-host/structure heuristics, existing JSON-LD and keyword filters) and
only when a persistent, content-hash-keyed **verdict cache** (Postgres) misses.

The stage stays **in-process for now** (ADR-0002): a Redis Stream with a
consumer group is the work queue (ADR-0003 — Redis holds transient, per-run,
resumable state), while verdicts, extractions, and the cache are persisted to
Postgres (durable). Splitting the stage into a separate worker process/service
is deferred until there is a concrete reason (run the model on separate/GPU
hardware, or scale it independently of crawling); the queue seam makes that a
refactor, not a rewrite, exactly as ADR-0002 anticipates.

## Why

The driver of LLM cost is **calls × price-per-call**, and observed spend
(~$10 in a few hours on a hosted nano model) is volume-driven, not price-driven:
a discovery run classifies most gate-passing pages, and a perpetual crawl
re-sees the same pages repeatedly. Cutting volume (gating + caching) is the
cost fix; moving to a local model removes per-call price; decoupling does
**neither** on its own — it buys resilience, backpressure for a slow serial
local model, and independent resumability of the LLM stage (which also closes
the extraction-loss gap seen when a call times out or the process restarts,
cf. #32).

## Considered options

- **Keep synchronous inline LLM calls (status quo).** Simple, but every timeout
  or restart silently drops that extraction, a slow local model stalls the crawl
  pools, and there is no natural place to cache or rate-limit. Rejected.
- **Extract a separate LLM service now, with a broker (Kafka/NATS).** The right
  long-term seam but premature: a second deployable, delivery semantics, and
  monitoring are heavy ops for a laptop-scale POC, and it conflicts with the
  monolith-for-now decision (ADR-0002). Deferred behind the in-process seam.
- **Persist the task queue in Postgres instead of Redis Streams.** Viable and
  simplest to reason about (a `llm_tasks` table with a status column), but
  couples hot-path per-run work to the durable store, against ADR-0003. Redis
  Streams reuse the datastore we already run and match the transient/resumable
  model. Kept as the fallback if Streams operational overhead proves unjustified.
