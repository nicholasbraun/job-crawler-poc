package downloader

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is a test-controlled clock; now() is read under dnsCache.mu while
// advance() is called from the test goroutine, so it guards its own state.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestCache returns a cache with a fake clock and a stub resolver installed,
// so tests never touch real DNS. The stub records call counts.
func newTestCache(t *testing.T, resolve func(host string) ([]net.IPAddr, error)) (*dnsCache, *fakeClock, *atomic.Int64) {
	t.Helper()
	clk := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	var calls atomic.Int64
	c := newDNSCache(5*time.Minute, time.Minute, 5*time.Second, 1024)
	c.now = clk.now
	c.resolve = func(_ context.Context, host string) ([]net.IPAddr, error) {
		calls.Add(1)
		return resolve(host)
	}
	return c, clk, &calls
}

func ip(s string) []net.IPAddr { return []net.IPAddr{{IP: net.ParseIP(s)}} }

func TestDNSCache(t *testing.T) {
	t.Run("a successful lookup is served from cache within its TTL", func(t *testing.T) {
		c, clk, calls := newTestCache(t, func(string) ([]net.IPAddr, error) { return ip("1.2.3.4"), nil })

		for range 5 {
			got, err := c.lookup(t.Context(), "example.com")
			if err != nil || len(got) != 1 || got[0].IP.String() != "1.2.3.4" {
				t.Fatalf("lookup: got %v, %v", got, err)
			}
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("resolver calls: got %d, want 1 (subsequent lookups should hit the cache)", n)
		}

		clk.advance(5*time.Minute + time.Second) // past posTTL
		if _, err := c.lookup(t.Context(), "example.com"); err != nil {
			t.Fatalf("lookup after expiry: %v", err)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("resolver calls after TTL expiry: got %d, want 2 (should re-resolve)", n)
		}
	})

	t.Run("a failed lookup is negatively cached, then re-tried after negTTL", func(t *testing.T) {
		dnsErr := &net.DNSError{Err: "no such host", Name: "dead.example", IsNotFound: true}
		c, clk, calls := newTestCache(t, func(string) ([]net.IPAddr, error) { return nil, dnsErr })

		for range 4 {
			_, err := c.lookup(t.Context(), "dead.example")
			if !errors.Is(err, dnsErr) {
				t.Fatalf("lookup: got %v, want the DNS error", err)
			}
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("resolver calls: got %d, want 1 (a dead host must not be re-resolved every time)", n)
		}

		clk.advance(time.Minute + time.Second) // past the shorter negTTL
		if _, err := c.lookup(t.Context(), "dead.example"); !errors.Is(err, dnsErr) {
			t.Fatalf("lookup after negTTL: %v", err)
		}
		if n := calls.Load(); n != 2 {
			t.Errorf("resolver calls after negTTL: got %d, want 2", n)
		}
	})

	t.Run("concurrent misses for one host coalesce into a single resolution", func(t *testing.T) {
		release := make(chan struct{})
		c, _, calls := newTestCache(t, func(string) ([]net.IPAddr, error) {
			<-release // hold the leader inside resolve so followers pile up
			return ip("9.9.9.9"), nil
		})

		const n = 20
		var wg sync.WaitGroup
		results := make([][]net.IPAddr, n)
		errs := make([]error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], errs[i] = c.lookup(t.Context(), "busy.example")
			}()
		}
		// Wait until the single leader is inside resolve, then let it finish.
		for calls.Load() == 0 {
			runtime.Gosched()
		}
		close(release)
		wg.Wait()

		if n := calls.Load(); n != 1 {
			t.Errorf("resolver calls: got %d, want 1 (concurrent misses must coalesce)", n)
		}
		for i := range n {
			if errs[i] != nil || len(results[i]) != 1 || results[i][0].IP.String() != "9.9.9.9" {
				t.Fatalf("caller %d: got %v, %v", i, results[i], errs[i])
			}
		}
	})

	t.Run("a caller cancelling its wait neither poisons the cache nor aborts the shared lookup", func(t *testing.T) {
		release := make(chan struct{})
		c, _, calls := newTestCache(t, func(string) ([]net.IPAddr, error) {
			<-release
			return ip("5.6.7.8"), nil
		})

		ctx, cancel := context.WithCancel(context.Background())
		type res struct {
			ips []net.IPAddr
			err error
		}
		ch := make(chan res, 1)
		go func() {
			ips, err := c.lookup(ctx, "slow.example")
			ch <- res{ips, err}
		}()

		// Wait until the lookup has registered its in-flight resolution, then cancel.
		for {
			c.mu.Lock()
			_, inflight := c.inflight["slow.example"]
			c.mu.Unlock()
			if inflight {
				break
			}
			runtime.Gosched()
		}
		cancel()

		if got := <-ch; !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled caller: got err %v, want context.Canceled", got.err)
		}

		// The shared resolution still completes and populates the cache.
		close(release)
		for {
			c.mu.Lock()
			_, cached := c.entries["slow.example"]
			c.mu.Unlock()
			if cached {
				break
			}
			runtime.Gosched()
		}
		got, err := c.lookup(context.Background(), "slow.example")
		if err != nil || len(got) != 1 || got[0].IP.String() != "5.6.7.8" {
			t.Fatalf("cached lookup after cancel: got %v, %v", got, err)
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("resolver calls: got %d, want 1 (one caller's cancel must not trigger a re-resolve)", n)
		}
	})

	t.Run("the cache stays bounded by maxEntries", func(t *testing.T) {
		c, _, _ := newTestCache(t, func(host string) ([]net.IPAddr, error) { return ip("1.1.1.1"), nil })
		c.maxEntries = 3

		for i := range 50 {
			host := "host-" + strconv.Itoa(i) + ".example"
			if _, err := c.lookup(t.Context(), host); err != nil {
				t.Fatalf("lookup %s: %v", host, err)
			}
		}
		c.mu.Lock()
		size := len(c.entries)
		c.mu.Unlock()
		if size > 3 {
			t.Errorf("cache size: got %d, want <= 3 (must evict to stay bounded)", size)
		}
	})
}

func TestCachingTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatalf("parsing test server port: %v", err)
	}

	t.Run("a hostname is resolved through the cache, then the address is dialed", func(t *testing.T) {
		c, _, calls := newTestCache(t, func(string) ([]net.IPAddr, error) { return ip("127.0.0.1"), nil })
		client := &http.Client{Transport: newCachingTransport(&net.Dialer{Timeout: 5 * time.Second}, c)}

		url := "http://unit.test.invalid:" + port + "/"
		for range 3 {
			resp, err := client.Get(url)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status: got %d, want 200", resp.StatusCode)
			}
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("resolver calls across 3 requests: got %d, want 1 (the cache should absorb repeats)", n)
		}
	})

	t.Run("a shared transport shares its DNS cache across clients", func(t *testing.T) {
		// This is what the page downloader and the robots.txt fetcher rely on: one
		// transport instance means one resolution per host serves both.
		c, _, calls := newTestCache(t, func(string) ([]net.IPAddr, error) { return ip("127.0.0.1"), nil })
		transport := newCachingTransport(&net.Dialer{Timeout: 5 * time.Second}, c)
		clientA := &http.Client{Transport: transport}
		clientB := &http.Client{Transport: transport}

		url := "http://shared.test.invalid:" + port + "/"
		for _, cl := range []*http.Client{clientA, clientB, clientA, clientB} {
			resp, err := cl.Get(url)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			resp.Body.Close()
		}
		if n := calls.Load(); n != 1 {
			t.Errorf("resolver calls across two clients sharing a transport: got %d, want 1", n)
		}
	})

	t.Run("a literal IP address bypasses the resolver", func(t *testing.T) {
		c, _, calls := newTestCache(t, func(string) ([]net.IPAddr, error) {
			t.Errorf("resolver must not be called for a literal IP")
			return ip("127.0.0.1"), nil
		})
		client := &http.Client{Transport: newCachingTransport(&net.Dialer{Timeout: 5 * time.Second}, c)}

		resp, err := client.Get(srv.URL) // srv.URL is http://127.0.0.1:PORT
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		resp.Body.Close()
		if n := calls.Load(); n != 0 {
			t.Errorf("resolver calls for a literal-IP URL: got %d, want 0", n)
		}
	})
}
