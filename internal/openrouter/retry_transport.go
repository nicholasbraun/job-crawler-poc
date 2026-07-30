package openrouter

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LLM request backoff defaults. Retries stay well inside the per-request
// http.Client.Timeout (Config.Timeout / DefaultTimeout), which caps the total via
// the request context, and maxBackoff bounds any single wait so a large Retry-After
// cannot pin a worker for minutes.
const (
	defaultLLMMaxRetries = 4
	defaultLLMBackoff    = 1 * time.Second
	defaultLLMMaxBackoff = 30 * time.Second
)

// newLLMHTTPClient builds the HTTP client both OpenRouter clients use: the given
// end-to-end Timeout plus a retryTransport that rides out transient upstream
// rate-limiting (#116 incident 2026-07-25). Sharing http.DefaultTransport pools
// connections across the extractor and classifier.
func newLLMHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &retryTransport{
			base:       http.DefaultTransport,
			maxRetries: defaultLLMMaxRetries,
			backoff:    defaultLLMBackoff,
			maxBackoff: defaultLLMMaxBackoff,
		},
	}
}

// retryTransport retries a request that comes back rate-limited (429) or with a
// transient upstream error (502/503/504), with exponential backoff that honors a
// Retry-After header. This keeps a worker waiting-and-retrying on a transient
// provider overload instead of returning at once -- which would let the durable
// LLM stream immediately re-deliver the task, storming an already-throttled
// upstream (the observed mistral-nemo "engine_overloaded" 429 storm). Under load
// the workers back off, so it also adaptively caps the outgoing request rate. A
// transport-level error (network/timeout) is returned as-is; only an explicit
// transient status is retried, where a Retry-After hint can guide the wait. After
// maxRetries it returns the last (still-failing) response, so the caller surfaces
// the error exactly as before -- just far less often, and after real backoff.
type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	backoff    time.Duration // initial delay before the first retry
	maxBackoff time.Duration // cap on any single wait (also caps Retry-After)
}

func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	wait := t.backoff
	for attempt := 0; ; attempt++ {
		// Replay the request body on every retry: RoundTrip consumes it, and the
		// OpenRouter requests are built from a bytes.Reader so GetBody yields a
		// fresh one. A body with no GetBody cannot be replayed, so such a request is
		// never retried (below).
		if attempt > 0 {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}

		resp, err := t.base.RoundTrip(req)
		if err != nil {
			return nil, err // a transport error is left to the caller / stream.
		}
		if attempt >= t.maxRetries || !retryableStatus(resp.StatusCode) || req.GetBody == nil {
			return resp, nil
		}

		delay := t.delay(resp, wait)
		drainClose(resp.Body) // free the connection for reuse before waiting
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
		}
		wait *= 2
	}
}

// delay is the wait before the next retry: the response's Retry-After hint when
// present, else the escalating backoff, capped at maxBackoff.
func (t *retryTransport) delay(resp *http.Response, backoff time.Duration) time.Duration {
	wait := backoff
	if ra := retryAfter(resp.Header.Get("Retry-After")); ra > 0 {
		wait = ra
	}
	if t.maxBackoff > 0 && wait > t.maxBackoff {
		wait = t.maxBackoff
	}
	return wait
}

// retryableStatus reports whether an HTTP status is a transient OpenRouter/upstream
// failure worth retrying: 429 (rate limited) or 502/503/504 (upstream overloaded /
// unavailable).
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// retryAfter parses a Retry-After header value -- delay-seconds or an HTTP-date --
// into a non-negative duration, or 0 when absent/unparseable/in the past.
func retryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if when, err := http.ParseTime(h); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// drainClose reads a bounded amount of a response body and closes it so the
// underlying connection can be reused for the retry.
func drainClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<16))
	_ = body.Close()
}
