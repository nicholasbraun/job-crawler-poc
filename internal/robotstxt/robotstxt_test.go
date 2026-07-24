package robotstxt_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
)

type fakeRules struct {
	allowed    map[string]bool
	crawlDelay time.Duration
}

func (r *fakeRules) IsAllowed(path string) bool {
	return r.allowed[path]
}

func (r *fakeRules) CrawlDelay() time.Duration {
	return r.crawlDelay
}

type fakeParser struct {
	rules robotstxt.Rules
	err   error
}

func (p *fakeParser) Parse(b []byte) (robotstxt.Rules, error) {
	if p.err != nil {
		return nil, p.err
	}
	return p.rules, nil
}

type fakeGetter struct {
	mu       sync.Mutex
	calls    int
	response *robotstxt.Response
	err      error
	// gate, if non-nil, blocks each Get call until the gate channel is closed.
	gate chan struct{}
	// lastCtx records the context of the most recent call. Guarded by mu.
	lastCtx context.Context
}

func (g *fakeGetter) Get(ctx context.Context, url string) (*robotstxt.Response, error) {
	g.mu.Lock()
	g.calls++
	g.lastCtx = ctx
	g.mu.Unlock()

	if g.gate != nil {
		<-g.gate
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if g.err != nil {
		return nil, g.err
	}
	return g.response, nil
}

func (g *fakeGetter) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func TestCheckerCheck(t *testing.T) {
	allowedRules := &fakeRules{allowed: map[string]bool{"/ok": true, "/blocked": false}}

	tests := []struct {
		name         string
		url          string
		response     *robotstxt.Response
		getterErr    error
		parser       *fakeParser
		wantErr      bool
		wantErrMatch string
		wantCalls    int
	}{
		{
			name:      "allowed path returns nil",
			url:       "http://example.com/ok",
			response:  &robotstxt.Response{StatusCode: 200, Content: []byte("body")},
			parser:    &fakeParser{rules: allowedRules},
			wantErr:   false,
			wantCalls: 1,
		},
		{
			name:         "disallowed path returns error",
			url:          "http://example.com/blocked",
			response:     &robotstxt.Response{StatusCode: 200, Content: []byte("body")},
			parser:       &fakeParser{rules: allowedRules},
			wantErr:      true,
			wantErrMatch: "blocked url",
			wantCalls:    1,
		},
		{
			name:      "404 treated as allow-all",
			url:       "http://example.com/anything",
			response:  &robotstxt.Response{StatusCode: 404},
			parser:    &fakeParser{rules: allowedRules},
			wantErr:   false,
			wantCalls: 1,
		},
		{
			name:      "410 treated as allow-all",
			url:       "http://example.com/anything",
			response:  &robotstxt.Response{StatusCode: 410},
			parser:    &fakeParser{rules: allowedRules},
			wantErr:   false,
			wantCalls: 1,
		},
		{
			name:         "500 treated as disallow-all",
			url:          "http://example.com/ok",
			response:     &robotstxt.Response{StatusCode: 500},
			parser:       &fakeParser{rules: allowedRules},
			wantErr:      true,
			wantErrMatch: "blocked url",
			wantCalls:    1,
		},
		{
			name:         "503 treated as disallow-all",
			url:          "http://example.com/ok",
			response:     &robotstxt.Response{StatusCode: 503},
			parser:       &fakeParser{rules: allowedRules},
			wantErr:      true,
			wantErrMatch: "blocked url",
			wantCalls:    1,
		},
		{
			name:         "getter error bubbles up",
			url:          "http://example.com/ok",
			getterErr:    errors.New("boom"),
			parser:       &fakeParser{rules: allowedRules},
			wantErr:      true,
			wantErrMatch: "error downloading",
			wantCalls:    1,
		},
		{
			name:         "parser error bubbles up",
			url:          "http://example.com/ok",
			response:     &robotstxt.Response{StatusCode: 200, Content: []byte("body")},
			parser:       &fakeParser{err: errors.New("parse fail")},
			wantErr:      true,
			wantErrMatch: "parse fail",
			wantCalls:    1,
		},
		{
			name:         "malformed URL short-circuits before fetch",
			url:          "http://[::1",
			parser:       &fakeParser{rules: allowedRules},
			wantErr:      true,
			wantErrMatch: "error parsing url",
			wantCalls:    0,
		},
		{
			name:      "empty hostname still dispatches to getter",
			url:       "http:///only-path",
			response:  &robotstxt.Response{StatusCode: 404},
			parser:    &fakeParser{rules: allowedRules},
			wantErr:   false,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getter := &fakeGetter{response: tt.response, err: tt.getterErr}
			checker := robotstxt.NewChecker(tt.parser, getter)

			err := checker.Check(t.Context(), tt.url)

			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected nil, got %v", err)
			}
			if tt.wantErrMatch != "" && !strings.Contains(err.Error(), tt.wantErrMatch) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErrMatch)
			}
			if got := getter.callCount(); got != tt.wantCalls {
				t.Fatalf("getter calls: got %d want %d", got, tt.wantCalls)
			}
		})
	}
}

