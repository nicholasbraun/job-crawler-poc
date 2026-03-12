package crawler

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

type URL struct {
	Hostname string
	RawURL   string
}

func ParseURL(base string, u string) (URL, error) {
	if base == "" {
		return URL{}, errors.New("url: cannot parse url with empty base")
	}

	if !strings.HasPrefix(u, "http") && !strings.HasPrefix(u, "/") && u != "" {
		return URL{}, errors.New("url: cannot parse url without schema or relative path")
	}

	parsed, err := url.Parse(base)
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
	}, nil
}

type URLRepository interface {
	Save(ctx context.Context, url string) error
	Visited(ctx context.Context, url string) (bool, error)
}
