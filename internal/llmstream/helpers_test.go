package llmstream_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// task is the unit of work the stage carries in these tests. Its field is
// exported so json.Marshal (the stream payload encoding) round-trips it.
type task struct {
	Key string
}

// spyProcessor is a shared, concurrency-safe processor.Processor[task] used to
// assert consumption behavior. Every worker/reclaimer of a stage is given the
// same instance (the newProc closure returns it), so counts accumulate across
// goroutines. failFor forces the first N attempts of a key to error (modelling a
// crash-before-ack); alwaysFail makes every attempt error (modelling poison); gate,
// when non-nil, holds every Process call open until it is closed or the context is
// cancelled (modelling a slow-but-alive worker still inside its call). All are set
// before Start, so writing them without the mutex is safe.
type spyProcessor struct {
	mu         sync.Mutex
	calls      int
	seen       map[string]int // successful processings per key
	failFor    map[string]int // remaining forced failures per key
	alwaysFail bool
	gate       chan struct{}
}

func newSpy() *spyProcessor {
	return &spyProcessor{seen: map[string]int{}, failFor: map[string]int{}}
}

func (p *spyProcessor) Process(ctx context.Context, t *task) error {
	// Count the invocation, then (if gated) block outside the lock so a held call
	// does not stall the counters other goroutines read.
	p.mu.Lock()
	p.calls++
	gate := p.gate
	p.mu.Unlock()

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.alwaysFail {
		return fmt.Errorf("spy: always fail %q", t.Key)
	}
	if p.failFor[t.Key] > 0 {
		p.failFor[t.Key]--
		return fmt.Errorf("spy: forced failure for %q", t.Key)
	}
	p.seen[t.Key]++
	return nil
}

func (p *spyProcessor) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func (p *spyProcessor) seenCount(key string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen[key]
}

// distinctSeen is the number of distinct keys that were successfully processed at
// least once — the count of durable effects, regardless of how many times a task
// was delivered.
func (p *spyProcessor) distinctSeen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.seen)
}

// procOf returns a newProc closure that always hands out the same shared spy.
func procOf(spy *spyProcessor) func() processor.Processor[task] {
	return func() processor.Processor[task] { return spy }
}

// waitFor polls cond until it holds or the timeout elapses (then fails the test).
func waitFor(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition never held within %v: %s", timeout, msg)
}

// newTestClient starts a throwaway Redis container and returns a connected
// client (mirrors the frontier's harness). Requires a running Docker daemon; a
// missing daemon surfaces as a test failure so CI can't silently drop coverage.
func newTestClient(t *testing.T) *redis.Client {
	t.Helper()
	ctx := t.Context()

	ctr, err := tcredis.Run(ctx, "redis:7")
	if err != nil {
		t.Fatalf("error starting redis container: %v", err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(ctr); err != nil {
			t.Logf("error terminating redis container: %v", err)
		}
	})

	connStr, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("error building connection string: %v", err)
	}

	opts, err := redis.ParseURL(connStr)
	if err != nil {
		t.Fatalf("error parsing redis url %q: %v", connStr, err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("error pinging redis: %v", err)
	}

	return client
}