func TestCheckerDedupesConcurrentFetches(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		allowed := &fakeRules{allowed: map[string]bool{"/path": true}}
		getter := &fakeGetter{
			response: &robotstxt.Response{StatusCode: 200, Content: []byte("body")},
			gate:     make(chan struct{}),
		}
		checker := robotstxt.NewChecker(&fakeParser{rules: allowed}, getter)

		const N = 10
		errCh := make(chan error, N)
		for range N {
			go func() {
				errCh <- checker.Check(t.Context(), "http://example.com/path")
			}()
		}

		// All goroutines durably blocked: one inside getter on the gate,
		// the rest inside singleflight waiting for the first to finish.
		synctest.Wait()

		close(getter.gate)
		synctest.Wait()

		for range N {
			if err := <-errCh; err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		}
		if got := getter.callCount(); got != 1 {
			t.Fatalf("expected 1 getter call, got %d", got)
		}
	})
}

func TestCheckerCachesPerHost(t *testing.T) {
	allowed := &fakeRules{allowed: map[string]bool{"/a": true, "/b": true}}
	getter := &fakeGetter{response: &robotstxt.Response{StatusCode: 200, Content: []byte("body")}}
	checker := robotstxt.NewChecker(&fakeParser{rules: allowed}, getter)

	for _, u := range []string{
		"http://example.com/a",
		"http://example.com/b",
		"http://example.com/a",
	} {
		if err := checker.Check(t.Context(), u); err != nil {
			t.Fatalf("unexpected err for %s: %v", u, err)
		}
	}
	if got := getter.callCount(); got != 1 {
		t.Fatalf("expected 1 call after three same-host checks, got %d", got)
	}

	if err := checker.Check(t.Context(), "http://other.example.com/a"); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got := getter.callCount(); got != 2 {
		t.Fatalf("expected 2 calls after adding a second host, got %d", got)
	}
}

// TestCheckerCancelledCallerStillReturnsCancellation confirms a caller's own wait
// is cancellable: cancelling the context passed to Check unblocks it promptly with
// context.Canceled rather than hanging on the shared fetch.
func TestCheckerCancelledCallerStillReturnsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		getter := &fakeGetter{
			response: &robotstxt.Response{StatusCode: 200, Content: []byte("body")},
			gate:     make(chan struct{}),
		}
		checker := robotstxt.NewChecker(&fakeParser{rules: &fakeRules{}}, getter)

		ctx, cancel := context.WithCancel(t.Context())
		errCh := make(chan error, 1)
		go func() { errCh <- checker.Check(ctx, "http://example.com/path") }()
		synctest.Wait() // fetch goroutine durably blocked in the gated getter

		// Cancel the caller while the fetch is still in flight: its wait must unblock.
		cancel()
		synctest.Wait()
		if err := <-errCh; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled caller: got %v, want context.Canceled", err)
		}

		close(getter.gate) // let the decoupled fetch drain so no goroutine lingers
	})
}

// TestCheckerDecouplesFetchFromCallerCancellation is the core of the singleflight
// hardening: when the worker that owns a shared /robots.txt fetch cancels mid-fetch,
// its own Check unblocks with context.Canceled, but the fetch keeps running on its
// decoupled context and still serves the co-sharers waiting on it — one shared
// fetch serves the whole group instead of the owner's cancellation failing everyone.
func TestCheckerDecouplesFetchFromCallerCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		allowed := &fakeRules{allowed: map[string]bool{"/path": true}}
		getter := &fakeGetter{
			response: &robotstxt.Response{StatusCode: 200, Content: []byte("body")},
			gate:     make(chan struct{}),
		}
		checker := robotstxt.NewChecker(&fakeParser{rules: allowed}, getter)

		const u = "http://example.com/path"

		// Owner A starts the shared fetch and blocks inside the gated getter.
		ctxA, cancelA := context.WithCancel(t.Context())
		errA := make(chan error, 1)
		go func() { errA <- checker.Check(ctxA, u) }()
		synctest.Wait() // A's fetch goroutine is durably blocked in the getter

		// Co-sharer B joins the same in-flight fetch under a live context.
		errB := make(chan error, 1)
		go func() { errB <- checker.Check(t.Context(), u) }()
		synctest.Wait() // B is durably blocked waiting on the shared result

		// Cancel A mid-fetch: only A's own wait unblocks, with context.Canceled.
		cancelA()
		synctest.Wait()
		if err := <-errA; !errors.Is(err, context.Canceled) {
			t.Fatalf("owner Check: got %v, want context.Canceled", err)
		}

		// The fetch is decoupled from A, so releasing the gate lets it complete and
		// serve B — A's cancellation did not poison the group.
		close(getter.gate)
		synctest.Wait()
		if err := <-errB; err != nil {
			t.Fatalf("co-sharer Check: got %v, want nil (shared fetch served the group)", err)
		}
		if got := getter.callCount(); got != 1 {
			t.Fatalf("getter calls: got %d, want 1 (one shared fetch, coalesced)", got)
		}
	})
}

