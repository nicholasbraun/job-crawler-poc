package crawler

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// URL is a parsed crawl target. It is a value type — safe to copy and compare.
// RawURL is always in canonical form (see normalize).
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
	normalize(parsed)

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
	normalize(parsed)

	return URL{
		Hostname: parsed.Hostname(),
		RawURL:   parsed.String(),
		Depth:    base.Depth + 1,
	}, nil
}

// normalize canonicalizes a URL so that trivially-equivalent variants dedup
// to the same string: lowercases scheme and host, drops the fragment, sorts
// query parameters, and strips a trailing slash from non-root paths.
func normalize(u *url.URL) {
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" {
		u.RawQuery = u.Query().Encode()
	}
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
	}
}

// URLRepository tracks which URLs have been seen during a crawl.
type URLRepository interface {
	// Save records that a URL has been seen. Returns true if the URL was new,
	// false if it was already saved.
	Save(ctx context.Context, url string) (bool, error)
	Visited(ctx context.Context, url string) (bool, error)
}
