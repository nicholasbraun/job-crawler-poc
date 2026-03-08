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

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type Frontier interface {
	AddURL(ctx context.Context, url crawler.URL) error
	Next(ctx context.Context) (crawler.URL, error)
}
