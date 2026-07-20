package robotstxt

import (
	"context"
	"io"
	"net/http"
	"time"
)

// Response holds the HTTP status code and body returned when fetching a
// robots.txt file.
type Response struct {
	StatusCode int
	// Content is the raw response body, capped at 1 MB.
	Content []byte
}

// RobotsTxtDownloader fetches robots.txt files over HTTP. It satisfies the
// Getter interface.
type RobotsTxtDownloader struct {
	httpClient *http.Client
	userAgent  string
}

// NewRobotsTxtDownloader creates a RobotsTxtDownloader with a 5-second HTTP
// timeout over transport. Pass the crawler's shared caching transport so
// robots.txt lookups reuse the same DNS cache and connection pool as page
// downloads — a host resolved for one is then a cache hit for the other. A nil
// transport falls back to http.DefaultTransport.
func NewRobotsTxtDownloader(userAgent string, transport http.RoundTripper) *RobotsTxtDownloader {
	return &RobotsTxtDownloader{
		httpClient: &http.Client{
			Timeout:   5 * time.Second,
			Transport: transport,
		},
		userAgent: userAgent,
	}
}

// Get fetches url and returns its status code and body. The response body
// is limited to 1 MB to prevent unbounded memory use from malformed responses.
func (d *RobotsTxtDownloader) Get(ctx context.Context, url string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("User-Agent", d.userAgent)

	res, err := d.httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer res.Body.Close()

	limitReader := io.LimitReader(res.Body, 1000000)
	body, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, err
	}

	return &Response{
		StatusCode: res.StatusCode,
		Content:    body,
	}, nil
}
