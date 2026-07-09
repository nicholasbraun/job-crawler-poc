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
	calls        metric.Int64Counter
	duration     metric.Float64Histogram
	gated        metric.Int64Counter
	content      metric.Int64Counter
	retries      metric.Int64Counter
	deadletter   metric.Int64Counter
	queueDepth   metric.Int64Gauge
	queuePending metric.Int64Gauge
}

// NewMetrics registers the LLM-stage instruments under the "llm" meter scope.
func NewMetrics() *Metrics {
	meter := otel.Meter("llm")
	return &Metrics{
		calls:        counter(meter, "crawler.llm.calls"),
		duration:     histogram(meter, "crawler.llm.call.duration", "ms"),
		gated:        counter(meter, "crawler.llm.gated"),
		content:      counter(meter, "crawler.llm.content"),
		retries:      counter(meter, "crawler.llm.retries"),
		deadletter:   counter(meter, "crawler.llm.deadletter"),
		queueDepth:   gauge(meter, "crawler.llm.queue.depth"),
		queuePending: gauge(meter, "crawler.llm.queue.pending"),
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

func (m *Metrics) recordQueueDepth(ctx context.Context, kind Kind, backlog, pending int64) {
	attrs := metric.WithAttributes(attribute.String("kind", string(kind)))
	m.queueDepth.Record(ctx, backlog, attrs)
	m.queuePending.Record(ctx, pending, attrs)
}
