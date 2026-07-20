package downloader

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// dnsCache is a bounded, TTL'd host->addresses cache in front of a resolver. A
// discovery crawl resolves thousands of distinct hosts — many of them dead
// domains that recur in link spam — and re-resolves each host several times (a
// robots.txt fetch plus every page). Without caching that funnels a burst of
// lookups at a single resolver (Docker's embedded 127.0.0.11 in the container
// case), which serializes and drops queries under load, so adding download
// workers overwhelms DNS instead of lifting throughput.
//
// The cache collapses repeat lookups to memory hits, caches failures negatively
// (a dead domain is not re-resolved every time it reappears), coalesces
// concurrent misses for the same host into a single resolution, and bounds its
// own size so it cannot grow without limit across a long-running crawl.
type dnsCache struct {
	mu       sync.Mutex
	entries  map[string]dnsEntry
	inflight map[string]*dnsFlight

	// resolve performs the actual lookup; overridable in tests. now is the clock,
	// overridable in tests. In production these are net.DefaultResolver and
	// time.Now.
	resolve func(context.Context, string) ([]net.IPAddr, error)
	now     func() time.Time

	// posTTL caps how long a successful result is served from cache; negTTL does
	// the same for a failure (kept shorter so a transiently-dead host recovers).
	// timeout bounds a single underlying resolution. maxEntries bounds the map.
	posTTL     time.Duration
	negTTL     time.Duration
	timeout    time.Duration
	maxEntries int

	lookups metric.Int64Counter
}

// dnsEntry is a cached resolution: addresses on success, err on a cached
// failure (negative cache), valid until expiry.
type dnsEntry struct {
	ips    []net.IPAddr
	err    error
	expiry time.Time
}

// dnsFlight lets concurrent lookups of the same host share one resolution. The
// result fields are written once, before done is closed.
type dnsFlight struct {
	done chan struct{}
	ips  []net.IPAddr
	err  error
}

func newDNSCache(posTTL, negTTL, timeout time.Duration, maxEntries int) *dnsCache {
	r := net.DefaultResolver
	c := &dnsCache{
		entries:    map[string]dnsEntry{},
		inflight:   map[string]*dnsFlight{},
		resolve:    func(ctx context.Context, host string) ([]net.IPAddr, error) { return r.LookupIPAddr(ctx, host) },
		now:        time.Now,
		posTTL:     posTTL,
		negTTL:     negTTL,
		timeout:    timeout,
		maxEntries: maxEntries,
	}
	c.lookups, _ = otel.Meter("http_client").Int64Counter(
		"crawler.http-client.dns.lookups",
		metric.WithDescription("DNS cache lookups by result (hit, negative_hit, coalesced, miss)."),
	)
	return c
}

// lookup returns host's addresses, serving a fresh cache entry when present and
// otherwise resolving once (coalescing concurrent misses). The caller's ctx
// cancels its own wait, but never aborts the shared resolution or poisons the
// cache — so one abandoned request cannot fail a host for the others.
func (c *dnsCache) lookup(ctx context.Context, host string) ([]net.IPAddr, error) {
	c.mu.Lock()
	if e, ok := c.entries[host]; ok && c.now().Before(e.expiry) {
		c.mu.Unlock()
		result := "hit"
		if e.err != nil {
			result = "negative_hit"
		}
		c.record(ctx, result)
		return e.ips, e.err
	}
	f, found := c.inflight[host]
	if !found {
		f = &dnsFlight{done: make(chan struct{})}
		c.inflight[host] = f
		go c.resolveAndCache(host, f)
	}
	c.mu.Unlock()

	if found {
		c.record(ctx, "coalesced")
	} else {
		c.record(ctx, "miss")
	}

	select {
	case <-f.done:
		return f.ips, f.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// resolveAndCache runs one resolution to completion under its own bounded
// timeout (decoupled from any caller's ctx), caches the result positively or
// negatively, and wakes every waiter.
func (c *dnsCache) resolveAndCache(host string, f *dnsFlight) {
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	ips, err := c.resolve(ctx, host)

	c.mu.Lock()
	delete(c.inflight, host)
	ttl := c.posTTL
	if err != nil {
		ttl = c.negTTL
	}
	c.storeLocked(host, dnsEntry{ips: ips, err: err, expiry: c.now().Add(ttl)})
	c.mu.Unlock()

	f.ips, f.err = ips, err
	close(f.done)
}

// storeLocked inserts an entry, first evicting expired then arbitrary entries
// when the cache is at capacity, so it stays bounded across a long crawl.
// Caller holds c.mu.
func (c *dnsCache) storeLocked(host string, e dnsEntry) {
	if len(c.entries) >= c.maxEntries {
		now := c.now()
		for k, v := range c.entries {
			if len(c.entries) < c.maxEntries {
				break
			}
			if now.After(v.expiry) {
				delete(c.entries, k)
			}
		}
		for k := range c.entries {
			if len(c.entries) < c.maxEntries {
				break
			}
			delete(c.entries, k)
		}
	}
	c.entries[host] = e
}

func (c *dnsCache) record(ctx context.Context, result string) {
	c.lookups.Add(ctx, 1, metric.WithAttributes(attribute.String("result", result)))
}

// newCachingTransport builds an http.Transport that resolves each host through
// the cache and dials the returned address with dialer (a bounded dial timeout),
// so a dead or slow host frees a worker in seconds instead of riding the client
// timeout. Idle-connection reuse is widened past the stdlib defaults so warm
// hosts skip both the redial and the lookup. A literal-IP address bypasses the
// cache. TLS (ServerName/verification) is still driven by the request host,
// since the Transport performs the handshake itself over the dialed connection.
func newCachingTransport(dialer *net.Dialer, cache *dnsCache) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConns = 256
	t.MaxIdleConnsPerHost = 4
	t.TLSHandshakeTimeout = 5 * time.Second
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if net.ParseIP(host) != nil {
			return dialer.DialContext(ctx, network, addr)
		}
		ips, err := cache.lookup(ctx, host)
		if err != nil {
			return nil, err
		}
		var firstErr error
		for _, ip := range ips {
			conn, derr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if derr == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = derr
			}
			if ctx.Err() != nil {
				break
			}
		}
		if firstErr == nil {
			firstErr = &net.DNSError{Err: "no addresses returned", Name: host, IsNotFound: true}
		}
		return nil, firstErr
	}
	return t
}
