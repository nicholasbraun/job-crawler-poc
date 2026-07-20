package redis

import (
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// The sibling transient-retry counter (crawler.frontier.transient_retries) lives
// in retry.go, coupled to withRetry; the two pop instruments below are the pop
// path's own and register here. All three register under the "frontier" meter
// scope and log-and-continue on error: OTel returns a non-nil no-op instrument
// on a registration error, so a metrics hiccup never breaks a crawl and callers
// never nil-guard a Record.

// newPopLatencyHistogram registers crawler.frontier.next.time, a label-free
// millisecond histogram of each pop-script evaluation. It is deliberately
// label-free (same low-cardinality discipline as the transient-retry counter):
// the aggregate p50/p95/p99 across runs is the O(N)-regression sentinel and
// histograms combine cleanly. It excludes intentional WAIT sleeps, which happen
// in Next's switch after the pop-script evaluation this measures.
//
// The bucket boundaries are set explicitly with sub-millisecond resolution. A
// healthy indexed pop is a few hundred microseconds, so OTel's default buckets
// ({0,5,10,25,...}ms) would file every healthy pop into the first (0,5]ms bucket
// and a histogram_quantile p99 would interpolate to ~5ms regardless of the true
// value — hiding a gradual O(N) regression until it crossed 5ms and defeating
// the early-warning sentinel this instrument exists to be.
func newPopLatencyHistogram() metric.Float64Histogram {
	h, err := otel.Meter("frontier").Float64Histogram(
		"crawler.frontier.next.time",
		metric.WithUnit("ms"),
		metric.WithDescription("Duration of a Frontier pop-script evaluation in ms, excluding intentional WAIT sleeps (ADR-0026)."),
		metric.WithExplicitBucketBoundaries(0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 50, 100),
	)
	if err != nil {
		slog.Error("frontier: error setting up pop-latency histogram", "err", err)
	}
	return h
}

// newDomainsSizeGauge registers crawler.frontier.domains.size, the current
// domain-schedule cardinality (count of non-empty Politeness Domains). It is
// labeled by run_id: a gauge is last-value, so two concurrent runs would clobber
// one unlabeled series. This is a deliberate, narrow exception to the counter's
// no-run_id rule — the same reasoning as llmobs.recordQueueDepth.
//
// Cardinality cost: the SDK's last-value aggregation never evicts an attribute
// set, so one series accrues per run_id the process has ever popped, not just per
// concurrently-active run — the count grows with runs seen over the process
// lifetime. A bounded run's terminal DONE reply carries cardinality 0, so a
// finished run's series settles at 0 rather than disappearing. This is bounded
// enough for this crawler's shape (a perpetual Discovery plus occasional bounded
// Keyword Crawls); evicting a finished run's series in DeleteRun is a future
// hardening tracked as a follow-up, deliberately out of #156's DeleteRun-unchanged
// scope.
func newDomainsSizeGauge() metric.Int64Gauge {
	g, err := otel.Meter("frontier").Int64Gauge(
		"crawler.frontier.domains.size",
		metric.WithDescription("Current Frontier domain-schedule cardinality (non-empty Politeness Domains), by run_id (ADR-0026)."),
	)
	if err != nil {
		slog.Error("frontier: error setting up domain-schedule-size gauge", "err", err)
	}
	return g
}
