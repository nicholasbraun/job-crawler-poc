// Package orchestrator is where everything is tied together.
// This is the entrypoint for our cmd/*/main.go.
package orchestrator

import (
	"context"
	"errors"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
)

type Config struct {
	Frontier      frontier.Frontier
	URLRepository crawler.URLRepository
	OnNextURL     func(ctx context.Context, nextURL *crawler.URL) error
}

type Orchestrator struct {
	frontier      frontier.Frontier
	urlRepository crawler.URLRepository
	onNextURL     func(ctx context.Context, nextURL *crawler.URL) error
}

func NewOrchestrator(cfg Config) *Orchestrator {
	return &Orchestrator{
		frontier:      cfg.Frontier,
		urlRepository: cfg.URLRepository,
		onNextURL:     cfg.OnNextURL,
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

		err = o.onNextURL(ctx, &nextURL)
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
