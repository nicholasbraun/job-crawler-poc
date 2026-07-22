// Package robotstxt provides a robots.txt filter for URLs. It downloads,
// parses, and caches robots.txt rules per hostname, deduplicating concurrent
// requests to the same host with singleflight.
package robotstxt

import (
	"context"
	"fmt"
	"net/url"
	"time"

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

const (
	// DefaultCacheTTL bounds how long parsed robots.txt Rules are served from
	// cache before a re-fetch. Robots rules change on the order of days, so an
	// hour keeps a hot host's checks off the network while bounding staleness.
	DefaultCacheTTL = time.Hour
	// DefaultCacheSize bounds how many hosts' Rules are cached, so the cache
	// cannot grow without limit across a discovery crawl that touches tens of
	// thousands of hosts. Sized to match the DNS cache (internal/downloader): a
	// host in one is generally a host in the other.
	DefaultCacheSize = 16384
)

// Checker decides whether URLs are allowed by the target host's robots.txt.
// Rules are cached per hostname (bounded in size and age, see cache);
// concurrent fetches to the same host are deduplicated via singleflight.
type Checker struct {
	parser     Parser
	downloader Getter
	cache      *cache
	sf         *singleflight.Group
}

type checkerConfig struct {
	cacheTTL  time.Duration
	cacheSize int
}

// CheckerOption configures a Checker.
type CheckerOption func(*checkerConfig)

// WithCacheTTL sets how long parsed robots.txt Rules are served from cache
// before a re-fetch (default 1h).
func WithCacheTTL(d time.Duration) CheckerOption {
	return func(c *checkerConfig) { c.cacheTTL = d }
}

// WithCacheSize bounds how many hosts' Rules the cache holds (default 16384).
func WithCacheSize(n int) CheckerOption {
	return func(c *checkerConfig) { c.cacheSize = n }
}

// NewChecker constructs a Checker that uses parser to interpret robots.txt
// content and downloader to fetch it.
func NewChecker(parser Parser, downloader Getter, opts ...CheckerOption) *Checker {
	cfg := checkerConfig{cacheTTL: DefaultCacheTTL, cacheSize: DefaultCacheSize}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Checker{
		parser:     parser,
		downloader: downloader,
		cache:      newCache(cfg.cacheTTL, cfg.cacheSize),
		sf:         new(singleflight.Group),
	}
}

// Check returns nil when u is allowed by the host's robots.txt and a non-nil
// error otherwise (blocked, unparseable URL, or robots.txt fetch failure).
func (c *Checker) Check(ctx context.Context, u string) error {
	parsedURL, err := url.Parse(u)
	if err != nil {
		return fmt.Errorf("robotstxt: error parsing url '%s': %w", u, err)
	}

	hostname := parsedURL.Hostname()

	fetchFn := func() (Rules, error) {
		res, err, _ := c.sf.Do(hostname, func() (any, error) {
			baseURL := parsedURL.Scheme + "://" + hostname
			robotsURL := baseURL + "/robots.txt"
			response, err := c.downloader.Get(ctx, robotsURL)
			if err != nil {
				return nil, fmt.Errorf("robotstxt: error downloading '%s': %w", robotsURL, err)
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

			return c.parser.Parse(response.Content)
		})
		if err != nil {
			return nil, err
		}
		if rules, ok := res.(Rules); ok {
			return rules, nil
		}
		return nil, fmt.Errorf("robotstxt: could not convert to Rules: %v", res)
	}

	rules, err := c.cache.getOrFetch(hostname, fetchFn)
	if err != nil {
		return fmt.Errorf("robotstxt: error getting rules from cache for hostname '%s': %w", hostname, err)
	}

	path := parsedURL.RequestURI()
	if rules.IsAllowed(path) {
		return nil
	}

	return fmt.Errorf("robotstxt: blocked url '%s'", u)
}
