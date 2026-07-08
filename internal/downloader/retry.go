package downloader

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type RetryClientOption func(*RetryClient)

// RetryClient is a Downloader decorator that retries failed requests with
// exponential backoff. Non-retryable errors (e.g., ErrNoHTML) fail immediately.
type RetryClient struct {
	inner Downloader
	// backoff is the initial delay before the first retry. Multiplied by
	// multiplicator after each subsequent attempt. Default: 2s.
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
		if !isRetryable(err) {
			return res, err
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

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoHTML) {
		return false
	}

	// A non-2xx status carries its own transient/permanent verdict; anything
	// else with an error (network failures, timeouts, body read errors) is
	// treated as transient and retried.
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Retryable
	}
	return true
}
