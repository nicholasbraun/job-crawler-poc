// Package discoveryprocessor implements the discovery crawl worker: it
// downloads a URL, parses its content (including JSON-LD), applies the
// deterministic career-page Gate, emits gate-passing pages as RawCareerPage
// candidates for the career-page pool, and discovers new URLs for the frontier.
// It does no LLM or database work, keeping the perpetual discovery pool cheap.
package discoveryprocessor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
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
	// (e.g., empty or non-textual content). Applied before the Gate.
	ContentFilter    filter.CheckFn[*crawler.Content]
	URLFilter        filter.CheckFn[string]
	RobotsTxtChecker RobotsTxtChecker
	// GateConfig supplies the pre-LLM URL-path signals and the final-rung
	// Confidence Score floats (ADR-0007 step 2, ADR-0016) the career-page gate
	// uses to accept a career-hub page without the LLM classifier. Wire
	// crawler.DefaultLLMGateConfig; a zero value is not a meaningful config — its
	// zero CertainThreshold certain-accepts every page that reaches the final rung.
	GateConfig crawler.LLMGateConfig
	// OnCareerPage is called for each page that passes the career-page gate,
	// typically enqueuing the candidate into the career-page worker pool.
	OnCareerPage func(ctx context.Context, page *crawler.RawCareerPage) error
}

type discoveryWorker struct {
	frontier             frontier.Frontier
	downloader           downloader.Downloader
	parser               parser.Parser
	contentFilter        filter.CheckFn[*crawler.Content]
	urlFilter            filter.CheckFn[string]
	robotsTxtChecker     RobotsTxtChecker
	gateConfig           crawler.LLMGateConfig
	urlsProcessedCounter metric.Int64Counter
	onCareerPage         func(ctx context.Context, page *crawler.RawCareerPage) error
}

func NewProcessor(cfg *Config) *discoveryWorker {
	meter := otel.Meter("discovery_worker")
	name := "crawler.discovery.processed"
	urlsProcessedCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("discovery_worker: error setting up metrics", "err", err, "name", name)
	}

	return &discoveryWorker{
		frontier:             cfg.Frontier,
		downloader:           cfg.Downloader,
		parser:               cfg.Parser,
		contentFilter:        cfg.ContentFilter,
		urlFilter:            cfg.URLFilter,
		robotsTxtChecker:     cfg.RobotsTxtChecker,
		gateConfig:           cfg.GateConfig,
		urlsProcessedCounter: urlsProcessedCounter,
		onCareerPage:         cfg.OnCareerPage,
	}
}

func (w *discoveryWorker) Process(ctx context.Context, nextURL *crawler.URL) error {
	defer func() {
		if err := w.frontier.MarkDone(ctx, nextURL.RawURL); err != nil {
			slog.Error("discovery_worker: failed to mark url done", "url", nextURL.RawURL, "err", err)
		}
	}()

	slog.Debug("discovery_worker: got nextURL", "url", nextURL.RawURL)

	if err := w.robotsTxtChecker.Check(ctx, nextURL.RawURL); err != nil {
		return fmt.Errorf("discovery_worker: robots.txt filtered out url (%s): %w", nextURL.RawURL, err)
	}

	rawHTML, err := w.downloader.Get(ctx, nextURL.RawURL)
	if err != nil {
		return fmt.Errorf("discovery_worker: error downloading raw html %w", err)
	}

	content, err := w.parser.Parse(rawHTML.Content)
	if err != nil {
		return fmt.Errorf("discovery_worker: error parsing content %w", err)
	}

	if err := w.contentFilter(content); err != nil {
		return fmt.Errorf("discovery_worker: content filtered out %w", err)
	}

	if accept, certain := pagegate.CareerPage(*nextURL, content, w.gateConfig); accept {
		slog.Info("discovery_worker: content passed career-page gate", "title", content.Title, "url", nextURL.RawURL, "certain", certain)

		err := w.onCareerPage(ctx, &crawler.RawCareerPage{
			URL:     *nextURL,
			Content: *content,
			Certain: certain,
		})
		if err != nil {
			slog.Error("discovery_worker: onCareerPage returned an error", "err", err)
		}
	}

	for _, contentURL := range content.URLs {
		parsed, err := nextURL.Parse(contentURL)
		if err != nil {
			slog.Error("discovery_worker: error parsing content url", "err", err, "url", contentURL)
			continue
		}
		if err := w.urlFilter(parsed.RawURL); err != nil {
			slog.Debug("discovery_worker: url filtered out", "url", parsed.RawURL, "cause", err)
			continue
		}

		// robots.txt is enforced when a URL is actually crawled (the page-level
		// Check at the top of Process), not here at discovery. Fetching robots.txt
		// for every discovered link -- most never crawled, many already visited --
		// serialized each worker on the network and was the discovery throughput
		// bottleneck. A disallowed URL is enqueued but rejected before any fetch
		// once it is popped.
		//
		// AddURL fuses dedup with enqueue: an already-seen URL is a silent
		// no-op, so there is no separate visited check to race against.
		err = w.frontier.AddURL(ctx, parsed)
		switch {
		case errors.Is(err, frontier.ErrMaxDepth):
			// Reaching maxDepth is an expected client-side rejection during
			// normal crawling, not an error worth flagging per URL. A single
			// deep hub page otherwise floods the logs with thousands of
			// identical ERROR lines (mirrors url_processor).
			slog.Debug("discovery_worker: max depth reached, dropping url", "url", parsed.RawURL)
			continue
		case err != nil:
			slog.Error("discovery_worker: error adding url", "err", err, "url", parsed.RawURL)
			continue
		}
	}

	w.urlsProcessedCounter.Add(ctx, 1)
	return nil
}
