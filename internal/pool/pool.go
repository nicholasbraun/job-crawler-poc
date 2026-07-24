// Package pool implements a generic worker pool.
package pool

import (
	"context"
	"errors"
	"log/slog"
	"runtime/debug"
	"sync"

	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

var ErrPoolClosed = errors.New("ErrPoolClosed")

type PoolOption[T any] func(*Pool[T])

func WithChannelSize[T any](n int) PoolOption[T] {
	return func(f *Pool[T]) {
		f.channelSize = n
	}
}

func WithMaxWorkers[T any](n int) PoolOption[T] {
	return func(f *Pool[T]) {
		f.maxWorkers = n
	}
}

// Pool is a bounded worker pool that distributes work items to a fixed
// number of Processor workers via a buffered channel.
type Pool[T any] struct {
	workStream  chan *T
	wg          sync.WaitGroup
	maxWorkers  int
	channelSize int
	newWorker   func() processor.Processor[T]
	name        string
	closed      bool
	// done is closed by Close to release any Enqueue blocked on a full
	// workStream, so shutdown latency is not coupled to the slowest Process.
	done chan struct{}
	// sends counts Enqueue calls that have passed the closed check and may still
	// send on workStream. Close waits for it to reach zero before closing
	// workStream, which makes a send on a closed channel impossible.
	sends sync.WaitGroup
	mu    sync.RWMutex
}

// Enqueue offers workload to the pool, blocking until a worker slot is free, the
// pool is closed, or ctx is cancelled. It returns ErrPoolClosed once Close has
// been called and ctx.Err() on cancellation.
func (p *Pool[T]) Enqueue(ctx context.Context, workload *T) error {
	// Hold the read lock only long enough to observe the closed flag and register
	// this send; it is released before the blocking send so Close's write lock
	// never queues behind an in-flight Process (the release-before-blocking
	// convention). done then lets a send abandon a full workStream the moment
	// Close is called, instead of waiting out a worker.
	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return ErrPoolClosed
	}
	p.sends.Add(1)
	p.mu.RUnlock()
	defer p.sends.Done()

	select {
	case p.workStream <- workload:
		return nil
	case <-p.done:
		return ErrPoolClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting new work, drains the work channel, and blocks until
// all workers have finished processing. Safe to call multiple times.
func (p *Pool[T]) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	// Wake every Enqueue blocked on a full workStream so it returns promptly and
	// stops sending, decoupling shutdown from Process latency.
	close(p.done)
	p.mu.Unlock()

	// No further Enqueue can begin a send — later callers observe closed and
	// reject — and every in-flight send has now either landed in the buffer or
	// bailed via done. Only once none remain is it safe to close workStream: the
	// workers then range to completion, draining every buffered item.
	p.sends.Wait()
	close(p.workStream)
	p.wg.Wait()
}

func (p *Pool[T]) run(ctx context.Context) {
	for range p.maxWorkers {
		w := p.newWorker()
		p.wg.Go(func() {
			for workload := range p.workStream {
				p.process(ctx, w, workload)
			}
		})
	}
}

// process runs a single work item, recovering from any panic in the worker's
// Process so one poisoned item can neither crash the pool goroutine nor the
// process: the panic is logged with the offending item and the worker moves on
// to the next item (see #19).
func (p *Pool[T]) process(ctx context.Context, w processor.Processor[T], workload *T) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("worker: recovered from panic in Process",
				"worker_name", p.name,
				"panic", rec,
				"item", workload,
				"stack", string(debug.Stack()),
			)
		}
	}()

	if err := w.Process(ctx, workload); err != nil {
		slog.Error("worker: error processing url", "worker_name", p.name, "err", err)
	}
}

func NewPool[T any](ctx context.Context, name string, factoryFn func() processor.Processor[T], opts ...PoolOption[T]) *Pool[T] {
	p := &Pool[T]{
		maxWorkers:  4,
		channelSize: 10,
		newWorker:   factoryFn,
		name:        name,
		done:        make(chan struct{}),
	}

	for _, fn := range opts {
		fn(p)
	}

	ws := make(chan *T, p.channelSize)
	p.workStream = ws

	p.run(ctx)

	return p
}
