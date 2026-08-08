package collection

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the Collection Crawl's otel/Prometheus instruments (ADR-0036): the
// per-Cycle counters (found / refreshed / closed listings, boards fetched /
// incomplete), the refetch lane's two gauges (robots friction, and the re-gate
// rejects a full content re-gate would Close) and the Cycle-duration histogram. They
// are built once and shared across Cycles (like llmobs.Metrics), tapped by the ATS and
// refetch lanes. The headline ListingsFound run counter is reused separately; these
// surface the collection-specific detail the run row does not carry.
type Metrics struct {
	found            metric.Int64Counter
	refreshed        metric.Int64Counter
	closed           metric.Int64Counter
	boardsFetched    metric.Int64Counter
	boardsIncomplete metric.Int64Counter
	robotsBlocked    metric.Int64Counter
	regateRejected   metric.Int64Counter
	cycleDuration    metric.Float64Histogram
}

// NewMetrics registers the Collection instruments on the "collection" meter. A
// registration error is logged, not fatal: the returned instrument is a no-op, so
// the taps stay unconditional (mirrors the other processors).
func NewMetrics() *Metrics {
	meter := otel.Meter("collection")
	return &Metrics{
		found:            counter(meter, "collection.listings.found"),
		refreshed:        counter(meter, "collection.listings.refreshed"),
		closed:           counter(meter, "collection.listings.closed"),
		boardsFetched:    counter(meter, "collection.boards.fetched"),
		boardsIncomplete: counter(meter, "collection.boards.incomplete"),
		robotsBlocked:    counter(meter, "collection.refetch.robots_blocked"),
		regateRejected:   counter(meter, "collection.refetch.regate_rejected"),
		cycleDuration:    histogram(meter, "collection.cycle.duration_ms"),
	}
}

func counter(meter metric.Meter, name string) metric.Int64Counter {
	c, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("collection: error setting up metric", "err", err, "name", name)
	}
	return c
}

func histogram(meter metric.Meter, name string) metric.Float64Histogram {
	h, err := meter.Float64Histogram(name)
	if err != nil {
		slog.Error("collection: error setting up metric", "err", err, "name", name)
	}
	return h
}

// Found tallies one saved posting (a found or refreshed save).
func (m *Metrics) Found(ctx context.Context) { m.found.Add(ctx, 1) }

// Refreshed tallies one changed posting re-enqueued for extraction.
func (m *Metrics) Refreshed(ctx context.Context) { m.refreshed.Add(ctx, 1) }

// Closed tallies n listings closed by the absence-sweep or a dormancy cascade.
func (m *Metrics) Closed(ctx context.Context, n int) { m.closed.Add(ctx, int64(n)) }

// BoardFetched tallies one reachable ATS board fetch.
func (m *Metrics) BoardFetched(ctx context.Context) { m.boardsFetched.Add(ctx, 1) }

// BoardIncomplete tallies one incomplete (presence-only) ATS board fetch.
func (m *Metrics) BoardIncomplete(ctx context.Context) { m.boardsIncomplete.Add(ctx, 1) }

// RobotsBlocked tallies one refetch short-circuited by the robots re-check (page
// probe or per-listing), from the politeDownloader's single choke point
// (ADR-0040). It is a friction gauge: it deliberately conflates a real Disallow
// with a transient robots.txt 5xx the checker folds to disallow-all, so a spike
// reads as a robots.txt outage more than mass Disallow adoption.
func (m *Metrics) RobotsBlocked(ctx context.Context) { m.robotsBlocked.Add(ctx, 1) }

// RegateRejected tallies one Open Job Listing whose page the Extract Gate would now
// reject on full content — how many the Corpus would shed if a content re-gate ran
// (ADR-0044). It is a measurement, not an action: nothing is Closed, and no listing's
// Listing Lifecycle, Liveness, streak or timestamps move. Read it as an increase over a
// Cycle, and as a LOWER bound — only listings actually refetched this Cycle are judged.
func (m *Metrics) RegateRejected(ctx context.Context) { m.regateRejected.Add(ctx, 1) }

// RecordCycle records one whole-Cycle wall-clock duration in milliseconds.
func (m *Metrics) RecordCycle(ctx context.Context, d time.Duration) {
	m.cycleDuration.Record(ctx, float64(d.Milliseconds()))
}
