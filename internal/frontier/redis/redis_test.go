package redis_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
)

func url(host, raw string, depth int) crawler.URL {
	return crawler.URL{Hostname: host, RawURL: raw, Depth: depth}
}

// TestRedisFrontier exercises the behaviors the in-mem reference guarantees,
// against a real Redis (the impl relies on Lua EVAL). Each subtest uses a fresh
// runID so their key namespaces don't collide on the shared container.
func TestRedisFrontier(t *testing.T) {
	client := newTestClient(t)

	t.Run("add then next returns it", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New())
		want := url("a", "http://a/1", 0)
		if err := f.AddURL(t.Context(), want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Errorf("Next: got %+v, want %+v", got, want)
		}
	})

	t.Run("per-domain cooldown ordering", func(t *testing.T) {
		cooldown := 300 * time.Millisecond
		f := redisfrontier.New(client, uuid.New(), redisfrontier.WithCooldown(cooldown))
		first := url("a", "http://a/1", 0)
		second := url("a", "http://a/2", 0)
		if err := f.AddURL(t.Context(), first); err != nil {
			t.Fatalf("AddURL first: %v", err)
		}
		if err := f.AddURL(t.Context(), second); err != nil {
			t.Fatalf("AddURL second: %v", err)
		}

		got1, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next first: %v", err)
		}
		if got1 != first {
			t.Errorf("first pop: got %+v, want %+v", got1, first)
		}
		if err := f.MarkDone(t.Context(), got1.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}

		start := time.Now()
		got2, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next second: %v", err)
		}
		elapsed := time.Since(start)
		if got2 != second {
			t.Errorf("second pop: got %+v, want %+v", got2, second)
		}
		// The same domain must respect the cooldown before its next pop.
		if elapsed < cooldown/2 {
			t.Errorf("second pop returned too soon: %v (cooldown %v)", elapsed, cooldown)
		}
	})

	t.Run("maxDomains cap", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New(), redisfrontier.WithMaxDomains(1))
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		// Same domain, another URL: still within the cap.
		if err := f.AddURL(t.Context(), url("a", "http://a/2", 0)); err != nil {
			t.Fatalf("AddURL a/2: %v", err)
		}
		// A new domain past the cap is rejected.
		err := f.AddURL(t.Context(), url("b", "http://b/1", 0))
		if !errors.Is(err, frontier.ErrMaxDomainLimit) {
			t.Errorf("AddURL b/1: got %v, want %v", err, frontier.ErrMaxDomainLimit)
		}
	})

	t.Run("maxDepth reject", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New(), redisfrontier.WithMaxDepth(2))
		err := f.AddURL(t.Context(), url("a", "http://a/deep", 3))
		if !errors.Is(err, frontier.ErrMaxDepth) {
			t.Errorf("AddURL depth 3: got %v, want %v", err, frontier.ErrMaxDepth)
		}
	})

	t.Run("dedup: repeat url is a no-op", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New())
		u := url("a", "http://a/1", 0)
		if err := f.AddURL(t.Context(), u); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		// Second add of the same URL must not error and must not enqueue again.
		if err := f.AddURL(t.Context(), u); err != nil {
			t.Fatalf("AddURL duplicate: %v", err)
		}

		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != u {
			t.Errorf("Next: got %+v, want %+v", got, u)
		}
		if err := f.MarkDone(t.Context(), got.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		// Only one URL was ever enqueued: the frontier is now done.
		if _, err := f.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Errorf("Next after drain: got %v, want %v", err, frontier.ErrDone)
		}
	})

	t.Run("bounded run drains to ErrDone", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New())
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if err := f.MarkDone(t.Context(), got.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		if _, err := f.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Errorf("Next after drain: got %v, want %v", err, frontier.ErrDone)
		}
	})

	t.Run("perpetual run blocks past drain", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New(),
			redisfrontier.WithMode(frontier.Perpetual),
			redisfrontier.WithPollInterval(50*time.Millisecond),
		)
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()

		// With nothing queued, a perpetual frontier never returns ErrDone; it
		// blocks until the context is cancelled.
		_, err := f.Next(ctx)
		if errors.Is(err, frontier.ErrDone) {
			t.Fatal("perpetual Next returned ErrDone; expected it to block")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Next: got %v, want context deadline exceeded", err)
		}
	})

	t.Run("crash safety: expired lease is reclaimed exactly once", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New(),
			redisfrontier.WithLeaseTTL(300*time.Millisecond),
			redisfrontier.WithPollInterval(50*time.Millisecond),
		)
		want := url("a", "http://a/1", 0)
		if err := f.AddURL(t.Context(), want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		// Take the URL and deliberately never MarkDone (simulates a crashed
		// worker holding the lease).
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Fatalf("first pop: got %+v, want %+v", got, want)
		}

		// A later Next blocks until the lease expires, then reclaims the same
		// URL. Bound it so a broken reclaim fails instead of hanging.
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		reclaimed, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next (reclaim): %v", err)
		}
		if reclaimed != want {
			t.Errorf("reclaimed: got %+v, want %+v", reclaimed, want)
		}

		// Reclaimed exactly once: after marking it done nothing else is queued.
		if err := f.MarkDone(t.Context(), reclaimed.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		if _, err := f.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Errorf("Next after reclaim+done: got %v, want %v (a dup would remain queued)", err, frontier.ErrDone)
		}
	})

	t.Run("resume: a second frontier on the same run sees shared state", func(t *testing.T) {
		runID := uuid.New()
		f1 := redisfrontier.New(client, runID)
		inA := url("a", "http://a/1", 0)
		inB := url("b", "http://b/1", 0)
		if err := f1.AddURL(t.Context(), inA); err != nil {
			t.Fatalf("AddURL a: %v", err)
		}
		if err := f1.AddURL(t.Context(), inB); err != nil {
			t.Fatalf("AddURL b: %v", err)
		}

		// f1 leases the first URL (domain a) but does not finish it.
		leased, err := f1.Next(t.Context())
		if err != nil {
			t.Fatalf("Next f1: %v", err)
		}
		if leased != inA {
			t.Fatalf("f1 pop: got %+v, want %+v", leased, inA)
		}

		// A brand-new frontier for the same run resumes the shared Redis state:
		// it hands out the still-queued URL (domain b) and does not re-hand the
		// URL f1 has leased.
		f2 := redisfrontier.New(client, runID)
		got, err := f2.Next(t.Context())
		if err != nil {
			t.Fatalf("Next f2: %v", err)
		}
		if got != inB {
			t.Errorf("f2 pop: got %+v, want %+v (leased URL must not be re-handed)", got, inB)
		}
	})
}
