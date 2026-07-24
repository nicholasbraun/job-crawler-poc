package collection_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installCollectionReader points the process-global meter provider at a fresh
// ManualReader for the duration of the test, restoring the previous provider on
// cleanup. Collection instruments bind at construction, so NewMetrics must be
// called AFTER this and the test must be non-parallel — the same discipline as
// the frontier metrics_test.go manual-reader idiom.
func installCollectionReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
}

// robotsBlockedCount sums the DataPoints of the collection.refetch.robots_blocked
// counter on the "collection" scope. The manual reader reports the raw dotted
// instrument name (no Prometheus _total suffix). Fails if the instrument is
// absent — a missing tap or a renamed instrument is a defect, not a zero.
func robotsBlockedCount(t *testing.T, rm *metricdata.ResourceMetrics) int64 {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "collection" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "collection.refetch.robots_blocked" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("robots_blocked: unexpected data type %T", m.Data)
			}
			var total int64
			for _, dp := range sum.DataPoints {
				total += dp.Value
			}
			return total
		}
	}
	t.Fatal("collection.refetch.robots_blocked instrument not found on the collection scope")
	return 0
}

// TestMetricsRobotsBlocked asserts RobotsBlocked moves the
// collection.refetch.robots_blocked counter, closing the loop from #238's
// onRobotsBlocked hook to the exported instrument. Non-parallel: the manual
// reader is the process-global meter provider and instruments bind at
// NewMetrics, so it must run after the reader is installed.
func TestMetricsRobotsBlocked(t *testing.T) {
	reader := installCollectionReader(t)
	m := collection.NewMetrics()

	m.RobotsBlocked(t.Context())
	m.RobotsBlocked(t.Context())
	m.RobotsBlocked(t.Context())

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	if got := robotsBlockedCount(t, &rm); got != 3 {
		t.Errorf("robots_blocked count: got %d, want 3", got)
	}
}
