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

// newVisitedSizeGauge registers crawler.frontier.visited.size, the post-eviction
// cardinality of the visited ZSET after each NEW insert, labeled by run_id.
// run_id-labeled for the same reason as domains.size (ADR-0026): a gauge is
// last-value, so concurrent runs would clobber one unlabeled series. The same
// cardinality caveat applies — one series accrues per run_id the process has ever
// popped (bounded for this crawler's perpetual-Discovery-plus-Keyword shape);
// evicting a finished run's series in DeleteRun is the same deferred hardening.
func newVisitedSizeGauge() metric.Int64Gauge {
	g, err := otel.Meter("frontier").Int64Gauge(
		"crawler.frontier.visited.size",
		metric.WithDescription("Post-eviction cardinality of the Frontier visited ZSET after each NEW insert, by run_id (ADR-0027)."),
	)
	if err != nil {
		slog.Error("frontier: error setting up visited-size gauge", "err", err)
	}
	return g
}

// newVisitedEvictedCounter registers crawler.frontier.visited.evicted, the count
// of visited entries FIFO-evicted by NEW inserts, labeled by run_id. Unlike the
// no-run_id transient-retry counter, this one carries run_id deliberately: an
// eviction is a per-run event, and the run_id lets the visited.size-vs-cap panel
// and the eviction-rate panel align on the same run. A counter sums (no last-value
// clobber), so the label adds attribution, not correctness risk; its cardinality
// is bounded by the same run-shape argument as the domains.size gauge (ADR-0026).
// Stays flat at zero until a run crosses the cap — then Re-admission is happening.
func newVisitedEvictedCounter() metric.Int64Counter {
	c, err := otel.Meter("frontier").Int64Counter(
		"crawler.frontier.visited.evicted",
		metric.WithDescription("Count of Frontier visited-ZSET entries FIFO-evicted past the per-run cap, by run_id (ADR-0027). Nonzero means Re-admission."),
	)
	if err != nil {
		slog.Error("frontier: error setting up visited-evicted counter", "err", err)
	}
	return c
}
