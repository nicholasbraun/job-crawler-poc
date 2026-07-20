// Package downloader provides HTTP clients for fetching web pages. The base
// Client handles single requests over a transport with a bounded dial timeout
// and a caching resolver (see resolver.go), so a crawl over many hosts does not
// overwhelm DNS; RetryClient wraps any Downloader with exponential backoff.
package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ErrNoHTML is returned when the response content type is not text/html.
// This error is non-retryable — wrapping it with RetryClient will not retry.
var ErrNoHTML = errors.New("content type is not 'text/html'")

// StatusError is returned when a request completes but the server responds with
// a non-2xx status. Retryable reports whether the status is a transient
// throttle or server error (429, 408, any 5xx) worth retrying, as opposed to a
// permanent client error (e.g. 404, 403, 410) whose body must not be mistaken
// for a real page. Callers drop the URL on a non-retryable StatusError.
//
// RetryAfter holds the delay requested by the server's Retry-After header, or 0
// when the header is absent, malformed, or points to the past. RetryClient
// honors this hint in place of its exponential backoff.
type StatusError struct {
	StatusCode int
	Retryable  bool
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("downloader: unexpected http status %d", e.StatusCode)
}

// retryableStatus reports whether a non-2xx status is transient. Throttles
// (429), request timeouts (408), and any 5xx are worth retrying; every other
// 4xx is a permanent client error.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, http.StatusRequestTimeout:
		return true
	}
	return code >= 500
}

// parseRetryAfter interprets a Retry-After header value, which per RFC 9110 is
// either a non-negative number of seconds (delta-seconds) or an HTTP-date. It
// returns the delay to wait before retrying, or 0 if the header is absent,
// malformed, or already in the past.
func parseRetryAfter(v string) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

type Client struct {
	httpClient            *http.Client
	downloadTimeHistogram metric.Float64Histogram
	userAgent             string
}

// ClientOption configures a Client.
type ClientOption func(*clientConfig)

// clientConfig holds a Client's tunables. The DNS defaults suit a discovery
// crawl over many mostly-once-seen hosts on a shared resolver.
type clientConfig struct {
	timeout       time.Duration // overall per-request budget (dial + TLS + response)
	dialTimeout   time.Duration // bounds a single TCP dial (and, via the cache, DNS)
	dnsPosTTL     time.Duration // how long a successful lookup is cached
	dnsNegTTL     time.Duration // how long a failed lookup is cached (kept shorter)
	dnsTimeout    time.Duration // bounds one underlying DNS resolution
	dnsMaxEntries int           // upper bound on cached hosts

	// transport, when set, is used verbatim instead of building a caching
	// transport from the DNS/dial fields — so a Client can share one transport
	// (hence one DNS cache and connection pool) with other clients.
	transport http.RoundTripper
}

// WithTimeout overrides the overall per-request timeout (default 10s).
func WithTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.timeout = d }
}

// WithDialTimeout bounds a single TCP dial, so a slow or dead host frees a
// worker quickly rather than riding out the overall request timeout (default 5s).
func WithDialTimeout(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.dialTimeout = d }
}

// WithDNSCacheTTL sets how long a successful DNS lookup is cached (default 5m).
func WithDNSCacheTTL(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.dnsPosTTL = d }
}

// WithDNSNegativeTTL sets how long a failed DNS lookup is cached (default 1m).
func WithDNSNegativeTTL(d time.Duration) ClientOption {
	return func(c *clientConfig) { c.dnsNegTTL = d }
}

// WithDNSCacheSize bounds how many hosts the DNS cache holds (default 16384).
func WithDNSCacheSize(n int) ClientOption {
	return func(c *clientConfig) { c.dnsMaxEntries = n }
}

// WithTransport makes the Client use rt instead of building its own caching
// transport, so it can share one transport — and thus one DNS cache and
// connection pool — with other clients (e.g. the robots.txt fetcher).
func WithTransport(rt http.RoundTripper) ClientOption {
	return func(c *clientConfig) { c.transport = rt }
}

func defaultClientConfig() clientConfig {
	return clientConfig{
		timeout:       10 * time.Second,
		dialTimeout:   5 * time.Second,
		dnsPosTTL:     5 * time.Minute,
		dnsNegTTL:     1 * time.Minute,
		dnsTimeout:    5 * time.Second,
		dnsMaxEntries: 16384,
	}
}

// cachingTransport builds the bounded-dial, DNS-caching transport from cfg.
func cachingTransport(cfg *clientConfig) *http.Transport {
	cache := newDNSCache(cfg.dnsPosTTL, cfg.dnsNegTTL, cfg.dnsTimeout, cfg.dnsMaxEntries)
	dialer := &net.Dialer{Timeout: cfg.dialTimeout, KeepAlive: 30 * time.Second}
	return newCachingTransport(dialer, cache)
}

// NewCachingTransport builds an *http.Transport with a bounded dial timeout and
// a caching resolver in front of system DNS. Share one instance across HTTP
// clients — e.g. the page downloader and the robots.txt fetcher — so they use a
// single DNS cache and connection pool, and a host resolved for one is a cache
// hit for the other. Safe for concurrent use.
func NewCachingTransport(opts ...ClientOption) *http.Transport {
	cfg := defaultClientConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return cachingTransport(&cfg)
}

func NewClient(userAgent string, opts ...ClientOption) *Client {
	cfg := defaultClientConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	meter := otel.Meter("http_client")
	downloadTimeHistogram, _ := meter.Float64Histogram("crawler.http-client.downloads.time", metric.WithUnit("ms"))

	transport := cfg.transport
	if transport == nil {
		transport = cachingTransport(&cfg)
	}
	return &Client{
		httpClient: &http.Client{
			Timeout:   cfg.timeout,
			Transport: transport,
		},
		downloadTimeHistogram: downloadTimeHistogram,
		userAgent:             userAgent,
	}
}

func (c *Client) Get(ctx context.Context, u string) (*Response, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("error creating request for url (%s). %w", u, err)
	}

	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", c.userAgent)

	res, err := c.httpClient.Do(req)
	end := time.Now()
	downloadTime := float64(end.Sub(start).Milliseconds())
	if err != nil {
		status, errorType := classifyError(err)
		c.downloadTimeHistogram.Record(ctx, downloadTime, metric.WithAttributes(attribute.String("status", status), attribute.String("errorType", errorType)))
		return nil, fmt.Errorf("error downloading url (%s). %w", u, err)
	}

	defer res.Body.Close()

	// Classify the status before the content-type guard: a throttle (429) is
	// often served as text/plain, so it must be recognized as a transient,
	// retryable failure rather than rejected as non-HTML. A non-2xx body (e.g.
	// a 404 "page not found") must never flow downstream as a real page.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		c.downloadTimeHistogram.Record(ctx, downloadTime, metric.WithAttributes(attribute.String("status", strconv.Itoa(res.StatusCode))))
		return nil, &StatusError{
			StatusCode: res.StatusCode,
			Retryable:  retryableStatus(res.StatusCode),
			RetryAfter: parseRetryAfter(res.Header.Get("Retry-After")),
		}
	}

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

	c.downloadTimeHistogram.Record(ctx, downloadTime, metric.WithAttributes(attribute.String("status", strconv.Itoa(res.StatusCode))))

	return &Response{res.StatusCode, body}, nil
}

func classifyError(err error) (status string, errorType string) {
	if err == nil {
		return "success", ""
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return "error", "timeout"
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "error", "timeout"
		}
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "error", "dns"
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return "error", "connection"
	}

	return "error", "unknown"
}
