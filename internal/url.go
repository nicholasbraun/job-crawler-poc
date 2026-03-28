package crawler

import (
	"context"
	"errors"
	"net/url"
)

// URL is a parsed crawl target. It is a value type — safe to copy and compare.
type URL struct {
	Hostname string
	RawURL   string
	// Depth is the number of links followed from a seed URL to reach this URL.
	// Seed URLs have depth 0.
	Depth int
}

func NewURL(u string) (URL, error) {
	if u == "" {
		return URL{}, errors.New("url: cannot create empty url")
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return URL{}, err
	}

	return URL{
		Hostname: parsed.Hostname(),
		RawURL:   parsed.String(),
		Depth:    0,
	}, nil
}

func (base *URL) Parse(u string) (URL, error) {
	parsed, err := url.Parse(base.RawURL)
	if err != nil {
		return URL{}, err
	}
	parsed, err = parsed.Parse(u)
	if err != nil {
		return URL{}, err
	}

	return URL{
		Hostname: parsed.Hostname(),
		RawURL:   parsed.String(),
		Depth:    base.Depth + 1,
	}, nil
}

// URLRepository tracks which URLs have been seen during a crawl.
type URLRepository interface {
	// Save records that a URL has been seen. Returns true if the URL was new,
	// false if it was already saved.
	Save(ctx context.Context, url string) (bool, error)
	Visited(ctx context.Context, url string) (bool, error)
}
