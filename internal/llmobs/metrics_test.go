package llmobs_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// installLLMReader points the process-global meter provider at a fresh ManualReader
// for the duration of the test, restoring the previous provider on cleanup. LLM
// instruments bind at construction, so NewMetrics must be called AFTER this and the
// test must be non-parallel — the same manual-reader discipline the collection and
// frontier metrics tests use.
func installLLMReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))
	return reader
}

// llmMetric returns the named instrument on the "llm" scope, or nil when the
// instrument recorded nothing (an untouched counter is never exported). The manual
// reader reports the raw dotted name; Prometheus exports crawler.llm.shadow as
// crawler_llm_shadow_total.
func llmMetric(rm *metricdata.ResourceMetrics, name string) *metricdata.Metrics {
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "llm" {
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

// TestMetricsShadowIsCountedSeparatelyFromCalls closes the loop from Recorder.Shadow
// to the exported instrument, and pins the separation ADR-0044 rests on: measurement
// spend lands on crawler.llm.shadow, labelled by verdict, and never on
// crawler.llm.calls — folding it in would corrupt the extract call rate the Extract
// Gate is judged by. Non-parallel: the manual reader is the process-global meter
// provider and instruments bind at NewMetrics.
func TestMetricsShadowIsCountedSeparatelyFromCalls(t *testing.T) {
	reader := installLLMReader(t)
	metrics := llmobs.NewMetrics()
	rec := llmobs.NewRecorder(metrics, nil, nil, "")

	ctx := t.Context()
	rec.Shadow(ctx, llmobs.ShadowAccept)
	rec.Shadow(ctx, llmobs.ShadowAccept)
	rec.Shadow(ctx, llmobs.ShadowAbstain)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	shadow := llmMetric(&rm, "crawler.llm.shadow")
	if shadow == nil {
		t.Fatal("crawler.llm.shadow instrument not found on the llm scope")
	}
	sum, ok := shadow.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("crawler.llm.shadow: unexpected data type %T", shadow.Data)
	}

	byVerdict := map[string]int64{}
	for _, dp := range sum.DataPoints {
		v, present := dp.Attributes.Value("verdict")
		if !present {
			t.Errorf("a shadow data point carries no verdict attribute: %v", dp.Attributes)
			continue
		}
		byVerdict[v.Emit()] += dp.Value
	}
	want := map[string]int64{"accept": 2, "abstain": 1}
	for verdict, n := range want {
		if byVerdict[verdict] != n {
			t.Errorf("shadow[verdict=%s] = %d, want %d (all: %v)", verdict, byVerdict[verdict], n, byVerdict)
		}
	}

	// The load-bearing assertion: a Shadow Extraction is real spend, but the call
	// counter must keep meaning "calls the Extract Gate admitted".
	if calls := llmMetric(&rm, "crawler.llm.calls"); calls != nil {
		if callSum, ok := calls.Data.(metricdata.Sum[int64]); ok && len(callSum.DataPoints) > 0 {
			t.Errorf("crawler.llm.calls moved on a Shadow Extraction: %v", callSum.DataPoints)
		}
	}
}
