package llmobs

import (
	"context"
	"log/slog"
	"time"
)

// Recorder is the per-run façade the LLM-stage call sites use to record their
// activity. It fans one call out to the shared Prometheus Metrics and the
// per-run Stats (and, for content, the Redis DupProbe). A Recorder is cheap and
// built per run; Nop returns one that records nothing (tests, or when a
// processor is wired without instrumentation).
type Recorder interface {
	// Call records one completed LLM call: its kind, outcome, and wall-clock
	// duration.
	Call(ctx context.Context, kind Kind, outcome Outcome, dur time.Duration)
	// Gated records a page a cheap gate resolved without an LLM call, and the
	// reason it short-circuited.
	Gated(ctx context.Context, kind Kind, reason Reason)
	// Content records the page content about to be fed to the LLM, measuring how
	// often identical content recurs (the duplicate-content probe).
	Content(ctx context.Context, kind Kind, content string)
	// Retry records a durable-stage task that was redelivered for another attempt
	// after a failed processing (a reclaimed pending entry).
	Retry(ctx context.Context, kind Kind)
	// DeadLetter records a durable-stage task that exhausted its attempts and was
	// moved to the dead-letter stream.
	DeadLetter(ctx context.Context, kind Kind)
	// QueueDepth records the current durable-stage backlog (total outstanding
	// stream entries) and pending (delivered-but-unacked) counts for a kind. The
	// gauges are additionally scoped by the recorder's run so concurrent runs do
	// not clobber one another's series.
	QueueDepth(ctx context.Context, kind Kind, backlog, pending int64)
}

type recorder struct {
	metrics *Metrics
	dup     *DupProbe
	stats   *Stats
	// runID scopes this run's queue-depth gauges so concurrent runs get distinct
	// series instead of overwriting a shared {kind} one.
	runID string
}

// NewRecorder builds a per-run Recorder over the shared metrics and dup probe
// and this run's stats. runID scopes the queue-depth gauges to this run. Any of
// metrics/dup/stats may be nil (each fan-out is guarded), which is how
// partially-configured setups degrade gracefully.
func NewRecorder(metrics *Metrics, dup *DupProbe, stats *Stats, runID string) Recorder {
	return &recorder{metrics: metrics, dup: dup, stats: stats, runID: runID}
}

func (r *recorder) Call(ctx context.Context, kind Kind, outcome Outcome, dur time.Duration) {
	if r.metrics != nil {
		r.metrics.recordCall(ctx, kind, outcome, float64(dur)/float64(time.Millisecond))
	}
	if r.stats != nil {
		r.stats.recordCall(kind, outcome)
	}
}

func (r *recorder) Gated(ctx context.Context, kind Kind, reason Reason) {
	if r.metrics != nil {
		r.metrics.recordGated(ctx, kind, reason)
	}
	if r.stats != nil {
		r.stats.recordGated(kind)
	}
}

func (r *recorder) Content(ctx context.Context, kind Kind, content string) {
	duplicate, err := r.dup.Observe(ctx, kind, content)
	if err != nil {
		slog.Error("llmobs: error probing content duplication", "err", err, "kind", kind)
		duplicate = false
	}
	if r.metrics != nil {
		r.metrics.recordContent(ctx, kind, duplicate)
	}
	if r.stats != nil {
		r.stats.recordContent(kind, duplicate)
	}
}

func (r *recorder) Retry(ctx context.Context, kind Kind) {
	if r.metrics != nil {
		r.metrics.recordRetry(ctx, kind)
	}
	if r.stats != nil {
		r.stats.recordRetry(kind)
	}
}

func (r *recorder) DeadLetter(ctx context.Context, kind Kind) {
	if r.metrics != nil {
		r.metrics.recordDeadLetter(ctx, kind)
	}
	if r.stats != nil {
		r.stats.recordDeadLetter(kind)
	}
}

func (r *recorder) QueueDepth(ctx context.Context, kind Kind, backlog, pending int64) {
	if r.metrics != nil {
		r.metrics.recordQueueDepth(ctx, kind, r.runID, backlog, pending)
	}
}

// Nop returns a Recorder that ignores everything, so call sites in tests (or a
// processor built without a recorder) need no nil checks.
func Nop() Recorder { return nopRecorder{} }

type nopRecorder struct{}

func (nopRecorder) Call(context.Context, Kind, Outcome, time.Duration) {}
func (nopRecorder) Gated(context.Context, Kind, Reason)                {}
func (nopRecorder) Content(context.Context, Kind, string)              {}
func (nopRecorder) Retry(context.Context, Kind)                        {}
func (nopRecorder) DeadLetter(context.Context, Kind)                   {}
func (nopRecorder) QueueDepth(context.Context, Kind, int64, int64)     {}
