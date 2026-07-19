package redis

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// opNext labels the transient-retry metric/log for Frontier.Next. It is a
// low-cardinality value (no run_id), so a Grafana series never explodes per run.
const opNext = "next"

// opAdd and opDone are the sibling low-cardinality op labels to opNext, stamping
// the transient-retry metric/log for Frontier.AddURL and Frontier.MarkDone
// respectively (no run_id, so a Grafana series never explodes per run).
const (
	opAdd  = "add"
	opDone = "done"
)

// WithRetryBackoff sets the transient-error retry backoff bounds: the first
// retry waits min, doubling to the max cap, then holds at max. Both are reused
// by the Frontier's jittered, context-aware sleep, so min/max are pre-jitter
// bounds. Defaults are 100ms / 5s; tests shrink them to drive the loop without
// burning wall-clock.
func WithRetryBackoff(min, max time.Duration) Option {
	return func(f *Frontier) { f.retryMin, f.retryMax = min, max }
}

// newTransientRetryCounter registers the transient-retry counter under the
// "frontier" meter scope. A registration error is logged and the returned
// (non-nil no-op) instrument is still used, so a metrics hiccup never breaks a
// crawl — mirroring the llmobs meter pattern.
func newTransientRetryCounter() metric.Int64Counter {
	c, err := otel.Meter("frontier").Int64Counter(
		"crawler.frontier.transient_retries",
		metric.WithDescription("Frontier Redis operations retried after a transient error (ADR-0024)."),
	)
	if err != nil {
		slog.Error("frontier: error setting up transient-retry counter", "err", err)
	}
	return c
}

// isTransient reports whether err is a recoverable Redis disruption the Frontier
// should ride out (ADR-0024). It is an allowlist, generous on network shapes:
// any net.Error (i/o timeout, OpError-wrapped connection refused/reset), EOF,
// bare connection refused/reset/broken-pipe syscalls, the go-redis connection-
// pool timeout, and the known temporarily-unavailable server replies. BUSY is
// one of those: Redis returns it while a Lua script runs past lua-time-limit,
// rejecting all other commands until that script finishes -- the rejected
// command never ran, so retrying once the script clears is safe. Any OTHER Redis
// *reply* (e.g. WRONGTYPE, a Lua error) is deterministic and therefore fatal. A
// cancelled/deadline-exceeded run context is intentional, not transient, and is
// excluded up front (context.DeadlineExceeded also satisfies net.Error).
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) {
		return true
	}
	if errors.Is(err, redis.ErrPoolTimeout) {
		return true
	}
	for _, prefix := range []string{"LOADING", "READONLY", "CLUSTERDOWN", "MASTERDOWN", "TRYAGAIN", "BUSY"} {
		if redis.HasErrorPrefix(err, prefix) {
			return true
		}
	}
	return false
}

// withRetry runs a Frontier Redis operation, absorbing Transient Frontier Errors
// (ADR-0024) by retrying with capped, jittered exponential backoff for as long
// as ctx is live. op is a low-cardinality label (e.g. opNext) stamped on the
// retry metric and log. It returns fn's result on success; the context error if
// ctx is cancelled — which wins over any error shape, so an intentional
// Stop/Shutdown that surfaced as an i/o timeout (#32) is never retried; or a
// fatal (non-transient) error unchanged. The only bound is ctx: a permanently
// unreachable Redis leaves the caller retrying (a "running-but-stalled" run),
// never failing.
func (f *Frontier) withRetry(ctx context.Context, op string, fn func() (any, error)) (any, error) {
	backoff := f.retryMin
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := fn()
		if err == nil {
			return res, nil
		}
		// A cancelled context wins over the error's shape: a Stop/Shutdown that
		// aborted the in-flight read (surfacing as an i/o timeout, #32) must end
		// the run, not retry.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		if !isTransient(err) {
			return nil, err
		}
		slog.Warn("frontier: transient redis error, retrying",
			"err", err, "op", op, "frontier", f.keyPrefix)
		f.retries.Add(ctx, 1, metric.WithAttributes(attribute.String("op", op)))
		if serr := f.sleep(ctx, backoff); serr != nil {
			return nil, serr
		}
		if backoff *= 2; backoff > f.retryMax {
			backoff = f.retryMax
		}
	}
}
