// Package robotstxt provides a robots.txt filter for URLs. It downloads,
// parses, and caches robots.txt rules per hostname, deduplicating concurrent
// requests to the same host with singleflight.
package robotstxt

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"golang.org/x/sync/singleflight"
)

// Rules determines whether a URL may be crawled according to a parsed
// robots.txt file.
type Rules interface {
	IsAllowed(path string) bool
	CrawlDelay() time.Duration
}

// Parser parses raw robots.txt content into a Rules set.
type Parser interface {
	Parse(b []byte) (Rules, error)
}

// Getter fetches the raw content of a URL. Used to download robots.txt files.
type Getter interface {
	Get(ctx context.Context, url string) (resp *Response, err error)
}

type allowAll struct{}

func (a allowAll) IsAllowed(path string) bool {
	return true
}

func (a allowAll) CrawlDelay() time.Duration {
	return 0
}

type disallowAll struct{}

func (a disallowAll) IsAllowed(path string) bool {
	return false
}

func (a disallowAll) CrawlDelay() time.Duration {
	return 0
}

// NewRobotsTxtCheckFn returns a filter.CheckFn that rejects URLs disallowed
// by the target host's robots.txt. Rules are cached per hostname and concurrent
// fetches to the same host are deduplicated. Returns a non-nil error for
// blocked URLs or if the robots.txt cannot be retrieved.
func NewRobotsTxtCheckFn(ctx context.Context, parser Parser, downloader Getter) filter.CheckFn[string] {
	cache := newCache()
	g := new(singleflight.Group)

	return func(u string) error {
		parsedURL, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("robotstxt: error parsing url '%s': %w", u, err)
		}

		hostname := parsedURL.Hostname()

		fetchFn := func() (Rules, error) {
			res, err, _ := g.Do(hostname, func() (any, error) {
				baseURL := parsedURL.Scheme + "://" + hostname
				robotsURL := baseURL + "/robots.txt"
				response, err := downloader.Get(ctx, robotsURL)
				if err != nil {
					return nil, fmt.Errorf("robotstxt: error downloading '%s'. %w", robotsURL, err)
				}

				// Treat missing (404/410)  robots.txt as allowing
				// all paths, and unavailable (5xx) as disallow
				// all paths per the robots.txt specification (RFC 9309 §2.4).
				if response.StatusCode == 404 || response.StatusCode == 410 {
					return allowAll{}, nil
				}

				if response.StatusCode >= 500 {
					return disallowAll{}, nil
				}

				return parser.Parse(response.Content)
			})
			if err != nil {
				return nil, err
			}
			if rules, ok := res.(Rules); ok {
				return rules, nil
			}
			return nil, fmt.Errorf("robotstxt: could not convert to Rules: %v", res)
		}

		rules, err := cache.getOrFetch(hostname, fetchFn)
		if err != nil {
			return fmt.Errorf("robotstxt: error getting rules from cache for hostname '%s': %w", hostname, err)
		}

		path := parsedURL.RequestURI()
		isAllowed := rules.IsAllowed(path)
		if isAllowed {
			return nil
		}

		return fmt.Errorf("robotstxt: blocked url '%s'", u)
	}
}
