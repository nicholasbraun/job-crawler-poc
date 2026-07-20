package redis_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// encodeTestMember mirrors the unexported encodeMember layout
// (depth\x1fhostname\x1fscope\x1fowner\x1furl) so a test can arrange raw
// inflight/queue state the public API cannot construct.
func encodeTestMember(depth int, host, scope, owner, raw string) string {
	return strconv.Itoa(depth) + "\x1f" + host + "\x1f" + scope + "\x1f" + owner + "\x1f" + raw
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

	t.Run("provenance survives add then next", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New())
		want := crawler.URL{
			Hostname: "a",
			RawURL:   "http://a/1",
			Depth:    0,
			Scope:    "a.example",
			Owner:    "greenhouse:a",
		}
		if err := f.AddURL(t.Context(), want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		// Struct compare covers Scope/Owner because URL stays a comparable value.
		if got != want {
			t.Errorf("Next: got %+v, want %+v", got, want)
		}
	})

	t.Run("provenance survives a reclaimed lease", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New(),
			redisfrontier.WithLeaseTTL(300*time.Millisecond),
			redisfrontier.WithPollInterval(50*time.Millisecond),
		)
		want := crawler.URL{
			Hostname: "a",
			RawURL:   "http://a/1",
			Depth:    0,
			Scope:    "a.example",
			Owner:    "greenhouse:a",
		}
		if err := f.AddURL(t.Context(), want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		// Lease it and never MarkDone (crashed worker); the reclaim path
		// re-enqueues the exact member, so Scope/Owner must survive it.
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Fatalf("first pop: got %+v, want %+v", got, want)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		reclaimed, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next (reclaim): %v", err)
		}
		if reclaimed != want {
			t.Errorf("reclaimed: got %+v, want %+v", reclaimed, want)
		}
	})

	t.Run("a legacy pre-provenance member yields an actionable error, not a crash", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id)
		prefix := "frontier:" + id.String() + ":"
		// Simulate frontier state written by a pre-#118 binary: a 3-field member
		// (depth\x1fhostname\x1furl, no scope/owner) queued with its domain eligible.
		legacy := "0\x1facme.com\x1fhttp://acme.com/legacy"
		if err := client.Do(ctx, "LPUSH", prefix+"q:acme.com", legacy).Err(); err != nil {
			t.Fatalf("seed legacy member: %v", err)
		}
		if err := client.Do(ctx, "ZADD", prefix+"domains", 0, "acme.com").Err(); err != nil {
			t.Fatalf("seed domain: %v", err)
		}

		_, err := f.Next(ctx)
		if err == nil {
			t.Fatal("Next: want an error for a legacy member, got nil")
		}
		if errors.Is(err, frontier.ErrDone) {
			t.Fatalf("Next: want a format error, got ErrDone")
		}
		if !strings.Contains(err.Error(), "provenance format") {
			t.Errorf("Next error should explain the incompatible format, got: %v", err)
		}
		// The member is put back, not lost, so an operator can flush and restart
		// without dropping URLs.
		if n, err := client.Do(ctx, "LLEN", prefix+"q:acme.com").Int64(); err != nil {
			t.Fatalf("LLEN: %v", err)
		} else if n != 1 {
			t.Errorf("legacy member should be put back on the queue, LLEN = %d, want 1", n)
		}
		// The domain keeps queued (bad) work, so the non-empty-domains invariant
		// requires it to stay in the schedule; a fresh cooldown throttles the error
		// loop until an operator flushes. (Read the raw domains ZSET directly: the
		// #156 domains.size gauge that will express this invariant lands after #154.)
		if err := client.Do(ctx, "ZSCORE", prefix+"domains", "acme.com").Err(); err != nil {
			t.Errorf("BADMEMBER domain must remain scheduled (queue non-empty), ZSCORE err: %v", err)
		}
	})

	t.Run("cooldown resets when a drained domain is refilled", func(t *testing.T) {
		// The keystone invariant proof: draining a domain's only URL must ZREM it
		// from the schedule, so a URL added back inside the cooldown window re-enters
		// as immediately eligible (ADR-0026's accepted trade). Under the old
		// unconditional ZADD-on-drain this second pop would WAIT ~cooldown.
		cooldown := 2 * time.Second
		f := redisfrontier.New(client, uuid.New(), redisfrontier.WithCooldown(cooldown))

		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		got1, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next a/1: %v", err)
		}
		if err := f.MarkDone(t.Context(), got1.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}

		// Refill the drained domain well inside its 2s cooldown window.
		second := url("a", "http://a/2", 0)
		if err := f.AddURL(t.Context(), second); err != nil {
			t.Fatalf("AddURL a/2: %v", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()
		got2, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next a/2: %v", err)
		}
		elapsed := time.Since(start)
		if got2 != second {
			t.Errorf("second pop: got %+v, want %+v", got2, second)
		}
		// Immediately eligible: nowhere near the 2s cooldown.
		if elapsed >= time.Second {
			t.Errorf("refilled domain was not immediately eligible: pop took %v (cooldown %v)", elapsed, cooldown)
		}
	})

	t.Run("pop wakes at the earliest future domain deadline", func(t *testing.T) {
		// A domain with remaining queued work is scheduled a cooldown out; when it
		// is the only work, the WAIT must fold in that future domain deadline rather
		// than blind-poll the (far larger) pollInterval.
		f := redisfrontier.New(client, uuid.New(),
			redisfrontier.WithCooldown(200*time.Millisecond),
			redisfrontier.WithPollInterval(5*time.Second),
		)
		if err := f.AddURL(t.Context(), url("a", "http://a/1", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		second := url("a", "http://a/2", 0)
		if err := f.AddURL(t.Context(), second); err != nil {
			t.Fatalf("AddURL a/2: %v", err)
		}
		first, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next a/1: %v", err)
		}
		if err := f.MarkDone(t.Context(), first.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		defer cancel()
		start := time.Now()
		got, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next a/2: %v", err)
		}
		elapsed := time.Since(start)
		if got != second {
			t.Errorf("second pop: got %+v, want %+v", got, second)
		}
		// Below the 5s pollInterval (so it did not blind-poll) but not instant (so
		// it actually waited the ~200ms domain deadline).
		if elapsed >= 2*time.Second {
			t.Errorf("Next took %v; WAIT did not fold in the future domain deadline", elapsed)
		}
		if elapsed < 100*time.Millisecond {
			t.Errorf("Next took %v; domain cooldown was not enforced before the second pop", elapsed)
		}
	})

	t.Run("expired leases are reclaimed in bounded batches, exactly once", func(t *testing.T) {
		// n > maxReclaim (256) forces at least two reclaim batches across pops, so
		// this exercises the cross-pop bound: every lease is returned to its queue
		// and handed out exactly once, none lost, none double-handed. The per-pop
		// cap itself is a structural guarantee of the Lua constant, not observable
		// through Next; the assertions target the outcomes the bound must produce.
		const n = 300
		id := uuid.New()
		f := redisfrontier.New(client, id)
		prefix := "frontier:" + id.String() + ":"
		ctx := t.Context()

		// Arrange raw expired leases across n distinct hostnames (each drains with
		// no cross-domain cooldown interplay) — state the public API cannot build.
		want := make(map[string]bool, n)
		pastMs := time.Now().Add(-time.Minute).UnixMilli()
		for i := 0; i < n; i++ {
			raw := fmt.Sprintf("http://h%d.example/x", i)
			member := encodeTestMember(0, fmt.Sprintf("h%d.example", i), "", "", raw)
			if err := client.Do(ctx, "HSET", prefix+"inflight", raw, member).Err(); err != nil {
				t.Fatalf("HSET inflight: %v", err)
			}
			if err := client.Do(ctx, "ZADD", prefix+"processing", pastMs, raw).Err(); err != nil {
				t.Fatalf("ZADD processing: %v", err)
			}
			want[raw] = true
		}

		drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		got := make(map[string]int, n)
		for {
			u, err := f.Next(drainCtx)
			if errors.Is(err, frontier.ErrDone) {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			got[u.RawURL]++
			if err := f.MarkDone(drainCtx, u.RawURL); err != nil {
				t.Fatalf("MarkDone: %v", err)
			}
		}

		if len(got) != n {
			t.Errorf("reclaimed %d distinct URLs, want %d", len(got), n)
		}
		for raw := range want {
			switch got[raw] {
			case 0:
				t.Errorf("URL %q was never reclaimed", raw)
			case 1:
				// exactly once
			default:
				t.Errorf("URL %q handed out %d times, want 1", raw, got[raw])
			}
		}
	})

	t.Run("a stale-domain backlog self-heals while real work drains", func(t *testing.T) {
		// Reproduces a pre-fix run's domain bloat: staleCount domains scheduled with
		// no queue behind them, at scores far below the real work. staleCount >
		// maxPrune forces several bounded prune pops. Because every stale score sorts
		// before the real domains (added at now), draining cannot reach the real URLs
		// until all stale domains are pruned — so terminating within the ctx is proof
		// the prune works, and a non-pruning pop would loop on the lowest stale domain
		// forever.
		const staleCount = 1000
		id := uuid.New()
		f := redisfrontier.New(client, id)
		prefix := "frontier:" + id.String() + ":"
		ctx := t.Context()

		for i := 0; i < staleCount; i++ {
			if err := client.Do(ctx, "ZADD", prefix+"domains", int64(i), fmt.Sprintf("stale%d.example", i)).Err(); err != nil {
				t.Fatalf("ZADD stale domain: %v", err)
			}
		}

		real := []crawler.URL{
			url("r0", "http://r0.example/x", 0),
			url("r1", "http://r1.example/x", 0),
			url("r2", "http://r2.example/x", 0),
		}
		for _, u := range real {
			if err := f.AddURL(ctx, u); err != nil {
				t.Fatalf("AddURL %q: %v", u.RawURL, err)
			}
		}

		drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		got := make(map[string]int, len(real))
		for {
			u, err := f.Next(drainCtx)
			if errors.Is(err, frontier.ErrDone) {
				break
			}
			if err != nil {
				t.Fatalf("Next: %v", err)
			}
			got[u.RawURL]++
			if err := f.MarkDone(drainCtx, u.RawURL); err != nil {
				t.Fatalf("MarkDone: %v", err)
			}
		}

		for _, u := range real {
			if got[u.RawURL] != 1 {
				t.Errorf("real URL %q handed out %d times, want 1", u.RawURL, got[u.RawURL])
			}
		}
		if len(got) != len(real) {
			t.Errorf("handed out %d distinct URLs, want %d", len(got), len(real))
		}
		// Invariant end-state: after full drain the schedule holds only non-empty
		// domains, and there are none. (Direct ZCARD read stands in for the #156
		// domains.size gauge, which lands after #154.)
		if card, err := client.ZCard(ctx, prefix+"domains").Result(); err != nil {
			t.Fatalf("ZCard domains: %v", err)
		} else if card != 0 {
			t.Errorf("domains schedule should be empty after drain, ZCARD = %d", card)
		}
	})
}
