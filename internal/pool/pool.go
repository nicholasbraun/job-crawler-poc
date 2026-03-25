// Package pool implements a generic worker pool.
package pool

import (
	"context"
	"errors"
	"log/slog"
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

type Pool[T any] struct {
	workStream  chan *T
	wg          sync.WaitGroup
	maxWorkers  int
	channelSize int
	newWorker   func() processor.Processor[T]
	name        string
	closed      bool
	mu          sync.RWMutex
}

func (p *Pool[T]) Enqueue(ctx context.Context, workload *T) error {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.closed {
		return ErrPoolClosed
	}

	select {
	case p.workStream <- workload:
	case <-ctx.Done():
		return ctx.Err()

	}

	return nil
}

func (p *Pool[T]) Close() {
	p.mu.Lock()

	if p.closed {
		p.mu.Unlock()
		return
	}

	p.closed = true
	close(p.workStream)

	p.mu.Unlock()

	p.wg.Wait()
}

func (p *Pool[T]) run(ctx context.Context) {
	for range p.maxWorkers {
		w := p.newWorker()
		p.wg.Go(func() {
			for workload := range p.workStream {
				err := w.Process(ctx, workload)
				if err != nil {
					slog.Error("worker: error processing url", "worker_name", p.name, "err", err)
				}
			}
		})
	}
}

func NewPool[T any](ctx context.Context, name string, factoryFn func() processor.Processor[T], opts ...PoolOption[T]) *Pool[T] {
	p := &Pool[T]{
		maxWorkers:  4,
		channelSize: 10,
		newWorker:   factoryFn,
		name:        name,
	}

	for _, fn := range opts {
		fn(p)
	}

	ws := make(chan *T, p.channelSize)
	p.workStream = ws

	p.run(ctx)

	return p
}
