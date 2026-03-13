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
)

type Config struct {
	Frontier        frontier.Frontier
	Downloader      http.Downloader
	Parser          parser.Parser
	URLRepository   crawler.URLRepository
	JobRepository   crawler.JobRepository
	ContentFilter   filter.CheckFn[*crawler.Content]
	URLFilter       filter.CheckFn[*crawler.URL]
	RelevanceFilter filter.CheckFn[*crawler.Content]
	MaxDepth        int
}

type Orchestrator struct {
	frontier        frontier.Frontier
	downloader      http.Downloader
	parser          parser.Parser
	urlRepository   crawler.URLRepository
	jobRepository   crawler.JobRepository
	contentFilter   filter.CheckFn[*crawler.Content]
	urlFilter       filter.CheckFn[*crawler.URL]
	relevanceFilter filter.CheckFn[*crawler.Content]
	maxDepth        int
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
	}
}

func (o *Orchestrator) Run(ctx context.Context, seedURLs []string) error {
	ctx, cancel := context.WithCancelCause(ctx)
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

		if nextURL.Depth >= o.maxDepth {
			slog.Info("max depth reached for URL", "url", nextURL.RawURL)
			continue
		}

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
			// TODO: add relevance pipeline. content has to be parsed for a job and saved to job db
		}

		for _, contentURL := range content.URLs {
			parsed, err := nextURL.Parse(contentURL)
			if err != nil {
				slog.Error("error parsing content url", "err", err, "url", contentURL)
				continue
			}
			if err := o.urlFilter(&parsed); err != nil {
				slog.Info("url filtered out", "cause", err)
				continue
			}
			visited, err := o.urlRepository.Visited(ctx, parsed.RawURL)
			if err != nil {
				slog.Error("error querying URLRepository", "err", err, "url", parsed)
				continue
			}
			if visited {
				continue
			}

			err = o.urlRepository.Save(ctx, parsed.RawURL)
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
