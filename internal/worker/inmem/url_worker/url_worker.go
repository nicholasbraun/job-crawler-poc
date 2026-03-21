package urlworker

import (
	"context"
	"log/slog"
	"sync"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/worker"
)

var channelSize = 10

type PoolOption func(*InMemURLWorkerPool)

func WithMaxWorkers(m int) PoolOption {
	return func(f *InMemURLWorkerPool) {
		f.maxWorkers = m
	}
}

type InMemURLWorkerPool struct {
	workStream chan crawler.URL
	wg         sync.WaitGroup
	workerCfg  *Config
	maxWorkers int
}

var _ worker.Processor[crawler.URL] = &InMemURLWorkerPool{}

func (p *InMemURLWorkerPool) Process(ctx context.Context, workload crawler.URL) error {
	select {
	case p.workStream <- workload:
	case <-ctx.Done():
		return ctx.Err()

	}

	return nil
}

func (p *InMemURLWorkerPool) Close() {
	close(p.workStream)
	p.wg.Wait()
}

func (p *InMemURLWorkerPool) run(ctx context.Context) {
	for range p.maxWorkers {
		w := NewWorker(p.workerCfg)
		p.wg.Go(func() {
			for workload := range p.workStream {
				err := w.Process(ctx, workload)
				if err != nil {
					slog.Error("url_worker: error processing url", "err", err)
				}
			}
		})
	}
}

func NewInMemURLWorkerPool(ctx context.Context, cfg *Config, opts ...PoolOption) *InMemURLWorkerPool {
	ws := make(chan crawler.URL, channelSize)
	p := &InMemURLWorkerPool{
		workStream: ws,
		workerCfg:  cfg,
		maxWorkers: 4,
	}

	for _, fn := range opts {
		fn(p)
	}

	p.run(ctx)

	return p
}
