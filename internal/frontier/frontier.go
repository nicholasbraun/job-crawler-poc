// Package frontier defines the interface for the URL Frontier, which is responsible for:
// - Accepting new URLs
// - Managing per-domain URL queues.
// - Internal rate limiting
// - Giving out the next URL for the crawler
//
// The Redis-backed implementation lives in the redis submodule.
package frontier

import (
	"context"
	"errors"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// ErrDone signals that the work is done.
var ErrDone = errors.New("frontier: no urls left to crawl. work is done")

// ErrMaxDepth indicates that the crawl depth for this domain has been reached.
var ErrMaxDepth = errors.New("frontier: max depth reached")

// Mode controls how Next behaves once the frontier drains.
type Mode int

const (
	// Bounded makes Next return ErrDone once all queues are empty and no URLs
	// are in-flight. Used for one-shot crawls that finish.
	Bounded Mode = iota
	// Perpetual makes Next keep blocking on drain instead of returning ErrDone,
	// so the run stays alive waiting for URLs to be added later (Step 5).
	Perpetual
)

// Frontier manages the queue of URLs to be crawled. Implementations handle
// per-domain queuing, rate limiting, and signaling when all work is complete.
//
// Callers must call MarkDone after processing each URL returned by Next,
// otherwise the frontier cannot detect when all work is finished.
type Frontier interface {
	AddURL(ctx context.Context, url crawler.URL) error
	// Next blocks until a URL is available and returns it. In bounded mode it
	// returns frontier.ErrDone when all queues are empty and no URLs are
	// in-flight; in perpetual mode it keeps blocking instead of returning
	// ErrDone.
	Next(ctx context.Context) (crawler.URL, error)
	// MarkDone signals that processing of the URL returned by Next has
	// completed, releasing its in-flight lease. url is the RawURL that Next
	// returned. Must be called exactly once per URL returned by Next.
	MarkDone(ctx context.Context, url string) error
}
