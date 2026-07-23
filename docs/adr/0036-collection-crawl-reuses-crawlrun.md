# The Collection Crawl reuses the CrawlRun machinery

Periodic collection is modeled as a new `CrawlKindCollection` on the existing
`CrawlDefinition`/`CrawlRun`, under a single **singleton collection definition** — not a
distinct `CollectionRun` aggregate. The run lifecycle (pause/resume, boot reconcile,
graceful-shutdown park, per-run Redis Frontier, counters) is substantial, tested machinery the
collector needs verbatim; only the *engine wiring* differs (no keyword/country gate, Corpus
write, absence-sweep, refetch pass). Seeds come from the whole Catalog via the existing
`ListSeeds` → `RouteSeeds` path the Keyword Crawl already used.

One **Collection Cycle** is one whole-catalog `CrawlRun`; the one-active-run invariant gives
non-overlapping Cycles for free — a scheduler firing while the last Cycle still runs just gets
`ErrActiveRunExists`.

## Scheduling

A **poll-based due-check** (not a fixed ticker) starts a Cycle when `now ≥ lastStart + interval`
(default daily) and no Cycle is active. This resumes immediately after an *overrun* (a Cycle
that runs longer than the interval) instead of idling until the next aligned tick, and never
bursts to "catch up" missed windows. The due condition derives from persisted run rows, so it
survives restarts.

## Rejected

A distinct `CollectionRun` aggregate — it would duplicate the entire
pause/resume/reconcile/shutdown state machine to express policy the engine wiring already
carries. Per-company runs — they'd break the singleton and multiply concurrent runs;
per-company *cadence*, if ever needed, is "which seeds a Cycle includes," not a new run type.
