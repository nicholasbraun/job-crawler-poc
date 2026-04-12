// Package urlprocessor implements the crawl worker that downloads a URL,
// parses its content, applies filters, discovers new URLs, and dispatches
// matching pages as job listings.
package urlprocessor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type Config struct {
	Frontier      frontier.Frontier
	Downloader    downloader.Downloader
	Parser        parser.Parser
	URLRepository crawler.URLRepository
	// ContentFilter rejects pages that should not be processed at all
	// (e.g., empty or non-textual content). Applied before link discovery.
	ContentFilter   filter.CheckFn[*crawler.Content]
	URLFilter       filter.CheckFn[string]
	RobotsTxtFilter filter.CheckFn[string]
	// RelevanceFilter identifies pages that look like job listings.
	// Pages that pass this filter are forwarded to OnJobListing.
	RelevanceFilter filter.CheckFn[*crawler.Content]
	// OnJobListing is called when a page passes the relevance filter.
	// Typically this enqueues the raw listing into a job listing worker pool.
	OnJobListing func(ctx context.Context, jobListing *crawler.RawJobListing) error
}

type urlWorker struct {
	frontier             frontier.Frontier
	downloader           downloader.Downloader
	parser               parser.Parser
	urlRepository        crawler.URLRepository
	contentFilter        filter.CheckFn[*crawler.Content]
	urlFilter            filter.CheckFn[string]
	robotsTxtFilter      filter.CheckFn[string]
	relevanceFilter      filter.CheckFn[*crawler.Content]
	urlsProcessedCounter metric.Int64Counter
	onJobListing         func(ctx context.Context, jobListing *crawler.RawJobListing) error
}

func NewProcessor(cfg *Config) *urlWorker {
	meter := otel.Meter("url_worker")
	name := "crawler.url.processed"
	urlsProcessedCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("url_worker: error setting up metrics", "err", err, "name", name)
	}

	return &urlWorker{
		frontier:             cfg.Frontier,
		downloader:           cfg.Downloader,
		parser:               cfg.Parser,
		urlRepository:        cfg.URLRepository,
		contentFilter:        cfg.ContentFilter,
		urlFilter:            cfg.URLFilter,
		robotsTxtFilter:      cfg.RobotsTxtFilter,
		relevanceFilter:      cfg.RelevanceFilter,
		urlsProcessedCounter: urlsProcessedCounter,
		onJobListing:         cfg.OnJobListing,
	}
}

func (w *urlWorker) Process(ctx context.Context, nextURL *crawler.URL) error {
	defer w.frontier.MarkDone(ctx)

	slog.Info("worker: got nextURL", "url", nextURL.RawURL)

	if err := w.robotsTxtFilter(nextURL.RawURL); err != nil {
		return fmt.Errorf("worker: robots.txt filtered out url (%s): %w", nextURL.RawURL, err)
	}

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
		slog.Info("worker: content passed relevance filter", "title", content.Title, "url", nextURL)

		err := w.onJobListing(ctx, &crawler.RawJobListing{
			URL:     *nextURL,
			Content: *content,
		})
		if err != nil {
			slog.Error("worker: onJobListing returned an error", "err", err)
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

		if err := w.robotsTxtFilter(parsed.RawURL); err != nil {
			slog.Info("worker: robots.txt filtered out url", "url", parsed.RawURL, "cause", err)
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
