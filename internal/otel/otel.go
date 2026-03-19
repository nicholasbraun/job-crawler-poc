package otel

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func Setup(ctx context.Context) (func(context.Context), error) {
	var shutdownFuncs []func(context.Context) error
	var err error

	// shutdown calls cleanup functions registered via shutdownFuncs.
	// Each registered cleanup will be invoked once.
	shutdown := func(ctx context.Context) {
		for _, fn := range shutdownFuncs {
			fn(ctx)
		}
		shutdownFuncs = nil
	}

	// 1. Create Prometheus exporter
	exporter, err := prometheus.New()
	if err != nil {
		return shutdown, err
	}

	// 2. Create meter provider with the exporter
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))
	shutdownFuncs = append(shutdownFuncs, provider.Shutdown)

	// 3. Set as global
	otel.SetMeterProvider(provider)

	// 4. Serve metrics endpoint (in background)
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		slog.Info("serving metrics at :2223/metrics")
		http.ListenAndServe(":2223", mux)
	}()

	return shutdown, nil
}
