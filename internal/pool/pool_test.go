package pool_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// blockingWorker parks in Process until release is closed. It lets a test pin a
// worker as busy so subsequent enqueues have nowhere to go.
type blockingWorker struct {
	release <-chan struct{}
}

func (w blockingWorker) Process(ctx context.Context, workload *int) error {
	<-w.release
	return nil
}

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

// After Close returns, Enqueue must reject with ErrPoolClosed, and Close must be
// safe to call more than once.
func TestEnqueueAfterCloseReturnsErrPoolClosed(t *testing.T) {
	var mu sync.Mutex
	processed := []int{}

	p := pool.NewPool(t.Context(), "test", func() processor.Processor[int] {
		return recordingWorker{mu: &mu, processed: &processed}
	}, pool.WithMaxWorkers[int](1), pool.WithChannelSize[int](4))

	n := 1
	if err := p.Enqueue(t.Context(), &n); err != nil {
		t.Fatalf("Enqueue before Close: %v", err)
	}

	p.Close()
	p.Close() // idempotent: a second Close must not panic or block

	m := 2
	if err := p.Enqueue(t.Context(), &m); !errors.Is(err, pool.ErrPoolClosed) {
		t.Fatalf("Enqueue after Close = %v, want ErrPoolClosed", err)
	}
}

// Close must not couple its shutdown signal to Process latency: an Enqueue
// blocked on a full pool has to be released by Close (via ErrPoolClosed) without
// waiting for the busy worker to finish. Close itself still waits for that
// in-flight Process to complete before returning.
func TestCloseReleasesBlockedEnqueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		p := pool.NewPool(t.Context(), "test",
			func() processor.Processor[int] { return blockingWorker{release: release} },
			pool.WithMaxWorkers[int](1), pool.WithChannelSize[int](0))

		// The single worker takes this item and parks in Process; with an
		// unbuffered workStream the pool now has no free slot at all.
		a := 1
		if err := p.Enqueue(t.Context(), &a); err != nil {
			t.Fatalf("Enqueue(a): %v", err)
		}

		// A second enqueue cannot be handed off, so it blocks in the send.
		b := 2
		errCh := make(chan error, 1)
		go func() { errCh <- p.Enqueue(t.Context(), &b) }()
		synctest.Wait() // b's Enqueue is now durably blocked on the send

		closeReturned := make(chan struct{})
		go func() { p.Close(); close(closeReturned) }()
		synctest.Wait() // Close has signalled shutdown and reached wg.Wait

		// The blocked send was released by Close's done signal, not by the worker.
		select {
		case err := <-errCh:
			if !errors.Is(err, pool.ErrPoolClosed) {
				t.Fatalf("blocked Enqueue after Close = %v, want ErrPoolClosed", err)
			}
		default:
			t.Fatal("Close left a blocked Enqueue hanging behind an in-flight Process")
		}

		// Close legitimately still blocks until the in-flight Process finishes.
		select {
		case <-closeReturned:
			t.Fatal("Close returned before the in-flight Process finished")
		default:
		}

		// Let the worker finish; Close then drains and returns.
		close(release)
		synctest.Wait()
		select {
		case <-closeReturned:
		default:
			t.Fatal("Close did not return after the worker finished")
		}
	})
}
