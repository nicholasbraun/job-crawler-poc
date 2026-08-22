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
	// postingScore is the live distribution of the Extract Gate's Posting Score over
	// the pages the Learned Veto JUDGED -- both the ones it let through and the ones it
	// vetoed (ADR-0049). Recording only the vetoes would show where the cut is without
	// showing what it is cutting into, and this distribution is the drift detector: it
	// is how a reader sees whether the live stream still resembles the Extract Gold Set
	// the weights were fitted on. Unlabelled by design; see recordPostingScore.
	postingScore metric.Float64Histogram
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
		calls:         counter(meter, "crawler.llm.calls"),
		duration:      histogram(meter, "crawler.llm.call.duration", "ms"),
		gated:         counter(meter, "crawler.llm.gated"),
		shadow:        counter(meter, "crawler.llm.shadow"),
		shadowDropped: counter(meter, "crawler.llm.shadow.dropped"),
		postingScore:  scoreHistogram(meter),
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

// postingScoreBuckets are the Posting Score histogram's explicit bucket boundaries:
// twenty equal 0.05-wide bands over the score's [0,1] range. OTel's defaults run to
// 10,000 and would file every score into the first bucket, leaving the distribution
// unreadable -- the same reason internal/frontier/redis sets its own.
//
// The ladder is deliberately NOT pinned to pagegate.VetoThreshold, and this package
// deliberately does not know that value. The threshold moves whenever the weights are
// refitted; a boundary that moved with it would silently re-bucket every historical
// series and destroy the one comparison this instrument exists for. A fixed ladder
// keeps every scrape ever taken comparable, and the dashboard draws the cut as a
// threshold line instead. The share below the cut needs no bucket of its own either:
// it is gated{reason="learned_veto"} over this histogram's count, and both are exact.
var postingScoreBuckets = []float64{
	0.05, 0.10, 0.15, 0.20, 0.25, 0.30, 0.35, 0.40, 0.45, 0.50,
	0.55, 0.60, 0.65, 0.70, 0.75, 0.80, 0.85, 0.90, 0.95,
}

// scoreHistogram registers the Posting Score histogram with its own bucket ladder.
// It does not go through the shared histogram helper because that one takes a unit
// and OTel's default boundaries; a score in [0,1] is unitless and needs the ladder
// above to be readable at all.
func scoreHistogram(meter metric.Meter) metric.Float64Histogram {
	h, err := meter.Float64Histogram(
		"crawler.llm.posting.score",
		metric.WithDescription("Distribution of the Extract Gate's Posting Score over the pages the Learned Veto judged, accepts and vetoes alike (ADR-0049). Empty unless EXTRACT_LEARNED_VETO is on."),
		metric.WithExplicitBucketBoundaries(postingScoreBuckets...),
	)
	if err != nil {
		slog.Error("llmobs: error setting up the posting-score histogram", "err", err)
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

// recordPostingScore observes one Posting Score, with NO attributes at all, which is
// the decision ADR-0049 makes explicitly: the score is never a label value. Shadow
// Extraction samples ~1% of gate rejects filed BY RUNG, so bucketing the score into
// low/mid/high series would divide those already-sparse samples several ways and
// multiply the time to observe the veto's first false-drop -- the exact failure
// PrimeShadow exists to prevent. The distribution's shape lives in the histogram's own
// buckets, where it costs no series on any counter.
//
// It is deliberately NOT primed the way the shadow counters are. Priming a counter
// adds a zero increment, which changes nothing; priming a histogram would mean
// OBSERVING a value nobody measured, and a fabricated 0 is a real point in a
// distribution.
func (m *Metrics) recordPostingScore(ctx context.Context, score float64) {
	m.postingScore.Record(ctx, score)
}

// PrimeShadow creates every Shadow Extraction series at zero, for each of rungs
// crossed with each verdict, plus the shed counter per rung. Call it once at
// start-up, before any sample can land.
//
// It exists because an OTel counter has no series until its first increment, and
// increase() measures growth from the first SCRAPED sample. A counter that appears
// already holding 1 therefore shows an increase of 0: the first shadow accept on a
// rung -- precisely the event this lane exists to catch -- is invisible to every
// range-scoped panel and alert until a second one arrives. Live verification of
// PR #272 hit exactly that, with the raw counter reading one accept while every
// panel read zero. On a fresh deploy, which is when the first post-flip reading is
// taken, that is the difference between "no false-drops" and "one nobody can see".
//
// The rungs are passed in rather than enumerated here so this package keeps knowing
// nothing about the Extract Gate: the composition root supplies pagegate.RejectRungs.
// The cost is a few permanently-zero rows, which also make "this rung has dropped or
// shed nothing" an explicit reading rather than an absent one.
func (m *Metrics) PrimeShadow(ctx context.Context, rungs []string) {
	for _, rung := range rungs {
		for _, verdict := range []ShadowVerdict{ShadowAccept, ShadowAbstain, ShadowError} {
			m.shadow.Add(ctx, 0, metric.WithAttributes(
				attribute.String("verdict", string(verdict)),
				attribute.String("rung", rung),
			))
		}
		m.shadowDropped.Add(ctx, 0, metric.WithAttributes(attribute.String("rung", rung)))
	}
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
