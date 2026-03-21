// Package frontier defines the interface for the URL Frontier, which is responsible for:
// - Accepting new URLs
// - Managing per-domain URL queues.
// - Internal rate limiting
// - Giving out the next URL for the crawler
//
// Implementations live in the submodules
// For the POC we'll do an in-memory frontier.
package frontier

import (
	"context"
	"errors"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// ErrMaxDomainLimit signals that the frontier has reached its max domains limit.
var ErrMaxDomainLimit = errors.New("frontier: max domains limit reached")

// ErrDone signals that the work is done.
var ErrDone = errors.New("frontier: no urls left to crawl. work is done")

// ErrMaxDepth indicates that the crawl depth for this domain has been reached.
var ErrMaxDepth = errors.New("frontier: max depth reached")

type Frontier interface {
	AddURL(ctx context.Context, url crawler.URL) error
	Next(ctx context.Context) (crawler.URL, error)
	MarkDone(ctx context.Context) error
}
