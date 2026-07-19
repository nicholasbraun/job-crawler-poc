package redis

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/embedded"
)

// countingCounter is an inline metric.Int64Counter fake: it records the total
// increment and every "op" attribute value, so a synctest test can assert the
// retry metric without a real meter provider. Embedding embedded.Int64Counter
// satisfies the sealed interface.
type countingCounter struct {
	embedded.Int64Counter
	mu    sync.Mutex
	count int64
	ops   []string
}

func (c *countingCounter) Add(_ context.Context, incr int64, opts ...metric.AddOption) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count += incr
	set := metric.NewAddConfig(opts).Attributes()
	if v, ok := set.Value(attribute.Key("op")); ok {
		c.ops = append(c.ops, v.AsString())
	}
}

func (*countingCounter) Enabled(context.Context) bool { return true }

func (c *countingCounter) total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func (c *countingCounter) opValues() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.ops...)
}

// retryFrontier builds a client-free Frontier wired to counter with the given
// backoff bounds; withRetry never touches the (nil) client, so this exercises
// the loop in isolation.
func retryFrontier(counter metric.Int64Counter, min, max time.Duration) *Frontier {
	f := New(nil, uuid.New(), WithRetryBackoff(min, max))
	f.retries = counter
	return f
}

// redisReplyErr is a fake go-redis reply error: a distinct Redis error type
// (RedisError marks it) so isTransient can classify it via HasErrorPrefix
// without importing go-redis internals.
type redisReplyErr string

func (e redisReplyErr) Error() string { return string(e) }
func (redisReplyErr) RedisError()     {}

