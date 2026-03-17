package inmem

import (
	"context"
	"log/slog"
	"sync"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

var (
	channelSize = 10
	workers     = 10
)

type InMemProcessor struct {
	workStream    chan processor.WorkLoad
	jobRepository crawler.JobRepository
	wg            sync.WaitGroup
}

var _ processor.Processor = &InMemProcessor{}

func (p *InMemProcessor) Process(ctx context.Context, workload processor.WorkLoad) error {
	select {
	case p.workStream <- workload:
	case <-ctx.Done():
		return ctx.Err()

	}

	return nil
}

func (p *InMemProcessor) Close() {
	close(p.workStream)
	p.wg.Wait()
}

func (p *InMemProcessor) run(ctx context.Context) {
	for range workers {
		p.wg.Go(func() {
			for workload := range p.workStream {
				slog.Info("process workload", "workload", workload)
				job := &crawler.Job{Title: workload.Content.Title, URL: workload.URL.RawURL, Company: "", Location: "", TechStack: nil}
				err := p.jobRepository.Save(ctx, job)
				if err != nil {
					slog.Error("error saving processed job", "err", err)
				}
			}
		})
	}
}

func NewInMemProcessor(ctx context.Context, jobRepo crawler.JobRepository) *InMemProcessor {
	ws := make(chan processor.WorkLoad, channelSize)
	p := &InMemProcessor{
		workStream:    ws,
		jobRepository: jobRepo,
	}

	p.run(ctx)

	return p
}
