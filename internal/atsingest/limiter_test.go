package atsingest_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
)

// TestHostLimiterPacesSameKey asserts successive Wait calls for one key are
// spaced by the interval: the first returns immediately, each later one blocks a
// further interval. Virtual time (synctest) makes the spacing exact.
func TestHostLimiterPacesSameKey(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 100 * time.Millisecond
		l := atsingest.NewHostLimiter(interval)
		start := time.Now()

		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Fatalf("first Wait: %v", err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("first Wait blocked %v, want 0 (no prior reservation)", d)
		}

		for i := 1; i <= 3; i++ {
			if err := l.Wait(t.Context(), "greenhouse"); err != nil {
				t.Fatalf("Wait %d: %v", i+1, err)
			}
			if want := time.Duration(i) * interval; time.Since(start) != want {
				t.Errorf("after %d paced Waits elapsed = %v, want %v", i, time.Since(start), want)
			}
		}
	})
}

// TestHostLimiterKeysAreIndependent asserts each provider paces on its own
// schedule: the first Wait on a second key does not wait behind the first key.
func TestHostLimiterKeysAreIndependent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := atsingest.NewHostLimiter(100 * time.Millisecond)
		start := time.Now()

		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Fatalf("greenhouse Wait: %v", err)
		}
		if err := l.Wait(t.Context(), "lever"); err != nil {
			t.Fatalf("lever Wait: %v", err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("first Wait on each key blocked %v total, want 0 (keys are independent)", d)
		}
	})
}

// TestHostLimiterWaitCancels asserts a blocked Wait returns ctx.Err() promptly
// when its context is cancelled, so the pool drains fast on stop.
func TestHostLimiterWaitCancels(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		l := atsingest.NewHostLimiter(time.Hour) // long enough that the second call must block
		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Fatalf("first Wait: %v", err)
		}

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if err := l.Wait(ctx, "greenhouse"); !errors.Is(err, context.Canceled) {
			t.Errorf("Wait error = %v, want context.Canceled", err)
		}
	})
}

// TestHostLimiterCancelledWaitReleasesSlot asserts a Wait cancelled mid-block
// gives its reserved slot back, so the next caller for the same key waits only
// one interval (not two): the cancelled call's tail reservation is rolled back.
func TestHostLimiterCancelledWaitReleasesSlot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const interval = 100 * time.Millisecond
		l := atsingest.NewHostLimiter(interval)

		// First Wait takes the immediate slot; the next reservation is at +interval.
		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Fatalf("first Wait: %v", err)
		}

		// A second Wait would block until +interval; cancel it so it releases its slot.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := l.Wait(ctx, "greenhouse"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Wait error = %v, want context.Canceled", err)
		}

		// The next Wait inherits the slot the cancelled call gave back: one
		// interval after the first, not two. Without the rollback it would be two.
		start := time.Now()
		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Fatalf("third Wait: %v", err)
		}
		if got := time.Since(start); got != interval {
			t.Errorf("Wait after a cancelled Wait blocked %v, want %v (cancelled slot released)", got, interval)
		}
	})
}

// TestHostLimiterNeverBlocks covers the two no-op configurations: a nil limiter
// and a non-positive interval both return immediately.
func TestHostLimiterNeverBlocks(t *testing.T) {
	t.Run("nil limiter", func(t *testing.T) {
		var l *atsingest.HostLimiter
		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Errorf("nil-limiter Wait: %v", err)
		}
	})
	t.Run("non-positive interval", func(t *testing.T) {
		l := atsingest.NewHostLimiter(0)
		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Errorf("zero-interval Wait: %v", err)
		}
		if err := l.Wait(t.Context(), "greenhouse"); err != nil {
			t.Errorf("zero-interval Wait (second call): %v", err)
		}
	})
}
