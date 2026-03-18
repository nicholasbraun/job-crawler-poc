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

		err = o.urlRepository.Save(ctx, seedURL)
		if err != nil {
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

		rawHTML, err := o.downloader.Get(ctx, nextURL.RawURL)
		if err != nil {
			slog.Error("error downloading raw html", "err", err)
			continue
		}

		content, err := o.parser.Parse(rawHTML.Content)
		if err != nil {
			slog.Error("error parsing content", "err", err)
			continue
		}

		if err := o.contentFilter(content); err != nil {
			slog.Info("content filtered out", "cause", err)
			continue
		}

		if err := o.relevanceFilter(content); err == nil {
			slog.Info("content passed relevance filter", "title", content.Title, "url", nextURL)
			err := o.processor.Process(ctx, processor.WorkLoad{URL: nextURL, Content: *content})
			if err != nil {
				slog.Error("error processing content", "err", err)
			}
		}

		for _, contentURL := range content.URLs {
			if err := o.urlFilter(contentURL); err != nil {
				slog.Info("url filtered out", "cause", err)
				continue
			}

			parsed, err := nextURL.Parse(contentURL)
			if err != nil {
				slog.Error("error parsing content url", "err", err, "url", contentURL)
				continue
			}
			visited, err := o.urlRepository.Visited(ctx, parsed.RawURL)
			if err != nil {
				slog.Error("error querying URLRepository", "err", err, "url", parsed)
				continue
			}
			if visited {
				slog.Info("already visited url. skipping", "url", parsed.RawURL)
				continue
			}

			err = o.urlRepository.Save(ctx, parsed.RawURL)
			if err != nil {
				slog.Error("error saving url", "err", err)
				continue
			}

			if parsed.Depth > o.maxDepth {
				slog.Info("max depth reached for URL", "url", parsed.RawURL)
				continue
			}

			err = o.frontier.AddURL(ctx, parsed)
			if errors.Is(err, frontier.ErrMaxDomainLimit) {
				slog.Info("max domain limit reached, dropping new domains")
				continue
			}
			if err != nil {
				slog.Error("error adding url", "err", err)
				continue
			}
		}

		select {
		case <-ctx.Done():
			cancel(ctx.Err())
			return ctx.Err()
		default:
		}
	}
}
