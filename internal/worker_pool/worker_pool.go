package workerpool

import (
	"context"
	"log/slog"
	"sync"
)

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
	newWorker   func() Worker[T]
	name        string
}

func (p *Pool[T]) Process(ctx context.Context, workload *T) error {
	select {
	case p.workStream <- workload:
	case <-ctx.Done():
		return ctx.Err()

	}

	return nil
}

func (p *Pool[T]) Close() {
	close(p.workStream)
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

func NewPool[T any](ctx context.Context, name string, factoryFn func() Worker[T], opts ...PoolOption[T]) *Pool[T] {
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
