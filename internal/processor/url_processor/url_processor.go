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
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// RobotsTxtChecker gates URLs by the target host's robots.txt. Returns nil
// when allowed, a non-nil error when blocked or unreachable.
type RobotsTxtChecker interface {
	Check(ctx context.Context, u string) error
}

type Config struct {
	Frontier   frontier.Frontier
	Downloader downloader.Downloader
	Parser     parser.Parser
	// ContentFilter rejects pages that should not be processed at all
	// (e.g., empty or non-textual content). Applied before link discovery.
	ContentFilter    filter.CheckFn[*crawler.Content]
	URLFilter        filter.CheckFn[string]
	RobotsTxtChecker RobotsTxtChecker
	// RelevanceFilter identifies pages that look like job listings.
	// Pages that pass this filter are forwarded to OnJobListing.
	RelevanceFilter filter.CheckFn[*crawler.Content]
	// GateConfig supplies the pre-LLM URL-path signals (ADR-0007 step 2) that
	// resolve a page without the LLM extractor: an ATS board root or career-hub
	// index (crawled for its postings, not extracted) and reject paths. A zero
	// value applies no URL gating, so every page falls through to keyword
	// relevance.
	GateConfig crawler.LLMGateConfig
	// OnJobListing is called when a page passes the relevance filter.
	// Typically this enqueues the raw listing into a job listing worker pool.
	OnJobListing func(ctx context.Context, jobListing *crawler.RawJobListing) error
	// Recorder instruments the keyword relevance gate (pages resolved without the
	// LLM extractor) for the ADR-0007 measurement. Optional: a nil Recorder
	// records nothing.
	Recorder llmobs.Recorder
}

type urlWorker struct {
	frontier             frontier.Frontier
	downloader           downloader.Downloader
	parser               parser.Parser
	contentFilter        filter.CheckFn[*crawler.Content]
	urlFilter            filter.CheckFn[string]
	robotsTxtChecker     RobotsTxtChecker
	relevanceFilter      filter.CheckFn[*crawler.Content]
	gateConfig           crawler.LLMGateConfig
	recorder             llmobs.Recorder
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

	recorder := cfg.Recorder
	if recorder == nil {
		recorder = llmobs.Nop()
	}

	return &urlWorker{
		frontier:             cfg.Frontier,
		downloader:           cfg.Downloader,
		parser:               cfg.Parser,
		contentFilter:        cfg.ContentFilter,
		urlFilter:            cfg.URLFilter,
		robotsTxtChecker:     cfg.RobotsTxtChecker,
		relevanceFilter:      cfg.RelevanceFilter,
		gateConfig:           cfg.GateConfig,
		recorder:             recorder,
		urlsProcessedCounter: urlsProcessedCounter,
		onJobListing:         cfg.OnJobListing,
	}
}

func (w *urlWorker) Process(ctx context.Context, nextURL *crawler.URL) error {
	defer w.frontier.MarkDone(ctx, nextURL.RawURL)

	slog.Info("worker: got nextURL", "url", nextURL.RawURL)

	if err := w.robotsTxtChecker.Check(ctx, nextURL.RawURL); err != nil {
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

	if !pagegate.ShouldExtract(*nextURL, w.gateConfig) {
		// A Career Page index or a reject path — resolved without the LLM extractor.
		w.recorder.Gated(ctx, llmobs.KindExtract, llmobs.ReasonURLStructure)
	} else if err := w.relevanceFilter(content); err == nil {
		slog.Info("worker: content passed relevance filter", "title", content.Title, "url", nextURL.RawURL)

		err := w.onJobListing(ctx, &crawler.RawJobListing{
			URL:     *nextURL,
			Content: *content,
		})
		if err != nil {
			slog.Error("worker: onJobListing returned an error", "err", err)
		}
	} else {
		// The keyword relevance gate resolved this page without the LLM extractor.
		w.recorder.Gated(ctx, llmobs.KindExtract, llmobs.ReasonIrrelevant)
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

		if err := w.robotsTxtChecker.Check(ctx, parsed.RawURL); err != nil {
			slog.Info("worker: robots.txt filtered out url", "url", parsed.RawURL, "cause", err)
			continue
		}

		// AddURL fuses dedup with enqueue: an already-seen URL is a silent
		// no-op, so there is no separate visited check to race against.
		err = w.frontier.AddURL(ctx, parsed)
		switch {
		case errors.Is(err, frontier.ErrMaxDomainLimit):
			slog.Debug("worker: max domain limit reached, dropping new domain", "url", parsed.RawURL)
			continue
		case errors.Is(err, frontier.ErrMaxDepth):
			// Reaching maxDepth is an expected client-side rejection during
			// normal crawling, not an error worth flagging per URL.
			slog.Debug("worker: max depth reached, dropping url", "url", parsed.RawURL)
			continue
		case err != nil:
			slog.Error("worker: error adding url", "err", err, "url", parsed.RawURL)
			continue
		}
	}

	w.urlsProcessedCounter.Add(ctx, 1)
	return nil
}
