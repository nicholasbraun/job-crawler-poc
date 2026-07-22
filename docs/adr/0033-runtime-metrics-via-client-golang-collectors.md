# Go runtime and process metrics come from client_golang collectors, not OTel-native runtime instrumentation

## Context / Decision

The crawler needs a memory/CPU/goroutine/GC data source so an approaching OOM
(see the robots-cache incident, ADR-0032) is visible before the kernel
SIGKILLs it. `internal/otel/otel.go` builds an OpenTelemetry meter provider
behind a Prometheus exporter and serves `/metrics` via `promhttp.Handler()` on
`:2223`; Prometheus already scrapes it. Every *custom* metric in the codebase
(downloader, frontier, LLM) is emitted through the OTel meter, so the obvious
move — and the naive reading of #183 — is to add
`go.opentelemetry.io/contrib/instrumentation/runtime` and stay OTel-native.

Two facts make the collectors the right source instead:

1. **They are already live.** `promhttp.Handler()` serves Prometheus's *default*
   registry, and `client_golang` auto-registers `NewGoCollector()` +
   `NewProcessCollector()` into that registry at package init. The OTel
   Prometheus exporter also defaults to the same registry. So
   `:2223/metrics` **already** emits `go_goroutines`, `go_memstats_*`,
   `process_resident_memory_bytes`, `process_cpu_seconds_total`, and
   `process_open_fds` — confirmed by `curl`. The premise "no data source
   exists" was wrong; nothing needs wiring for the Go/process axis.
2. **OTel-native does not cover the process.**
   `instrumentation/runtime` instruments the Go *runtime* only — heap,
   goroutines, GC. It emits nothing for process RSS, open FDs, or CPU, which
   #183 explicitly requires. Adopting it would mean bolting a second source on
   for the process-level metrics anyway, and would produce
   `process_runtime_go_*`-style names that diverge from the `go_*`/`process_*`
   names every off-the-shelf Grafana/PromQL example assumes.

We therefore take runtime and process metrics from the `client_golang`
collectors already in the default registry, and add exactly one bespoke gauge —
**`GOMEMLIMIT`** — which no collector exports. It is read at scrape time via
`debug.SetMemoryLimit(-1)` (returns the current limit without mutating it) and
surfaced through the existing OTel meter, keeping the one custom instrument
consistent with how the rest of the crawler's custom metrics are emitted. The
`go_memstats_*` classic set already covers heap in-use/allocated, next-GC
target, goroutines, and GC pause/frequency, so heap-vs-`GOMEMLIMIT` headroom is
graphable as soon as the gauge lands.

## Considered options

- **OTel-native `instrumentation/runtime`.** Rejected: covers the Go runtime
  only, so it satisfies none of the process-level bullets (RSS, FDs, CPU) and
  would still need a second source; it also renames the standard series away
  from the `go_*`/`process_*` convention for no benefit, since the transport is
  Prometheus either way. Consistency with the OTel meter is a weak argument for
  infra metrics that are not part of the domain model.
- **Full `client_golang` extended runtime metrics
  (`WithGoCollectorRuntimeMetrics`).** Deferred. The default (classic memstats)
  collector already satisfies every #183 bullet; the extended set roughly
  triples GC-histogram cardinality for detail nobody has asked for. Easy to opt
  into later if a GC-pause histogram becomes interesting.
- **A plain `prometheus.NewGauge` for `GOMEMLIMIT`.** Rejected in favour of an
  OTel observable gauge. A raw Prometheus gauge guarantees the exact series name
  but breaks the "custom metrics are OTel" convention; for the one bespoke
  gauge, consistency wins and the emitted name is pinned at implementation time
  against the actual `/metrics` output.
- **Skip `GOMEMLIMIT`, infer headroom from `mem_limit`.** Rejected: the soft
  Go limit (`GOMEMLIMIT`), not the container `mem_limit`, is what the GC targets
  and what a heap climb approaches. Making the exact ceiling the heap is chasing
  a first-class series is the whole point of the headline panel.

## Consequences

- **The "wire metrics" half of #183 collapses to one gauge.** The Go/process
  collectors need no code — they already scrape. The Go change is the single
  `GOMEMLIMIT` observable gauge; the bulk of the ticket is the Grafana
  dashboard.
- **Runtime metrics bypass the OTel meter provider.** They register straight
  into the default Prometheus registry (as they always have). This is a
  deliberate, idiomatic exception to the codebase's OTel-for-custom-metrics
  convention — a future reader tempted to "make it OTel-native" would regress
  the process-level metrics. That is what this ADR exists to prevent.
- **Dashboard queries bind to `go_*`/`process_*` names.** Standard, stable, and
  portable to any Go Prometheus dashboard; not tied to OTel semantic-convention
  churn.
