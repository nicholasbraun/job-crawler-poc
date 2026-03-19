// Package http is an adapter for the net/http standard library package.
// This is where we download URLs
package http

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

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
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:147.0) Gecko/20100101 Firefox/147.0")

	res, err := c.httpClient.Do(req)
	end := time.Now()
	time := float64(end.Sub(start).Milliseconds())
	if err != nil {
		c.downloadTimeHistogram.Record(ctx, time, metric.WithAttributes(attribute.String("status", "error")))
		return nil, fmt.Errorf("error downloading url (%s). %w", url, err)
	}
	c.downloadTimeHistogram.Record(ctx, time, metric.WithAttributes(attribute.String("status", "success")))
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("error reading the body. %w", err)
	}

	return &Response{res.StatusCode, body}, nil
}
