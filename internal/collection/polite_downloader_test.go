package collection_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// fakeRobots is an inline robotsChecker: Check returns f.err (nil = allow) and
// counts calls.
type fakeRobots struct {
	err   error
	calls int
}

func (f *fakeRobots) Check(_ context.Context, _ string) error {
	f.calls++
	return f.err
}

// spyLimiter is an inline hostLimiter recording the keys it was asked to space.
// onWait, if set, runs inside Wait (used to assert ordering: inner not yet
// called at wait time). When ctxAware is set, Wait blocks on ctx.Done() so a
// pre-cancelled ctx surfaces as ctx.Err().
type spyLimiter struct {
	mu       sync.Mutex
	keys     []string
	err      error
	onWait   func()
	ctxAware bool
}

func (s *spyLimiter) Wait(ctx context.Context, key string) error {
	s.mu.Lock()
	s.keys = append(s.keys, key)
	s.mu.Unlock()
	if s.onWait != nil {
		s.onWait()
	}
	if s.ctxAware {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}

func (s *spyLimiter) waited() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.keys...)
}

// TestPoliteDownloaderRobotsBlockShortCircuits asserts a robots re-check block
// short-circuits with ErrRobotsDisallowed BEFORE any spacing wait or inner GET,
// and fires the onRobotsBlocked hook once.
func TestPoliteDownloaderRobotsBlockShortCircuits(t *testing.T) {
	const url = "https://jobs.acme.com/j/1"
	inner := newFakeDownloader()
	inner.ok(url, "body") // a wrongful inner call would return a 200, not the sentinel
	robots := &fakeRobots{err: errors.New("disallow")}
	spy := &spyLimiter{}
	hookFired := false
	d := collection.NewPoliteDownloader(inner, robots, spy, func(context.Context) { hookFired = true })

	resp, err := d.Get(t.Context(), url)

	if !errors.Is(err, collection.ErrRobotsDisallowed) {
		t.Errorf("err = %v, want ErrRobotsDisallowed", err)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil on a robots block", resp)
	}
	if got := inner.requested(); len(got) != 0 {
		t.Errorf("inner GET called %v, want not called on a robots block", got)
	}
	if got := spy.waited(); len(got) != 0 {
		t.Errorf("limiter waited on %v, want no wait on a robots block", got)
	}
	if !hookFired {
		t.Error("onRobotsBlocked hook did not fire")
	}
}

// TestPoliteDownloaderAllowWaitsThenFetches asserts an allowed URL is spaced
// per registrable domain (eTLD+1) and only then fetched — the wait-before-GET
// order — with the hook untouched.
func TestPoliteDownloaderAllowWaitsThenFetches(t *testing.T) {
	const url = "https://jobs.acme.com/x"
	inner := newFakeDownloader()
	inner.ok(url, "body")
	robots := &fakeRobots{} // allow
	spy := &spyLimiter{}
	spy.onWait = func() {
		if got := inner.requested(); len(got) != 0 {
			t.Errorf("inner GET called %v before the spacing wait, want wait first", got)
		}
	}
	hookFired := false
	d := collection.NewPoliteDownloader(inner, robots, spy, func(context.Context) { hookFired = true })

	resp, err := d.Get(t.Context(), url)

	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp == nil || resp.StatusCode != 200 {
		t.Fatalf("resp = %+v, want the stubbed 200", resp)
	}
	// jobs.acme.com folds to its registrable domain acme.com.
	if got := spy.waited(); len(got) != 1 || got[0] != "acme.com" {
		t.Errorf("spacing keys = %v, want [acme.com] (eTLD+1 fold)", got)
	}
	// Robots is checked exactly once per logical request (ADR-0040): the decorator
	// sits outside the retry client, so re-attempts self-space without re-fetching.
	if robots.calls != 1 {
		t.Errorf("robots checked %d times, want exactly 1 (once per logical request)", robots.calls)
	}
	if got := inner.requested(); len(got) != 1 || got[0] != url {
		t.Errorf("inner requested = %v, want one GET of %q", got, url)
	}
	if hookFired {
		t.Error("onRobotsBlocked fired on an allowed fetch, want not fired")
	}
}

// TestPoliteDownloaderCtxCancelledMidWait asserts a cancelled spacing wait
// surfaces the context error and never calls inner.
func TestPoliteDownloaderCtxCancelledMidWait(t *testing.T) {
	const url = "https://jobs.acme.com/x"
	inner := newFakeDownloader()
	inner.ok(url, "body")
	robots := &fakeRobots{} // allow, so we reach the limiter
	spy := &spyLimiter{ctxAware: true}
	d := collection.NewPoliteDownloader(inner, robots, spy, nil)

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancelled

	resp, err := d.Get(ctx, url)

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if resp != nil {
		t.Errorf("resp = %+v, want nil on a cancelled wait", resp)
	}
	if got := inner.requested(); len(got) != 0 {
		t.Errorf("inner GET called %v, want not called on a cancelled wait", got)
	}
}