func TestIsTransient(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"opError conn reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}, true},
		{"i/o deadline exceeded", os.ErrDeadlineExceeded, true},
		{"connection refused", syscall.ECONNREFUSED, true},
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"broken pipe", syscall.EPIPE, true},
		{"pool timeout", redis.ErrPoolTimeout, true},
		{"loading reply", redisReplyErr("LOADING Redis is loading the dataset in memory"), true},
		{"readonly reply", redisReplyErr("READONLY You can't write against a read only replica."), true},
		{"clusterdown reply", redisReplyErr("CLUSTERDOWN The cluster is down"), true},
		{"masterdown reply", redisReplyErr("MASTERDOWN Link with MASTER is down"), true},
		{"tryagain reply", redisReplyErr("TRYAGAIN Multiple keys request during rehashing"), true},
		{"busy reply", redisReplyErr("BUSY Redis is busy running a script. You can only call SCRIPT KILL or SHUTDOWN NOSAVE."), true},
		{"wrongtype reply is fatal", redisReplyErr("WRONGTYPE Operation against a key holding the wrong kind of value"), false},
		{"context canceled", context.Canceled, false},
		{"context deadline", context.DeadlineExceeded, false},
		{"plain error", errors.New("boom"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isTransient(tt.err); got != tt.want {
				t.Errorf("isTransient(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestWithRetry(t *testing.T) {
	t.Run("transient N then success returns value and counts N", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			counter := &countingCounter{}
			f := retryFrontier(counter, 10*time.Millisecond, 100*time.Millisecond)

			const transientN = 3
			calls := 0
			fn := func() (any, error) {
				calls++
				if calls <= transientN {
					return nil, io.EOF
				}
				return "ok", nil
			}

			res, err := f.withRetry(t.Context(), opNext, fn)
			if err != nil {
				t.Fatalf("withRetry: unexpected error %v", err)
			}
			if res != "ok" {
				t.Errorf("result: got %v, want %q", res, "ok")
			}
			if got := counter.total(); got != transientN {
				t.Errorf("retry count: got %d, want %d", got, transientN)
			}
			for i, op := range counter.opValues() {
				if op != opNext {
					t.Errorf("op[%d]: got %q, want %q", i, op, opNext)
				}
			}
		})
	})

	t.Run("records op label add and done on retries", func(t *testing.T) {
		for _, op := range []string{opAdd, opDone} {
			t.Run(op, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					counter := &countingCounter{}
					f := retryFrontier(counter, 10*time.Millisecond, 100*time.Millisecond)

					const transientN = 2
					calls := 0
					fn := func() (any, error) {
						calls++
						if calls <= transientN {
							return nil, io.EOF
						}
						return "ok", nil
					}

					res, err := f.withRetry(t.Context(), op, fn)
					if err != nil {
						t.Fatalf("withRetry: unexpected error %v", err)
					}
					if res != "ok" {
						t.Errorf("result: got %v, want %q", res, "ok")
					}
					if got := counter.total(); got != transientN {
						t.Errorf("retry count: got %d, want %d", got, transientN)
					}
					for i, gotOp := range counter.opValues() {
						if gotOp != op {
							t.Errorf("op[%d]: got %q, want %q", i, gotOp, op)
						}
					}
				})
			})
		}
	})

	t.Run("fatal error surfaces immediately with no retry", func(t *testing.T) {
		counter := &countingCounter{}
		f := retryFrontier(counter, 10*time.Millisecond, 100*time.Millisecond)

		fatal := redisReplyErr("WRONGTYPE Operation against a key holding the wrong kind of value")
		calls := 0
		fn := func() (any, error) {
			calls++
			return nil, fatal
		}

		_, err := f.withRetry(t.Context(), opNext, fn)
		if !errors.Is(err, fatal) {
			t.Errorf("error: got %v, want %v", err, fatal)
		}
		if calls != 1 {
			t.Errorf("fn calls: got %d, want 1", calls)
		}
		if got := counter.total(); got != 0 {
			t.Errorf("retry count: got %d, want 0", got)
		}
	})

	t.Run("context cancelled mid-backoff returns promptly", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			counter := &countingCounter{}
			f := retryFrontier(counter, 50*time.Millisecond, 500*time.Millisecond)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			fn := func() (any, error) { return nil, io.EOF }

			var (
				gotErr error
				gotRes any
			)
			done := make(chan struct{})
			go func() {
				gotRes, gotErr = f.withRetry(ctx, opNext, fn)
				close(done)
			}()

			// Let the goroutine reach the durable sleep between retries, then
			// cancel: the loop must return the context error, not a success.
			synctest.Wait()
			cancel()
			synctest.Wait()

			select {
			case <-done:
			default:
				t.Fatal("withRetry did not return after cancellation")
			}
			if !errors.Is(gotErr, context.Canceled) {
				t.Errorf("error: got %v, want context.Canceled", gotErr)
			}
			if gotRes != nil {
				t.Errorf("result: got %v, want nil", gotRes)
			}
		})
	})

	t.Run("backoff doubles and caps", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			counter := &countingCounter{}
			f := retryFrontier(counter, 100*time.Millisecond, 400*time.Millisecond)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			start := time.Now()
			var offsets []time.Duration
			fn := func() (any, error) {
				offsets = append(offsets, time.Since(start))
				if len(offsets) == 5 {
					cancel() // end the loop after enough gaps to observe the plateau
				}
				return nil, io.EOF
			}

			// Run synchronously: the timer sleeps auto-advance the fake clock, and
			// fn cancels the context on the 5th call so withRetry returns.
			if _, err := f.withRetry(ctx, opNext, fn); !errors.Is(err, context.Canceled) {
				t.Fatalf("withRetry: got %v, want context.Canceled", err)
			}
			if len(offsets) != 5 {
				t.Fatalf("fn call count: got %d, want 5", len(offsets))
			}

			// Inter-call gaps follow the pre-jitter backoff schedule (100, 200, 400,
			// 400 — doubling then holding at the cap); maxJitter absorbs the added
			// jitter. The shape (doubling then plateau) is the assertion.
			wantMin := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 400 * time.Millisecond}
			for i, lo := range wantMin {
				gap := offsets[i+1] - offsets[i]
				hi := lo + maxJitter
				if gap < lo || gap > hi {
					t.Errorf("gap[%d→%d] = %v, want within [%v, %v]", i, i+1, gap, lo, hi)
				}
			}
		})
	})
}
