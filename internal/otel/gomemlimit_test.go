package otel

import (
	"runtime/debug"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// TestGoMemLimitGauge pins the Prometheus series name and value the dashboard
// binds to. It mirrors Setup's wiring (OTel Prometheus exporter -> meter
// provider) but against a private registry so it can Gather without binding a
// port or touching the global provider. If the OTel->Prometheus name/unit
// translation ever changes crawler_runtime_go_memory_limit_bytes, the
// heap-vs-limit panel breaks silently in Grafana but this test fails loudly.
func TestGoMemLimitGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	exporter, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		t.Fatalf("new prometheus exporter: %v", err)
	}
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	registerGoMemLimitGauge(provider.Meter("runtime"))

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	const want = "crawler_runtime_go_memory_limit_bytes"
	var got *dto.MetricFamily
	for _, mf := range mfs {
		if mf.GetName() == want {
			got = mf
			break
		}
	}
	if got == nil {
		names := make([]string, 0, len(mfs))
		for _, mf := range mfs {
			names = append(names, mf.GetName())
		}
		t.Fatalf("series %q not exported; got families %v", want, names)
	}

	if got.GetType() != dto.MetricType_GAUGE {
		t.Errorf("series %q is %v, want GAUGE", want, got.GetType())
	}

	metrics := got.GetMetric()
	if len(metrics) != 1 {
		t.Fatalf("series %q has %d samples, want 1", want, len(metrics))
	}
	// debug.SetMemoryLimit(-1) reads the current limit without changing it; the
	// gauge must report exactly that.
	wantVal := float64(debug.SetMemoryLimit(-1))
	if gotVal := metrics[0].GetGauge().GetValue(); gotVal != wantVal {
		t.Errorf("series %q = %g, want %g (debug.SetMemoryLimit(-1))", want, gotVal, wantVal)
	}
}
