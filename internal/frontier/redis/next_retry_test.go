package redis_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestNextRidesOutTransientErrorThenContextWins is the end-to-end sanity check
// for ADR-0024: Next hitting a dead Redis address gets connection-refused (a
// transient error) on every attempt, so the retry loop keeps trying — never
// surfacing a failure — until the short-deadline run context expires, at which
// point the context bound (not the network error) ends the call. It observes the
// real crawler.frontier.transient_retries counter via an SDK ManualReader.
//
// A dead address (nothing listening) is used rather than a testcontainer so the
// transient error is deterministic and the test needs no Docker daemon. The
// process-global meter provider is set BEFORE constructing the Frontier so its
// counter binds to the manual reader; the test is non-parallel for that reason.
// silentRedisLogger discards go-redis' internal log output; it satisfies the
// (internal) Logging interface structurally.
type silentRedisLogger struct{}

func (silentRedisLogger) Printf(context.Context, string, ...interface{}) {}

// stderrRedisLogger mirrors go-redis' default logger, used to restore normal
// logging after a test silences it.
type stderrRedisLogger struct{ l *log.Logger }

func (s stderrRedisLogger) Printf(_ context.Context, format string, v ...interface{}) {
	_ = s.l.Output(2, fmt.Sprintf(format, v...))
}

func newStderrRedisLogger() stderrRedisLogger {
	return stderrRedisLogger{l: log.New(os.Stderr, "redis: ", log.LstdFlags|log.Lshortfile)}
}

func TestNextRidesOutTransientErrorThenContextWins(t *testing.T) {
	// The deliberately dead address makes go-redis log a "failed to dial" line per
	// attempt; silence its global logger for the duration so the noise does not
	// swamp the test output, restoring a stderr logger afterward.
	redis.SetLogger(silentRedisLogger{})
	t.Cleanup(func() { redis.SetLogger(newStderrRedisLogger()) })

	reader := sdkmetric.NewManualReader()
	// Restore the process-global meter provider afterward so this sanity check
	// does not leak its manual-reader provider into later tests in the package.
	prevMP := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prevMP) })
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	// Nothing listens on 127.0.0.1:1, so every dial is refused. The pool's own
	// dial-retry (DialerRetries, default 5 × ~100ms) is collapsed to a single
	// fast attempt so each Next command surfaces the connection-refused error
	// well before the run-context deadline, driving many outer retries; MaxRetries
	// -1 disables go-redis' command-level retry so the Frontier's loop is the only
	// one in play.
	client := redis.NewClient(&redis.Options{
		Addr:               "127.0.0.1:1",
		DialTimeout:        20 * time.Millisecond,
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
		PoolSize:           1,
		MaxRetries:         -1,
	})
	t.Cleanup(func() { _ = client.Close() })

	f := redisfrontier.New(client, uuid.New(),
		redisfrontier.WithRetryBackoff(1*time.Millisecond, 5*time.Millisecond))

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	if _, err := f.Next(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Next: got %v, want context.DeadlineExceeded", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}

	if got := transientRetryCount(t, &rm, "next"); got == 0 {
		t.Errorf("transient_retries counter (op=next): got 0, want > 0")
	}
}

// transientRetryCount extracts the summed crawler.frontier.transient_retries
// value carrying the given op attribute from a collected metric set, or 0 if
// absent. Shared across the redis_test package's retry tests (next/add/done).
func transientRetryCount(t *testing.T, rm *metricdata.ResourceMetrics, op string) int64 {
	t.Helper()
	var total int64
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.transient_retries" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("transient_retries: unexpected data type %T", m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("op"); ok && v.AsString() == op {
					total += dp.Value
				}
			}
		}
	}
	return total
}
