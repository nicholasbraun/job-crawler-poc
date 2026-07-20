package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installManualReader points the process-global meter provider at a fresh
// ManualReader for the duration of the test, restoring the previous provider on
// cleanup. Frontier instruments bind at construction, so New must be called
// AFTER this and the test must be non-parallel — the same discipline as the
// transient-retry sanity check in next_retry_test.go.
func installManualReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
}

// collectFrontier snapshots the current metric set from the manual reader.
func collectFrontier(t *testing.T, reader *sdkmetric.ManualReader) *metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return &rm
}

// popLatencyStats returns the total sample count and the maximum recorded
// millisecond value of crawler.frontier.next.time. The histogram is label-free,
// so its samples aggregate across every run in the collected set.
func popLatencyStats(t *testing.T, rm *metricdata.ResourceMetrics) (count uint64, maxMs float64) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.next.time" {
				continue
			}
			hist, ok := m.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("next.time: unexpected data type %T", m.Data)
			}
			for _, dp := range hist.DataPoints {
				count += dp.Count
				if v, ok := dp.Max.Value(); ok && v > maxMs {
					maxMs = v
				}
			}
		}
	}
	return count, maxMs
}

// domainsSizeValue returns the crawler.frontier.domains.size gauge value carrying
// the given run_id attribute, and whether such a series exists. A gauge is
// last-value, so this is the cardinality of the most recent pop for that run.
func domainsSizeValue(t *testing.T, rm *metricdata.ResourceMetrics, runID string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.domains.size" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("domains.size: unexpected data type %T", m.Data)
			}
			for _, dp := range g.DataPoints {
				if v, ok := dp.Attributes.Value("run_id"); ok && v.AsString() == runID {
					return dp.Value, true
				}
			}
		}
	}
	return 0, false
}

// TestFrontierMetrics drives the Frontier through its public API against a real
// testcontainer Redis and asserts the two pop instruments through an OTEL SDK
// ManualReader (never Lua shape or key names). Non-parallel because the manual
// reader is the process-global meter provider and instruments bind at New.
func TestFrontierMetrics(t *testing.T) {
	reader := installManualReader(t)
	client := newTestClient(t)

	t.Run("next_time records a sample per pop", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		// Drain fully: the URL pop, then MarkDone, then the terminal DONE pop.
		// Every pop-script evaluation records one histogram sample.
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if err := f.MarkDone(t.Context(), got.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		if _, err := f.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Fatalf("Next after drain: got %v, want ErrDone", err)
		}

		if count, _ := popLatencyStats(t, collectFrontier(t, reader)); count == 0 {
			t.Errorf("next.time sample count: got 0, want > 0")
		}
	})

	t.Run("domains_size reflects cardinality and carries run_id", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)
		// Three distinct hosts → schedule cardinality 3.
		for _, u := range []string{"h0", "h1", "h2"} {
			if err := f.AddURL(t.Context(), url(u, "http://"+u+"/1", 0)); err != nil {
				t.Fatalf("AddURL %s: %v", u, err)
			}
		}
		// One pop drains the earliest host's only URL → its domain leaves the
		// schedule → cardinality 2, carried on the URL reply's trailing element.
		if _, err := f.Next(t.Context()); err != nil {
			t.Fatalf("Next: %v", err)
		}

		got, ok := domainsSizeValue(t, collectFrontier(t, reader), runID.String())
		if !ok {
			t.Fatalf("domains.size: no series for run_id %s (run_id label missing)", runID)
		}
		if got != 2 {
			t.Errorf("domains.size: got %d, want 2", got)
		}
	})

	t.Run("invariant: draining returns the schedule toward zero; adding re-schedules", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		// Fully drain: the terminal DONE reply carries cardinality 0.
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if err := f.MarkDone(t.Context(), got.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		if _, err := f.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Fatalf("Next after drain: got %v, want ErrDone", err)
		}
		if v, ok := domainsSizeValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 0 {
			t.Errorf("domains.size after drain: got %d (ok=%v), want 0", v, ok)
		}

		// Two URLs on one domain: a pop leaves one queued, so the domain stays
		// scheduled → cardinality 1, proving re-scheduling on add.
		if err := f.AddURL(t.Context(), url("a", "http://a/2", 0)); err != nil {
			t.Fatalf("AddURL a/2: %v", err)
		}
		if err := f.AddURL(t.Context(), url("a", "http://a/3", 0)); err != nil {
			t.Fatalf("AddURL a/3: %v", err)
		}
		if _, err := f.Next(t.Context()); err != nil {
			t.Fatalf("Next a/2: %v", err)
		}
		if v, ok := domainsSizeValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 1 {
			t.Errorf("domains.size after re-add: got %d (ok=%v), want 1", v, ok)
		}
	})

	t.Run("next_time excludes the WAIT sleep", func(t *testing.T) {
		// Isolate this assertion from sibling subtests. next.time is label-free, so
		// a shared reader's max folds in every prior subtest's pops — a cold
		// warm-up EVAL there could then trip the < 200ms bound and blame this
		// subtest. A fresh reader (with the Frontier built AFTER it, so its
		// instruments rebind) scopes the max to only the pops driven here.
		reader := installManualReader(t)

		// Domain a holds a 400ms cooldown with a second URL queued behind it, so
		// the second Next spends ~400ms in bounded WAIT sleeps before the pop. If
		// the sleep were inside the measured window, a sample near 400ms would
		// appear; asserting every recorded eval stays far below it proves the
		// sleep is outside the window. Local-Redis pop evals are single-digit ms,
		// so 200ms is a wide, robust margin against the 400ms cooldown.
		runID := uuid.New()
		f := redisfrontier.New(client, runID,
			redisfrontier.WithCooldown(400*time.Millisecond),
			redisfrontier.WithPollInterval(50*time.Millisecond),
		)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		if err := f.AddURL(t.Context(), url("a", "http://a/2", 0)); err != nil {
			t.Fatalf("AddURL a/2: %v", err)
		}
		first, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next a/1: %v", err)
		}
		if err := f.MarkDone(t.Context(), first.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		if _, err := f.Next(ctx); err != nil {
			t.Fatalf("Next a/2: %v", err)
		}

		if _, maxMs := popLatencyStats(t, collectFrontier(t, reader)); maxMs >= 200 {
			t.Errorf("next.time max: got %.1fms, want < 200ms (the WAIT sleep leaked into the measured window)", maxMs)
		}
	})
}
