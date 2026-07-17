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
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
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
	// HasATSFetcher reports whether an ATS provider has a registered board-API
	// fetcher. Only a firing ATS Embed for such a provider triggers OnATSEmbed; an
	// embed for a clientless provider is ignored — v1 does not derive a crawlable
	// board URL from it (ADR-0022). Optional: nil reports false for every provider,
	// disabling embed-triggered fetches.
	HasATSFetcher func(provider string) bool
	// OnATSEmbed triggers an ATS Fetch of a board embedded on a crawled Keyword-Crawl
	// page, attributed to the page's Owner (ADR-0022, #129). Called once per firing
	// embed whose provider HasATSFetcher reports registered. Optional: nil disables
	// embed-triggered fetches.
	OnATSEmbed func(ctx context.Context, provider, tenant, owner string) error
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
	hasATSFetcher        func(provider string) bool
	onATSEmbed           func(ctx context.Context, provider, tenant, owner string) error
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
		hasATSFetcher:        cfg.HasATSFetcher,
		onATSEmbed:           cfg.OnATSEmbed,
	}
}

func (w *urlWorker) Process(ctx context.Context, nextURL *crawler.URL) error {
	defer func() {
		if err := w.frontier.MarkDone(ctx, nextURL.RawURL); err != nil {
			slog.Error("worker: failed to mark url done", "url", nextURL.RawURL, "err", err)
		}
	}()

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

	// Trigger an ATS Fetch for any board embedded on this page (ADR-0022, #129).
	// Runs for every downloaded+parsed+filtered page regardless of the extract or
	// relevance outcome: the embedded board is not among content.URLs (embeds are
	// kept out of the crawl frontier) so it is otherwise unreachable.
	w.triggerATSEmbeds(ctx, nextURL, content)

	if !pagegate.ShouldExtract(*nextURL, content, w.gateConfig) {
		// The Extract Gate shed this page without the LLM extractor -- a URL signal
		// (Career Page index or reject path) or a page-structure signal (ATS embed,
		// JSON-LD openings index, or job-link saturation).
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

		// Scope fence (ADR-0021): on a Keyword Crawl, follow a discovered link only
		// when it resolves to the same Company as its seed. parsed.Scope is inherited
		// from the seed via URL.Parse; an empty Scope means roam, so this is inert for
		// any un-scoped crawl. Runs alongside the URL filter chain but keyed on
		// provenance the chain cannot see.
		if !catalog.InScope(parsed.Scope, parsed) {
			slog.Info("worker: url out of scope, dropping", "url", parsed.RawURL, "scope", parsed.Scope)
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

// triggerATSEmbeds fires an ATS Fetch for each firing ATS Embed on content whose
// provider has a registered board-API fetcher (ADR-0022, #129). The fetched board
// is attributed to nextURL.Owner — the ADR-0021 attribution key the page inherited
// from its seed. An embed for a clientless provider (no registered fetcher) is
// ignored; v1 never derives a crawlable board URL from it. It is inert when either
// hook is nil (a Discovery Crawl or an un-wired Keyword Crawl). A trigger error is
// logged and skipped, best-effort, so it never aborts Process.
func (w *urlWorker) triggerATSEmbeds(ctx context.Context, nextURL *crawler.URL, content *crawler.Content) {
	if w.onATSEmbed == nil || w.hasATSFetcher == nil {
		return
	}
	for _, ref := range pagegate.ATSEmbedTenants(content) {
		if !w.hasATSFetcher(ref.Provider) {
			continue
		}
		if err := w.onATSEmbed(ctx, ref.Provider, ref.Tenant, nextURL.Owner); err != nil {
			slog.Error("worker: ats embed trigger failed", "err", err,
				"provider", ref.Provider, "tenant", ref.Tenant, "url", nextURL.RawURL)
		}
	}
}
