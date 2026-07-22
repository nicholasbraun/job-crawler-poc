package otel

import (
	"context"
	"log/slog"
	"runtime/debug"

	"go.opentelemetry.io/otel/metric"
)

// registerGoMemLimitGauge registers crawler.runtime.go.memory_limit, an
// observable gauge reporting the Go soft memory limit (GOMEMLIMIT) in bytes.
//
// The client_golang Go/process collectors already scraped by Prometheus expose
// heap, goroutines, GC, RSS, CPU, and FDs, but none of them export GOMEMLIMIT —
// and it, not the container mem_limit, is the ceiling the garbage collector
// targets and a climbing heap approaches before the kernel OOM-kills the crawler
// (see ADR-0032, ADR-0033). It is read at each collection via
// debug.SetMemoryLimit(-1), which returns the current limit without changing it
// (math.MaxInt64 when GOMEMLIMIT is unset). The value is effectively constant
// per process — set once from the GOMEMLIMIT env — but an observable gauge reads
// it live so the series stays correct even if the limit is ever adjusted at
// runtime.
//
// It reaches Prometheus as crawler_runtime_go_memory_limit_bytes: the "By" unit
// adds the _bytes suffix, mirroring how "ms" adds _milliseconds on the existing
// histograms. That series pairs with the collectors' go_memstats_heap_inuse_bytes
// on the system dashboard's heap-vs-limit panel. Log-and-continue on error: OTel
// returns a non-nil no-op instrument, so a metrics registration hiccup never
// blocks startup.
func registerGoMemLimitGauge(meter metric.Meter) {
	_, err := meter.Int64ObservableGauge(
		"crawler.runtime.go.memory_limit",
		metric.WithUnit("By"),
		metric.WithDescription("Go soft memory limit (GOMEMLIMIT) in bytes via debug.SetMemoryLimit(-1); math.MaxInt64 when unset. The ceiling the GC targets and the heap approaches before OOM (ADR-0033)."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(debug.SetMemoryLimit(-1))
			return nil
		}),
	)
	if err != nil {
		slog.Error("otel: error setting up GOMEMLIMIT gauge", "err", err)
	}
}
