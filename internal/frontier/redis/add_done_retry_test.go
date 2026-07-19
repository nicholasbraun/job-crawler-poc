package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// TestAddAndDoneRideOutTransientErrorThenContextWins is the end-to-end sanity
// check for ADR-0024 on AddURL and MarkDone (the #142 siblings of the Next test):
// each op hitting a dead Redis address gets connection-refused (a transient
// error) on every attempt, so the retry loop keeps trying — never surfacing a
// failure — until the short-deadline run context expires, at which point the
// context bound (not the network error) ends the call. It observes the real
// crawler.frontier.transient_retries counter, per op label, via an SDK
// ManualReader.
//
// A dead address (nothing listening) is used rather than a testcontainer so the
// transient error is deterministic and the test needs no Docker daemon. The
// process-global meter provider is set BEFORE constructing the Frontier so its
// counter binds to the manual reader; the test is non-parallel for that reason.
func TestAddAndDoneRideOutTransientErrorThenContextWins(t *testing.T) {
	// The deliberately dead address makes go-redis log a "failed to dial" line
	// per attempt; silence its global logger for the duration so the noise does
	// not swamp the test output, restoring a stderr logger afterward.
	redis.SetLogger(silentRedisLogger{})
	t.Cleanup(func() { redis.SetLogger(newStderrRedisLogger()) })

	reader := sdkmetric.NewManualReader()
	otel.SetMeterProvider(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)))

	// Nothing listens on 127.0.0.1:1, so every dial is refused. The pool's own
	// dial-retry is collapsed to a single fast attempt so each command surfaces
	// connection-refused well before the run-context deadline, driving many outer
	// retries; MaxRetries -1 disables go-redis' command-level retry so the
	// Frontier's loop is the only one in play.
	client := redis.NewClient(&redis.Options{
		Addr:               "127.0.0.1:1",
		DialTimeout:        20 * time.Millisecond,
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
		PoolSize:           1,
		MaxRetries:         -1,
	})
	t.Cleanup(func() { _ = client.Close() })

	// Construct the Frontier AFTER the meter provider so its counter binds to the
	// manual reader above; New calls newTransientRetryCounter() at construction.
	f := redisfrontier.New(client, uuid.New(),
		redisfrontier.WithRetryBackoff(1*time.Millisecond, 5*time.Millisecond))

	t.Run("add", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		// Depth 0 <= default maxDepth (3), so the maxDepth guard is not hit and
		// the call reaches the retried script.
		url := crawler.URL{Hostname: "example.com", RawURL: "http://example.com/jobs", Depth: 0}
		if err := f.AddURL(ctx, url); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("AddURL: got %v, want context.DeadlineExceeded", err)
		}

		if got := collectRetryCount(t, reader, "add"); got == 0 {
			t.Errorf("transient_retries counter (op=add): got 0, want > 0")
		}
	})

	t.Run("done", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()

		if err := f.MarkDone(ctx, "http://example.com/jobs"); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("MarkDone: got %v, want context.DeadlineExceeded", err)
		}

		if got := collectRetryCount(t, reader, "done"); got == 0 {
			t.Errorf("transient_retries counter (op=done): got 0, want > 0")
		}
	})
}

// collectRetryCount reads the manual reader and returns the summed
// transient_retries value carrying the given op label.
func collectRetryCount(t *testing.T, reader *sdkmetric.ManualReader, op string) int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collecting metrics: %v", err)
	}
	return transientRetryCount(t, &rm, op)
}
