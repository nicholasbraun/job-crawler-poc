package downloader

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type RetryClientOption func(*RetryClient)

// RetryClient is a Downloader decorator that retries failed requests with
// exponential backoff. Non-retryable errors (e.g., ErrNoHTML, a permanent 4xx,
// an NXDOMAIN DNS failure) fail immediately.
type RetryClient struct {
	inner Downloader
	// backoff is the initial delay before the first retry. Multiplied by
	// multiplicator after each subsequent attempt. Default: 2s.
	backoff       time.Duration
	multiplicator int
	maxTries      int
	// maxBackoff caps the delay before any single retry, bounding the escalating
	// exponential backoff so a persistently-throttling host cannot stall a run
	// indefinitely. It does double duty as the boundary of a Retry-After hint we
	// take seriously: a hint above it ends the attempt sequence rather than being
	// shrunk to fit (ADR-0054). Default: 2m.
	maxBackoff time.Duration

	// retriesAbandoned counts attempt sequences ended early because the server's
	// Retry-After exceeded maxBackoff. Every increment saved at least one request
	// (the check sits below the final-attempt break), so the series reads as a
	// saving rather than a rename of the exhaustion it replaces (ADR-0054). No
	// host attribute: that would open one series per crawled host, and the
	// offending host is already in the terminal error's log line.
	retriesAbandoned metric.Int64Counter
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

	retryClient.retriesAbandoned, _ = otel.Meter("http_client").Int64Counter(
		"crawler.http-client.retries.abandoned",
		metric.WithDescription("Retry sequences ended early because the server's Retry-After exceeded maxBackoff (ADR-0054), by status."),
	)

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
// Retry-After hint apply unbounded. It also disables the abandonment of an
// over-ceiling hint (ADR-0054): with no ceiling there is no such thing as a hint
// we refuse to honour, so it is waited out in full.
func WithMaxBackoff(mb time.Duration) RetryClientOption {
	return func(client *RetryClient) {
		client.maxBackoff = mb
	}
}

func (rc *RetryClient) Get(ctx context.Context, url string) (*Response, error) {
	currentBackoff := rc.backoff

	var lastErr error
	for i := 1; i <= rc.maxTries; i++ {
		res, err := rc.inner.Get(ctx, url)
		if !isRetryable(err) {
			return res, err
		}
		lastErr = err

		// No point backing off after the final attempt: no retry follows it, so
		// sleeping would only pin a worker before the loop exits with the
		// terminal error below.
		if i == rc.maxTries {
			break
		}

		var statusErr *StatusError
		hasStatus := errors.As(err, &statusErr)

		// A hint longer than the ceiling is one we have already decided not to
		// honour, so every remaining attempt would land inside a window the server
		// has just told us has not reset: guaranteed failures, known in advance,
		// from a header we already parsed. End the sequence here rather than
		// shrinking the wait to the ceiling and spending them (ADR-0054). Keyed on
		// the hint alone and never on the computed wait — an exponential backoff
		// that overruns the ceiling is our own escalation and says nothing about
		// when the server will serve us, so it is still capped and retried below.
		// A non-positive maxBackoff documents an unbounded hint, so with no ceiling
		// there is no hint we refuse.
		if rc.maxBackoff > 0 && hasStatus && statusErr.RetryAfter > rc.maxBackoff {
			rc.retriesAbandoned.Add(ctx, 1, metric.WithAttributes(
				attribute.String("status", strconv.Itoa(statusErr.StatusCode)),
			))
			// Keep the URL: pool.go logs only worker_name and err, so this message
			// is the only place it survives to the log. %w-wrap so callers still
			// errors.As their way to the *StatusError, exactly as after an
			// exhaustion.
			return nil, fmt.Errorf("gave up on %s after %d of %d tries: server asked to wait longer than the %s retry ceiling: %w", url, i, rc.maxTries, rc.maxBackoff, err)
		}

		// Honor a server-provided Retry-After hint when present; otherwise fall
		// back to exponential backoff. The backoff still advances so that a
		// subsequent hint-less attempt waits the escalated delay.
		wait := currentBackoff
		if hasStatus && statusErr.RetryAfter > 0 {
			wait = statusErr.RetryAfter
		}
		// Only the exponential escalation can still reach this cap: a hint is
		// either at or below the ceiling and waited in full, or above it and
		// already abandoned above (ADR-0054).
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

	// Every attempt was a retryable failure. Wrap the last one so callers can
	// errors.Is/errors.As it (e.g. to a *StatusError or a timeout) after retries
	// are exhausted. lastErr is nil only in the degenerate maxTries <= 0 case,
	// where the loop never ran and there is no underlying error to wrap.
	if lastErr == nil {
		return nil, fmt.Errorf("could not GET %s after %d tries", url, rc.maxTries)
	}
	return nil, fmt.Errorf("could not GET %s after %d tries: %w", url, rc.maxTries, lastErr)
}

func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrNoHTML) {
		return false
	}

	// A DNS "no such host" (NXDOMAIN) is permanent: the name does not resolve and
	// will not within a run, so retrying only sleeps a worker through backoff to
	// the same failure (now an instant negative-cache hit, see resolver.go).
	// Transient DNS errors (timeout, temporary SERVFAIL) stay retryable and fall
	// through below.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
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
