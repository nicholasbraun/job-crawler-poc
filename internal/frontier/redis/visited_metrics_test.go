package redis_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// visitedSizeValue returns the crawler.frontier.visited.size gauge value for the
// given run_id and whether such a series exists. A gauge is last-value, so this
// is the post-eviction cardinality recorded by that run's most recent NEW insert.
func visitedSizeValue(t *testing.T, rm *metricdata.ResourceMetrics, runID string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.visited.size" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("visited.size: unexpected data type %T", m.Data)
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

// visitedEvictedValue returns the summed crawler.frontier.visited.evicted counter
// value for the given run_id and whether such a series exists. The series exists
// (at 0) as soon as one NEW insert has recorded for that run, so its presence
// proves the counter is wired even when the cap never fired.
func visitedEvictedValue(t *testing.T, rm *metricdata.ResourceMetrics, runID string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.visited.evicted" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("visited.evicted: unexpected data type %T", m.Data)
			}
			var total int64
			found := false
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("run_id"); ok && v.AsString() == runID {
					total += dp.Value
					found = true
				}
			}
			if found {
				return total, true
			}
		}
	}
	return 0, false
}

// visitedCapValue returns the crawler.frontier.visited.cap gauge value for the
// given run_id and whether such a series exists. A gauge is last-value; the cap
// is static per run, so every NEW insert records the same configured ceiling.
func visitedCapValue(t *testing.T, rm *metricdata.ResourceMetrics, runID string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.visited.cap" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("visited.cap: unexpected data type %T", m.Data)
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

// TestVisitedMetrics drives the Frontier through its public API against a real
// testcontainer Redis and asserts the two visited instruments through an OTEL SDK
// ManualReader (never Lua shape or key names). Non-parallel because the manual
// reader is the process-global meter provider and instruments bind at New; the
// reader is installed once and every Frontier is built after it, with subtests
// isolated by distinct run_id (both instruments are run_id-labeled).
func TestVisitedMetrics(t *testing.T) {
	reader := installManualReader(t)
	client := newTestClient(t)

	t.Run("new insert records size and zero evictions, run_id-labeled", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		rm := collectFrontier(t, reader)
		if v, ok := visitedSizeValue(t, rm, runID.String()); !ok || v != 1 {
			t.Errorf("visited.size: got %d (ok=%v), want 1", v, ok)
		}
		// The run_id series exists at 0, proving the counter is wired and the cap
		// never fired for this run.
		if v, ok := visitedEvictedValue(t, rm, runID.String()); !ok || v != 0 {
			t.Errorf("visited.evicted: got %d (ok=%v), want 0", v, ok)
		}
	})

	t.Run("dup insert records nothing", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		if err := f.AddURL(t.Context(), url("b", "http://b/1", 0)); err != nil {
			t.Fatalf("AddURL b/1: %v", err)
		}
		// Re-adding a resident URL must return nil (the bare 'DUP' reply is still
		// accepted, not surfaced as an "unexpected result" error).
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1 repeat: %v", err)
		}

		rm := collectFrontier(t, reader)
		// The DUP did not overwrite the gauge to a bogus post-DUP value.
		if v, ok := visitedSizeValue(t, rm, runID.String()); !ok || v != 2 {
			t.Errorf("visited.size after DUP: got %d (ok=%v), want 2", v, ok)
		}
		if v, ok := visitedEvictedValue(t, rm, runID.String()); !ok || v != 0 {
			t.Errorf("visited.evicted after DUP: got %d (ok=%v), want 0", v, ok)
		}
	})

	t.Run("eviction increments evicted by the overflow, run_id-labeled", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID, redisfrontier.WithVisitedCap(3))

		raws := []string{"http://h0/1", "http://h1/1", "http://h2/1", "http://h3/1", "http://h4/1"}
		hosts := []string{"h0", "h1", "h2", "h3", "h4"}
		for i, raw := range raws {
			if i > 0 {
				time.Sleep(2 * time.Millisecond) // strictly increasing scores => deterministic FIFO
			}
			if err := f.AddURL(t.Context(), url(hosts[i], raw, 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}

		rm := collectFrontier(t, reader)
		// Adds #4 and #5 each evicted one entry past the cap of 3, summed on the
		// counter.
		if v, ok := visitedEvictedValue(t, rm, runID.String()); !ok || v != 2 {
			t.Errorf("visited.evicted: got %d (ok=%v), want 2", v, ok)
		}
		// The gauge is pinned at the cap = post-eviction ZCARD.
		if v, ok := visitedSizeValue(t, rm, runID.String()); !ok || v != 3 {
			t.Errorf("visited.size: got %d (ok=%v), want 3", v, ok)
		}
	})

	t.Run("cap gauge reflects the configured per-run cap, run_id-labeled", func(t *testing.T) {
		// A non-default cap proves the gauge tracks the effective per-run visitedCap
		// rather than the fixed DefaultVisitedCap the dashboard used to hard-code.
		runID := uuid.New()
		const wantCap = 1_000_000
		f := redisfrontier.New(client, runID, redisfrontier.WithVisitedCap(wantCap))
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		rm := collectFrontier(t, reader)
		if v, ok := visitedCapValue(t, rm, runID.String()); !ok || v != wantCap {
			t.Errorf("visited.cap: got %d (ok=%v), want %d", v, ok, wantCap)
		}
	})
}
