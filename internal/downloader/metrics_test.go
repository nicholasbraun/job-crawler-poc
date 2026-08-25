package downloader_test

import (
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installHTTPClientReader points the process-global meter provider at a fresh
// ManualReader for the duration of the test, restoring the previous provider on
// cleanup. The retry client's instruments bind at construction, so NewRetryClient
// must be called AFTER this and the test must be non-parallel — the same
// manual-reader discipline the llmobs, collection and frontier metrics tests use.
func installHTTPClientReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
}

// httpClientMetric returns the named instrument on the "http_client" scope, or nil
// when it recorded nothing — an untouched counter is never exported. The manual
// reader reports the raw dotted name; Prometheus exports
// crawler.http-client.retries.abandoned as crawler_http_client_retries_abandoned_total,
// which is the name the downloader dashboard queries.
func httpClientMetric(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "http_client" {
			continue
		}
		for i, m := range sm.Metrics {
			if m.Name == name {
				return &sm.Metrics[i]
			}
		}
	}
	return nil
}

// TestRetriesAbandonedCounter closes the loop from a Throttle Abandonment to the
// exported instrument, and pins the two properties ADR-0054 rests on: the series is
// labelled by the status that carried the unhonourable hint, and an exhaustion is
// NOT counted. The second is what lets the number be read as requests saved rather
// than as a rename of the exhaustion it replaces — an abandonment on the final
// attempt would have saved nothing.
//
// It matters more here than instrument coverage usually would: ADR-0054 leaves the
// ERROR volume deliberately unchanged, so the logs cannot distinguish "the change
// never fires" from "no host sent an over-ceiling hint". This counter is the only
// signal that separates them, and a flat panel is what a silently-broken Add looks
// like. Non-parallel: the manual reader is the process-global meter provider.
func TestRetriesAbandonedCounter(t *testing.T) {
	reader := installHTTPClientReader(t)

	// One abandonment: a 429 whose hint sits far above the ceiling.
	abandoning := downloader.NewRetryClient(
		&mockDownloader{
			responses: []*downloader.Response{nil},
			errors:    []error{&downloader.StatusError{StatusCode: 429, Retryable: true, RetryAfter: time.Hour}},
		},
		downloader.WithMaxBackoff(30*time.Second),
	)
	if _, err := abandoning.Get(t.Context(), "http://something.de"); err == nil {
		t.Fatal("expected a hint above the ceiling to end the fetch")
	}

	// ...and an exhaustion carrying an equally over-long hint, on a single-attempt
	// client so the hint arrives where there is nothing left to abandon. It must
	// leave no mark on the counter.
	exhausting := downloader.NewRetryClient(
		&mockDownloader{
			responses: []*downloader.Response{nil},
			errors:    []error{&downloader.StatusError{StatusCode: 503, Retryable: true, RetryAfter: time.Hour}},
		},
		downloader.WithMaxTries(1), downloader.WithMaxBackoff(30*time.Second),
	)
	if _, err := exhausting.Get(t.Context(), "http://something.de"); err == nil {
		t.Fatal("expected the single attempt to exhaust")
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	abandoned := httpClientMetric(&rm, "crawler.http-client.retries.abandoned")
	if abandoned == nil {
		t.Fatal("crawler.http-client.retries.abandoned instrument not found on the http_client scope")
	}
	sum, ok := abandoned.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("crawler.http-client.retries.abandoned: unexpected data type %T", abandoned.Data)
	}

	byStatus := map[string]int64{}
	for _, dp := range sum.DataPoints {
		status, present := dp.Attributes.Value("status")
		if !present {
			t.Errorf("an abandonment data point carries no status attribute: %v", dp.Attributes)
			continue
		}
		// Host must never reach the attribute set: it would mint one series per
		// crawled host (ADR-0054). The offending host is in the log line instead.
		if _, present := dp.Attributes.Value("host"); present {
			t.Errorf("abandonment data point carries an unbounded host attribute: %v", dp.Attributes)
		}
		byStatus[status.Emit()] += dp.Value
	}

	want := map[string]int64{"429": 1}
	if len(byStatus) != len(want) {
		t.Errorf("abandonments by status = %v, want %v (an exhaustion must not be counted)", byStatus, want)
	}
	for status, n := range want {
		if byStatus[status] != n {
			t.Errorf("abandonments[status=%s] = %d, want %d (all: %v)", status, byStatus[status], n, byStatus)
		}
	}
}
