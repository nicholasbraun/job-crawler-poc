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
	// maxBackoff caps the delay before any single retry, bounding both the
	// escalating exponential backoff and a server's Retry-After hint so a
	// persistently-throttling host cannot stall a run indefinitely. Default: 2m.
	maxBackoff time.Duration
}

func NewRetryClient(httpClient Downloader, opts ...RetryClientOption) *RetryClient {
	retryClient := &RetryClient{
		inner:         httpClient,
		backoff:       2 * time.Second,
		multiplicator: 2,
		maxTries:      5,
		maxBackoff:    2 * time.Minute,
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

// WithMaxBackoff caps the delay before any single retry. A non-positive value
// removes the ceiling, letting the exponential backoff and a server's
// Retry-After hint apply unbounded.
func WithMaxBackoff(mb time.Duration) RetryClientOption {
	return func(client *RetryClient) {
		client.maxBackoff = mb
	}
}

func (rc *RetryClient) Get(ctx context.Context, url string) (*Response, error) {
	currentBackoff := rc.backoff

	for i := 1; i <= rc.maxTries; i++ {
		res, err := rc.inner.Get(ctx, url)
		if !isRetryable(err) {
			return res, err
		}

		// Honor a server-provided Retry-After hint when present; otherwise fall
		// back to exponential backoff. The backoff still advances so that a
		// subsequent hint-less attempt waits the escalated delay.
		wait := currentBackoff
		var statusErr *StatusError
		if errors.As(err, &statusErr) && statusErr.RetryAfter > 0 {
			wait = statusErr.RetryAfter
		}
		if rc.maxBackoff > 0 && wait > rc.maxBackoff {
			wait = rc.maxBackoff
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
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
