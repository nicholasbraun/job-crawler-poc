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

	t.Run("cooldown WAIT is bounded by pollInterval", func(t *testing.T) {
		// Domain "a" holds a long cooldown with a URL queued behind it, so Next
		// takes the bestScore>now WAIT branch. Clamping that sleep to
		// pollInterval lets a live worker notice a brand-new eligible domain
		// added mid-sleep, instead of blocking the full cooldown (there is no
		// cross-process wakeup signal like the in-mem frontier's).
		f := redisfrontier.New(client, uuid.New(),
			redisfrontier.WithCooldown(10*time.Second),
			redisfrontier.WithPollInterval(50*time.Millisecond),
		)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		if err := f.AddURL(t.Context(), url("a", "http://a/2", 0)); err != nil {
			t.Fatalf("AddURL a/2: %v", err)
		}
		first, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next a/1: %v", err)
		}
		if err := f.MarkDone(t.Context(), first.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}

		// Add a fresh, immediately-eligible domain while the worker is asleep on
		// domain a's 10s cooldown.
		want := url("b", "http://b/1", 0)
		go func() {
			time.Sleep(100 * time.Millisecond)
			_ = f.AddURL(context.Background(), want)
		}()

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()
		got, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next b/1: %v", err)
		}
		if got != want {
			t.Errorf("got %+v, want %+v", got, want)
		}
		// Far below the 10s cooldown: the WAIT was bounded, not slept to the
		// domain deadline.
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("Next took %v; cooldown WAIT was not bounded by pollInterval", elapsed)
		}
	})

	t.Run("reclaim tracks lease expiry under a long pollInterval", func(t *testing.T) {
		// With a pollInterval far larger than leaseTTL, folding the earliest
		// in-flight lease expiry into the WAIT deadline is what bounds
		// crash-reclaim latency to ~leaseTTL. A tiny cooldown keeps the reclaimed
		// domain immediately eligible so the observed latency is the reclaim,
		// not a politeness delay.
		f := redisfrontier.New(client, uuid.New(),
			redisfrontier.WithLeaseTTL(300*time.Millisecond),
			redisfrontier.WithPollInterval(30*time.Second),
			redisfrontier.WithCooldown(10*time.Millisecond),
		)
		want := url("a", "http://a/1", 0)
		if err := f.AddURL(t.Context(), want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		// Lease it and never MarkDone (crashed worker); its domain queue is now
		// empty, so the next WAIT has no concrete domain deadline and would sleep
		// the full 30s pollInterval without the lease-expiry fold.
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Fatalf("first pop: got %+v, want %+v", got, want)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()
		reclaimed, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next (reclaim): %v", err)
		}
		if reclaimed != want {
			t.Errorf("reclaimed: got %+v, want %+v", reclaimed, want)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("reclaim took %v; WAIT was not bounded by lease expiry", elapsed)
		}
	})

	t.Run("DeleteRun removes all of a run's keys", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		if _, err := f.Next(t.Context()); err != nil {
			t.Fatalf("Next: %v", err)
		}

		pattern := "frontier:" + runID.String() + ":*"
		before, err := client.Keys(t.Context(), pattern).Result()
		if err != nil {
			t.Fatalf("Keys before: %v", err)
		}
		if len(before) == 0 {
			t.Fatal("expected frontier keys to exist before delete")
		}

		if err := redisfrontier.DeleteRun(t.Context(), client, runID); err != nil {
			t.Fatalf("DeleteRun: %v", err)
		}

		after, err := client.Keys(t.Context(), pattern).Result()
		if err != nil {
			t.Fatalf("Keys after: %v", err)
		}
		if len(after) != 0 {
			t.Errorf("expected no frontier keys after delete, got %v", after)
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

	t.Run("Len counts queued plus in-flight URLs", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)

		// A run with no keys reports 0.
		if n, err := redisfrontier.Len(t.Context(), client, runID); err != nil {
			t.Fatalf("Len empty: %v", err)
		} else if n != 0 {
			t.Errorf("empty frontier: want 0, got %d", n)
		}

		// Three URLs across two domains, all queued.
		for _, u := range []crawler.URL{
			url("a", "http://a/1", 0),
			url("a", "http://a/2", 0),
			url("b", "http://b/1", 0),
		} {
			if err := f.AddURL(t.Context(), u); err != nil {
				t.Fatalf("AddURL: %v", err)
			}
		}
		if n, err := redisfrontier.Len(t.Context(), client, runID); err != nil {
			t.Fatalf("Len queued: %v", err)
		} else if n != 3 {
			t.Errorf("three queued urls: want 3, got %d", n)
		}

		// Leasing one URL (not yet MarkDone) moves it from a queue to the
		// in-flight processing set; the total is unchanged.
		leased, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if n, err := redisfrontier.Len(t.Context(), client, runID); err != nil {
			t.Fatalf("Len with lease: %v", err)
		} else if n != 3 {
			t.Errorf("two queued + one in-flight: want 3, got %d", n)
		}

		// Marking it done drops it from the frontier entirely.
		if err := f.MarkDone(t.Context(), leased.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		if n, err := redisfrontier.Len(t.Context(), client, runID); err != nil {
			t.Fatalf("Len after done: %v", err)
		} else if n != 2 {
			t.Errorf("after marking one done: want 2, got %d", n)
		}
	})
}
