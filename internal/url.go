package crawler

import (
	"context"
	"errors"
	"net/url"
)

type URL struct {
	Hostname string
	RawURL   string
	Depth    int
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

type URLRepository interface {
	Save(ctx context.Context, url string) error
	Visited(ctx context.Context, url string) (bool, error)
}
