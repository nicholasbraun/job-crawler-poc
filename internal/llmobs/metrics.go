package llmobs

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the OpenTelemetry instruments for the LLM stage, exported on the
// existing Prometheus endpoint. One instance is shared across all runs; each
// series is distinguished by attributes (kind/outcome/reason/result) applied at
// record time, matching the repo's OTel idiom. Instrument-creation errors are
// logged and the returned no-op instrument is still used, so a metrics hiccup
// never breaks a crawl.
type Metrics struct {
	calls    metric.Int64Counter
	duration metric.Float64Histogram
	gated    metric.Int64Counter
	// shadow counts Shadow Extractions by verdict and rejecting rung (ADR-0044), on
	// its own counter so measurement spend never lands in calls.
	shadow metric.Int64Counter
	// shadowDropped counts samples the Shadow Extraction lane SHED before any verdict
	// -- its bounded stream had no capacity. It is the trustworthiness of the
	// false-drop rate's denominator: a shed sample the counter did not record would
	// leave that rate resting on a silently non-uniform subsample.
	shadowDropped metric.Int64Counter
	content       metric.Int64Counter
	retries       metric.Int64Counter
	deadletter    metric.Int64Counter
	queueDepth    metric.Int64Gauge
	queuePending  metric.Int64Gauge
}

// NewMetrics registers the LLM-stage instruments under the "llm" meter scope.
func NewMetrics() *Metrics {
	meter := otel.Meter("llm")
	return &Metrics{
		calls:         counter(meter, "crawler.llm.calls"),
		duration:      histogram(meter, "crawler.llm.call.duration", "ms"),
		gated:         counter(meter, "crawler.llm.gated"),
		shadow:        counter(meter, "crawler.llm.shadow"),
		shadowDropped: counter(meter, "crawler.llm.shadow.dropped"),
		content:       counter(meter, "crawler.llm.content"),
		retries:       counter(meter, "crawler.llm.retries"),
		deadletter:    counter(meter, "crawler.llm.deadletter"),
		queueDepth:    gauge(meter, "crawler.llm.queue.depth"),
		queuePending:  gauge(meter, "crawler.llm.queue.pending"),
	}
}

func counter(meter metric.Meter, name string) metric.Int64Counter {
	c, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("llmobs: error setting up counter", "err", err, "name", name)
	}
	return c
}

func histogram(meter metric.Meter, name, unit string) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name, metric.WithUnit(unit))
	if err != nil {
		slog.Error("llmobs: error setting up histogram", "err", err, "name", name)
	}
	return h
}

func gauge(meter metric.Meter, name string) metric.Int64Gauge {
	g, err := meter.Int64Gauge(name)
	if err != nil {
		slog.Error("llmobs: error setting up gauge", "err", err, "name", name)
	}
	return g
}

func (m *Metrics) recordCall(ctx context.Context, kind Kind, outcome Outcome, ms float64) {
	attrs := metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("outcome", string(outcome)),
	)
	m.calls.Add(ctx, 1, attrs)
	m.duration.Record(ctx, ms, attrs)
}

func (m *Metrics) recordGated(ctx context.Context, kind Kind, reason Reason) {
	m.gated.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("reason", string(reason)),
	))
}

// recordShadow counts one Shadow Extraction on its OWN counter, labelled by verdict
// AND by the Extract Gate rung that rejected the page (ADR-0044). It never touches
// m.calls or m.duration: the shadow lane is real spend the cost view sums
// separately, but the call counter must keep meaning "extract calls the Extract Gate
// admitted".
//
// The rung is what makes the false-drop rate actionable. Split by it,
// shadow{verdict=accept} / shadow over rung=positive_evidence is the share the
// Positive Evidence kill switch would recover, and the same ratio over a reject rung
// is the share it would not -- so the first reading after the flip cannot read high
// for a cause the named revert does not fix.
//
// There is still deliberately NO kind attribute: Shadow Extraction is extract-only,
// and a kind label would invite a sum by (kind) that silently merges it with real
// calls.
func (m *Metrics) recordShadow(ctx context.Context, verdict ShadowVerdict, rung string) {
	m.shadow.Add(ctx, 1, metric.WithAttributes(
		attribute.String("verdict", string(verdict)),
		attribute.String("rung", rung),
	))
}

// recordShadowDropped counts one sampled page the Shadow Extraction lane never got
// to measure, labelled by the rung that rejected it. Sampling is uniform over
// gate-rejected pages, but SHEDDING is not -- a full stream sheds whatever arrives
// while it is full -- so a drop stream that is not visible turns the false-drop rate
// into an estimate over an unknown subsample. Its own counter, never folded into
// shadow: a sample with no verdict must not enter that counter's denominator.
func (m *Metrics) recordShadowDropped(ctx context.Context, rung string) {
	m.shadowDropped.Add(ctx, 1, metric.WithAttributes(attribute.String("rung", rung)))
}

func (m *Metrics) recordContent(ctx context.Context, kind Kind, duplicate bool) {
	result := "unique"
	if duplicate {
		result = "duplicate"
	}
	m.content.Add(ctx, 1, metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("result", result),
	))
}

func (m *Metrics) recordRetry(ctx context.Context, kind Kind) {
	m.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(kind))))
}

func (m *Metrics) recordDeadLetter(ctx context.Context, kind Kind) {
	m.deadletter.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", string(kind))))
}

// recordQueueDepth records the backlog/pending gauges keyed by kind AND run_id.
// Unlike the counters, these are gauges (last-writer-wins per series), so without
// a run_id two concurrent runs would write the same {kind} series and clobber each
// other — a Grafana sum by (kind) would then track one run, not their total. The
// run_id splits them so the sum is accurate; a finished run records a terminal 0
// (see Stage.Close) so its series does not pin the sum above the live total.
func (m *Metrics) recordQueueDepth(ctx context.Context, kind Kind, runID string, backlog, pending int64) {
	attrs := metric.WithAttributes(
		attribute.String("kind", string(kind)),
		attribute.String("run_id", runID),
	)
	m.queueDepth.Record(ctx, backlog, attrs)
	m.queuePending.Record(ctx, pending, attrs)
}
