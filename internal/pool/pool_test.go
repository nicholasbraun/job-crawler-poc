package pool_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// recordingWorker records every item it is handed and optionally panics or
// returns an error for items matching a predicate. Its state is shared across
// all workers in a pool via pointers guarded by a mutex.
type recordingWorker struct {
	mu        *sync.Mutex
	processed *[]int
	panicOn   func(int) bool
	errOn     func(int) bool
}

func (w recordingWorker) Process(ctx context.Context, workload *int) error {
	w.mu.Lock()
	*w.processed = append(*w.processed, *workload)
	w.mu.Unlock()

	if w.panicOn != nil && w.panicOn(*workload) {
		panic("boom")
	}
	if w.errOn != nil && w.errOn(*workload) {
		return errors.New("processing failed")
	}
	return nil
}

func TestPoolRecoversFromWorkerPanic(t *testing.T) {
	var mu sync.Mutex
	processed := []int{}

	// A single worker so ordering is deterministic: the poisoned item is
	// processed, panics, and the same worker must survive to handle the rest.
	p := pool.NewPool(t.Context(), "test", func() processor.Processor[int] {
		return recordingWorker{
			mu:        &mu,
			processed: &processed,
			panicOn:   func(n int) bool { return n == 2 },
		}
	}, pool.WithMaxWorkers[int](1), pool.WithChannelSize[int](10))

	for i := 0; i < 5; i++ {
		n := i
		if err := p.Enqueue(t.Context(), &n); err != nil {
			t.Fatalf("Enqueue(%d): %v", n, err)
		}
	}

	// Close blocks until all workers drain; if the panic had escaped the
	// goroutine the test process would already have crashed.
	p.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != 5 {
		t.Fatalf("processed %d items, want 5 (a panic must not drop subsequent work): %v", len(processed), processed)
	}
}

func TestPoolContinuesAfterProcessError(t *testing.T) {
	var mu sync.Mutex
	processed := []int{}

	p := pool.NewPool(t.Context(), "test", func() processor.Processor[int] {
		return recordingWorker{
			mu:        &mu,
			processed: &processed,
			errOn:     func(n int) bool { return n == 1 },
		}
	}, pool.WithMaxWorkers[int](1), pool.WithChannelSize[int](10))

	for i := 0; i < 3; i++ {
		n := i
		if err := p.Enqueue(t.Context(), &n); err != nil {
			t.Fatalf("Enqueue(%d): %v", n, err)
		}
	}
	p.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(processed) != 3 {
		t.Fatalf("processed %d items, want 3: %v", len(processed), processed)
	}
}
