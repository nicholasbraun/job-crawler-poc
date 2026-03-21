// Package orchestrator is where everything is tied together.
// This is the entrypoint for our cmd/*/main.go.
package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/http"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	urlworker "github.com/nicholasbraun/job-crawler-poc/internal/worker/inmem/url_worker"
)

type Config struct {
	Frontier        frontier.Frontier
	Downloader      http.Downloader
	Parser          parser.Parser
	URLRepository   crawler.URLRepository
	JobRepository   crawler.JobRepository
	ContentFilter   filter.CheckFn[*crawler.Content]
	URLFilter       filter.CheckFn[string]
	RelevanceFilter filter.CheckFn[*crawler.Content]
	MaxDepth        int
	MaxWorkers      int
	Processor       processor.Processor
}

type Orchestrator struct {
	frontier        frontier.Frontier
	downloader      http.Downloader
	parser          parser.Parser
	urlRepository   crawler.URLRepository
	jobRepository   crawler.JobRepository
	contentFilter   filter.CheckFn[*crawler.Content]
	urlFilter       filter.CheckFn[string]
	relevanceFilter filter.CheckFn[*crawler.Content]
	maxDepth        int
	maxWorkers      int
	processor       processor.Processor
}

func NewOrchestrator(cfg Config) *Orchestrator {
	return &Orchestrator{
		frontier:        cfg.Frontier,
		downloader:      cfg.Downloader,
		parser:          cfg.Parser,
		urlRepository:   cfg.URLRepository,
		jobRepository:   cfg.JobRepository,
		contentFilter:   cfg.ContentFilter,
		urlFilter:       cfg.URLFilter,
		relevanceFilter: cfg.RelevanceFilter,
		maxDepth:        cfg.MaxDepth,
		processor:       cfg.Processor,
		maxWorkers:      cfg.MaxWorkers,
	}
}

func (o *Orchestrator) Run(ctx context.Context, seedURLs []string) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer o.processor.Close()

	for _, seedURL := range seedURLs {
		parsed, err := crawler.NewURL(seedURL)
		if err != nil {
			slog.Error("could not parse seed url", "err", err)
			continue
		}

		if _, err = o.urlRepository.Save(ctx, seedURL); err != nil {
			slog.Error("error saving url", "err", err)
			continue
		}

		err = o.frontier.AddURL(ctx, parsed)
		if errors.Is(err, frontier.ErrMaxDomainLimit) {
			slog.Info("max domain limit reached, dropping new domains")
			continue
		}
		if err != nil {
			slog.Error("error adding seed url", "err", err)
			continue
		}
	}

	pool := urlworker.NewInMemURLWorkerPool(ctx, &urlworker.Config{
		URLRepository:   o.urlRepository,
		Frontier:        o.frontier,
		Downloader:      o.downloader,
		Parser:          o.parser,
		JobRepository:   o.jobRepository,
		ContentFilter:   o.contentFilter,
		URLFilter:       o.urlFilter,
		RelevanceFilter: o.relevanceFilter,
		MaxDepth:        o.maxDepth,
		Processor:       o.processor,
	}, urlworker.WithMaxWorkers(o.maxWorkers))

	defer pool.Close()

	for {
		nextURL, err := o.frontier.Next(ctx)
		if errors.Is(err, frontier.ErrDone) {
			slog.Info("received Done signal. ending crawl")
			cancel(frontier.ErrDone)
			return nil
		}
		if err != nil {
			slog.Error("error getting next url", "err", err)
			cancel(err)
			return err
		}
		slog.Info("got nextURL", "url", nextURL.RawURL)

		err = pool.Process(ctx, nextURL)
		if err != nil {
			cancel(err)
			return err
		}

		select {
		case <-ctx.Done():
			cancel(ctx.Err())
			return ctx.Err()
		default:
		}
	}
}