func TestCheckerEvictsAtCacheSize(t *testing.T) {
	allowed := &fakeRules{allowed: map[string]bool{"/a": true}}
	getter := &fakeGetter{response: &robotstxt.Response{StatusCode: 200, Content: []byte("body")}}
	checker := robotstxt.NewChecker(&fakeParser{rules: allowed}, getter, robotstxt.WithCacheSize(1))

	check := func(u string) {
		if err := checker.Check(t.Context(), u); err != nil {
			t.Fatalf("unexpected err for %s: %v", u, err)
		}
	}

	// A single-host cache: the first check fetches, the second is a cache hit.
	check("http://a.example.com/a")
	check("http://a.example.com/a")
	if got := getter.callCount(); got != 1 {
		t.Fatalf("same-host recheck: got %d calls, want 1", got)
	}

	// A second distinct host overflows the cap of 1 and evicts the first.
	check("http://b.example.com/a")
	if got := getter.callCount(); got != 2 {
		t.Fatalf("second host: got %d calls, want 2", got)
	}

	// The first host was evicted, so re-checking it fetches again rather than
	// serving a stale-but-resident entry — the cache stayed bounded.
	check("http://a.example.com/a")
	if got := getter.callCount(); got != 3 {
		t.Fatalf("re-check of evicted host: got %d calls, want 3", got)
	}
}

func TestCheckerRefetchesAfterTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		allowed := &fakeRules{allowed: map[string]bool{"/path": true}}
		getter := &fakeGetter{response: &robotstxt.Response{StatusCode: 200, Content: []byte("body")}}
		checker := robotstxt.NewChecker(&fakeParser{rules: allowed}, getter, robotstxt.WithCacheTTL(time.Hour))

		const u = "http://example.com/path"

		// Two checks within the TTL: the second is served from cache.
		if err := checker.Check(t.Context(), u); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if err := checker.Check(t.Context(), u); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got := getter.callCount(); got != 1 {
			t.Fatalf("within TTL: got %d calls, want 1", got)
		}

		// Advance past the TTL; the entry is now stale and must be re-fetched.
		time.Sleep(time.Hour + time.Minute)

		if err := checker.Check(t.Context(), u); err != nil {
			t.Fatalf("unexpected err after TTL: %v", err)
		}
		if got := getter.callCount(); got != 2 {
			t.Fatalf("after TTL expiry: got %d calls, want 2", got)
		}
	})
}

func TestCheckerReprobesUnavailableUnderShortTTL(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// A 5xx robots.txt is an RFC 9309 disallow-all, but a transient one: it
		// must be cached only under the short unavailable TTL so a server blip does
		// not lock the host out for the full positive TTL (default 1h).
		getter := &fakeGetter{response: &robotstxt.Response{StatusCode: 503}}
		checker := robotstxt.NewChecker(&fakeParser{}, getter)

		const u = "http://example.com/ok"

		// First probe hits the 5xx: blocked, and the verdict is cached.
		if err := checker.Check(t.Context(), u); err == nil {
			t.Fatalf("expected blocked on 5xx, got nil")
		}

		// Still inside the short unavailable TTL: served from cache, no re-fetch.
		time.Sleep(robotstxt.DefaultUnavailableTTL - time.Minute)
		if err := checker.Check(t.Context(), u); err == nil {
			t.Fatalf("expected blocked on 5xx within unavailable TTL, got nil")
		}
		if got := getter.callCount(); got != 1 {
			t.Fatalf("within unavailable TTL: got %d calls, want 1", got)
		}

		// Past the short unavailable TTL but far inside the 1h positive TTL: had the
		// 5xx been cached under the positive TTL this would still be a hit. It must
		// re-probe instead.
		time.Sleep(2 * time.Minute)
		if err := checker.Check(t.Context(), u); err == nil {
			t.Fatalf("expected blocked on 5xx after unavailable TTL, got nil")
		}
		if got := getter.callCount(); got != 2 {
			t.Fatalf("after unavailable TTL expiry: got %d calls, want 2", got)
		}
	})
}
