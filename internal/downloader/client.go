// Package downloader provides HTTP clients for fetching web pages. The base
// Client handles single requests; RetryClient wraps any Downloader with
// exponential backoff.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ErrNoHTML is returned when the response content type is not text/html.
// This error is non-retryable — wrapping it with RetryClient will not retry.
var ErrNoHTML = errors.New("content type is not 'text/html'")

type Client struct {
	httpClient            *http.Client
	downloadTimeHistogram metric.Float64Histogram
}

func NewClient() *Client {
	meter := otel.Meter("http_client")
	downloadTimeHistogram, _ := meter.Float64Histogram("crawler.http-client.downloads.time", metric.WithUnit("ms"))
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		downloadTimeHistogram: downloadTimeHistogram,
	}
}

func (c *Client) Get(ctx context.Context, url string) (*Response, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request for url (%s). %w", url, err)
	}

	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:147.0) Gecko/20100101 Firefox/147.0")

	res, err := c.httpClient.Do(req)
	end := time.Now()
	time := float64(end.Sub(start).Milliseconds())
	if err != nil {
		c.downloadTimeHistogram.Record(ctx, time, metric.WithAttributes(attribute.String("status", "error")))
		return nil, fmt.Errorf("error downloading url (%s). %w", url, err)
	}

	defer res.Body.Close()

	limitReader := io.LimitReader(res.Body, 5000000)
	body, err := io.ReadAll(limitReader)
	if err != nil {
		return nil, fmt.Errorf("error reading the body. %w", err)
	}

	headerContentType := res.Header.Get("content-type")
	const expectedContentType = "text/html"

	if headerContentType == "" {
		if detectedContentType := http.DetectContentType(body); !strings.Contains(detectedContentType, expectedContentType) {
			return nil, fmt.Errorf("downloader: got content-type: %s. %w", detectedContentType, ErrNoHTML)
		}
	} else if !strings.Contains(headerContentType, expectedContentType) {
		return nil, fmt.Errorf("downloader: got content-type: %s. %w", headerContentType, ErrNoHTML)
	}

	c.downloadTimeHistogram.Record(ctx, time, metric.WithAttributes(attribute.String("status", "success")))

	return &Response{res.StatusCode, body}, nil
}
