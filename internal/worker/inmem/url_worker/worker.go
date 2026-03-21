package urlworker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/http"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
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
	Processor       processor.Processor
}

type Worker struct {
	frontier             frontier.Frontier
	downloader           http.Downloader
	parser               parser.Parser
	urlRepository        crawler.URLRepository
	jobRepository        crawler.JobRepository
	contentFilter        filter.CheckFn[*crawler.Content]
	urlFilter            filter.CheckFn[string]
	relevanceFilter      filter.CheckFn[*crawler.Content]
	maxDepth             int
	processor            processor.Processor
	urlsProcessedCounter metric.Int64Counter
}

func NewWorker(cfg *Config) *Worker {
	meter := otel.Meter("worker")
	urlsProcessedCounter, _ := meter.Int64Counter("crawler.urls.processed")

	return &Worker{
		frontier:             cfg.Frontier,
		downloader:           cfg.Downloader,
		parser:               cfg.Parser,
		urlRepository:        cfg.URLRepository,
		jobRepository:        cfg.JobRepository,
		contentFilter:        cfg.ContentFilter,
		urlFilter:            cfg.URLFilter,
		relevanceFilter:      cfg.RelevanceFilter,
		maxDepth:             cfg.MaxDepth,
		processor:            cfg.Processor,
		urlsProcessedCounter: urlsProcessedCounter,
	}
}

func (w *Worker) Process(ctx context.Context, nextURL crawler.URL) error {
	defer w.frontier.MarkDone(context.Background())

	slog.Info("worker: got nextURL", "url", nextURL.RawURL)

	rawHTML, err := w.downloader.Get(ctx, nextURL.RawURL)
	if err != nil {
		return fmt.Errorf("worker: error downloading raw html %w", err)
	}

	content, err := w.parser.Parse(rawHTML.Content)
	if err != nil {
		return fmt.Errorf("worker: error parsing content %w", err)
	}

	if err := w.contentFilter(content); err != nil {
		return fmt.Errorf("worker: content filtered out %w", err)
	}

	if err := w.relevanceFilter(content); err == nil {
		slog.Info("content passed relevance filter", "title", content.Title, "url", nextURL)

		err := w.processor.Process(ctx, processor.WorkLoad{URL: nextURL, Content: *content})
		if err != nil {
			slog.Error("error processing content", "err", err)
		}

	}

	for _, contentURL := range content.URLs {
		parsed, err := nextURL.Parse(contentURL)
		if err != nil {
			slog.Error("worker: error parsing content url", "err", err, "url", contentURL)
			continue
		}
		if err := w.urlFilter(parsed.RawURL); err != nil {
			slog.Info("worker: url filtered out", "url", parsed.RawURL, "cause", err)
			continue
		}

		isNew, err := w.urlRepository.Save(ctx, parsed.RawURL)
		if err != nil {
			slog.Error("worker: error saving url", "err", err)
			continue
		}

		if !isNew {
			continue
		}

		err = w.frontier.AddURL(ctx, parsed)
		if errors.Is(err, frontier.ErrMaxDomainLimit) {
			slog.Info("worker: max domain limit reached, dropping new domains")
			continue
		}
		if err != nil {
			slog.Error("worker: error adding url", "err", err)
			continue
		}
	}

	w.urlsProcessedCounter.Add(ctx, 1)
	return nil
}
