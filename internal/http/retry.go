package http

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type RetryClientOption func(*RetryClient)

type RetryClient struct {
	inner         Downloader
	backoff       time.Duration
	multiplicator int
	maxTries      int
}

func NewRetryClient(httpClient Downloader, opts ...RetryClientOption) *RetryClient {
	retryClient := &RetryClient{
		inner:         httpClient,
		backoff:       2 * time.Second,
		multiplicator: 2,
		maxTries:      5,
	}

	for _, fn := range opts {
		fn(retryClient)
	}

	return retryClient
}

func WithBackoff(b time.Duration) RetryClientOption {
	return func(client *RetryClient) {
		client.backoff = b
	}
}

func WithMaxTries(mt int) RetryClientOption {
	return func(client *RetryClient) {
		client.maxTries = mt
	}
}

func WithMultiplicator(m int) RetryClientOption {
	return func(client *RetryClient) {
		client.multiplicator = m
	}
}

func (rc *RetryClient) Get(ctx context.Context, url string) (*Response, error) {
	currentBackoff := rc.backoff

	for i := 1; i <= rc.maxTries; i++ {
		res, err := rc.inner.Get(ctx, url)
		if err == nil && !isRetryable(res, err) {
			return res, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(currentBackoff):
			currentBackoff *= time.Duration(rc.multiplicator)
		}
	}

	return nil, fmt.Errorf("could not GET %s after %d tries", url, rc.maxTries)
}

func isRetryable(res *Response, err error) bool {
	if err != nil {
		// network errors, etc.
		return true
	}

	retryableCodes := []int{
		http.StatusBadGateway,
		http.StatusGatewayTimeout,
		http.StatusInternalServerError,
		http.StatusTooManyRequests,
		http.StatusRequestTimeout,
	}

	for _, c := range retryableCodes {
		if res.StatusCode == c {
			return true
		}
	}

	return false
}
