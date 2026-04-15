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

type ctxRecordingGetter struct {
	receivedCtxErr error
	response       *robotstxt.Response
}

func (g *ctxRecordingGetter) Get(ctx context.Context, url string) (*robotstxt.Response, error) {
	g.receivedCtxErr = ctx.Err()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return g.response, nil
}

func TestCheckerPropagatesCtxCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	getter := &ctxRecordingGetter{response: &robotstxt.Response{StatusCode: 200}}
	checker := robotstxt.NewChecker(&fakeParser{}, getter)

	err := checker.Check(ctx, "http://example.com/")
	if err == nil {
		t.Fatalf("expected error from cancelled ctx, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in chain, got %v", err)
	}
	if getter.receivedCtxErr == nil {
		t.Fatalf("expected getter to observe cancelled ctx, got nil")
	}
}
