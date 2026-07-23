package collection_test

import (
	"context"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// TestDue exercises every branch of the pure due-predicate (ADR-0036): the
// never-run kickoff, the active skip, and the not-active boundary around
// lastStart+interval — including the many-intervals-late case that proves a
// single overdue window yields one true, not a catch-up burst.
func TestDue(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const interval = time.Hour

	tests := []struct {
		name      string
		now       time.Time
		lastStart time.Time
		active    bool
		want      bool
	}{
		{
			name:      "never run and idle is due immediately",
			now:       base,
			lastStart: time.Time{},
			active:    false,
			want:      true,
		},
		{
			name:      "active is never due regardless of elapsed time",
			now:       base.Add(10 * interval),
			lastStart: base,
			active:    true,
			want:      false,
		},
		{
			name:      "active with zero lastStart is not due",
			now:       base.Add(10 * interval),
			lastStart: time.Time{},
			active:    true,
			want:      false,
		},
		{
			name:      "idle before the interval boundary is not due",
			now:       base.Add(interval - time.Second),
			lastStart: base,
			active:    false,
			want:      false,
		},
		{
			name:      "idle exactly at the boundary is due (inclusive)",
			now:       base.Add(interval),
			lastStart: base,
			active:    false,
			want:      true,
		},
		{
			name:      "idle just past the boundary is due",
			now:       base.Add(interval + time.Second),
			lastStart: base,
			active:    false,
			want:      true,
		},
		{
			name:      "idle many intervals late is due once (no catch-up count)",
			now:       base.Add(100 * interval),
			lastStart: base,
			active:    false,
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := collection.Due(tt.now, tt.lastStart, tt.active, interval); got != tt.want {
				t.Errorf("Due(now=%v, lastStart=%v, active=%v, interval=%v) = %v, want %v",
					tt.now, tt.lastStart, tt.active, interval, got, tt.want)
			}
		})
	}
}

// fakeRunStore is an in-memory stand-in for the persisted run history shared by
// the scheduler's LatestRunLookup and Starter ports. A background AfterFunc (in
// the synctest bubble) flips a started run terminal after cycleDur, modelling a
// Cycle's wall-clock duration; cycleDur == 0 completes it immediately. It records
// every start time so tests assert observable cadence, not internal fields.
type fakeRunStore struct {
	mu       sync.Mutex
	runs     []*crawler.CrawlRun
	starts   []time.Time
	cycleDur time.Duration
}

// LatestByDefinition returns a copy of the max-StartedAt run, or nil when empty.
func (s *fakeRunStore) LatestByDefinition(_ context.Context, _ uuid.UUID) (*crawler.CrawlRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest *crawler.CrawlRun
	for _, r := range s.runs {
		if latest == nil || r.StartedAt.After(latest.StartedAt) {
			latest = r
		}
	}
	if latest == nil {
		return nil, nil
	}
	cp := *latest
	return &cp, nil
}

// Start mimics the one-active-run invariant: a start racing a non-terminal run
// gets ErrActiveRunExists; otherwise it appends a running run and schedules it
// terminal after cycleDur.
func (s *fakeRunStore) Start(_ context.Context, defID uuid.UUID) (*crawler.CrawlRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runs {
		if !r.Status.Terminal() {
			return nil, crawler.ErrActiveRunExists
		}
	}
	run := &crawler.CrawlRun{
		ID:           uuid.New(),
		DefinitionID: defID,
		Status:       crawler.RunStatusRunning,
		StartedAt:    time.Now(),
	}
	s.runs = append(s.runs, run)
	s.starts = append(s.starts, run.StartedAt)
	if s.cycleDur <= 0 {
		run.Status = crawler.RunStatusCompleted
	} else {
		time.AfterFunc(s.cycleDur, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			run.Status = crawler.RunStatusCompleted
		})
	}
	return run, nil
}

func (s *fakeRunStore) startTimes() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Time, len(s.starts))
	copy(out, s.starts)
	return out
}

// TestSchedulerStartsOnCadence asserts the loop starts a Cycle once per interval
// when each Cycle finishes well within its window: over ~3h at a 1h cadence it
// fires at ≈ T0, T0+1h, T0+2h.
func TestSchedulerStartsOnCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeRunStore{cycleDur: time.Minute}
		sched := collection.NewScheduler(collection.Config{
			Runs:         store,
			Starter:      store,
			DefinitionID: crawler.CollectionDefinitionID,
			Interval:     time.Hour,
			Poll:         time.Minute,
		})

		ctx, cancel := context.WithCancel(t.Context())
		start := time.Now()
		go sched.Run(ctx)

		// Advance past the third cadence tick but not the fourth, then let the loop
		// settle so every due start has landed.
		time.Sleep(3*time.Hour - time.Minute)
		synctest.Wait()

		cancel()
		synctest.Wait() // let Run observe the cancellation and return (no leaked goroutine)

		starts := store.startTimes()
		if len(starts) != 3 {
			t.Fatalf("start count = %d, want 3 (T0, T0+1h, T0+2h)", len(starts))
		}
		for i, want := range []time.Duration{0, time.Hour, 2 * time.Hour} {
			if got := starts[i].Sub(start); got != want {
				t.Errorf("start %d at %v, want %v after T0", i, got, want)
			}
		}
	})
}

// TestSchedulerResumesAfterOverrunWithoutBurst covers the three cadence
// invariants at once (ADR-0036): a Cycle that overruns two windows (cycleDur=3h,
// interval=1h) is skipped while active, then resumes exactly once right after it
// finishes — never a catch-up burst for the T0+1h and T0+2h windows it missed.
func TestSchedulerResumesAfterOverrunWithoutBurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &fakeRunStore{cycleDur: 3 * time.Hour}
		sched := collection.NewScheduler(collection.Config{
			Runs:         store,
			Starter:      store,
			DefinitionID: crawler.CollectionDefinitionID,
			Interval:     time.Hour,
			Poll:         time.Minute,
		})

		ctx, cancel := context.WithCancel(t.Context())
		start := time.Now()
		go sched.Run(ctx)

		// While the first Cycle is still running (well before it finishes at T0+3h)
		// the scheduler must not have started a second: active ⇒ skipped.
		time.Sleep(3*time.Hour - time.Minute)
		synctest.Wait()
		if got := len(store.startTimes()); got != 1 {
			t.Fatalf("starts during the overrun = %d, want exactly 1 (active cycle skips the missed windows)", got)
		}

		// The Cycle completes at T0+3h; within one poll the scheduler resumes with a
		// single start — not a burst of three for the two windows it slept through.
		time.Sleep(2 * time.Minute)
		synctest.Wait()

		cancel()
		synctest.Wait()

		starts := store.startTimes()
		if len(starts) != 2 {
			t.Fatalf("total starts = %d, want 2 (initial + one resume, no catch-up burst)", len(starts))
		}
		if got := starts[0].Sub(start); got != 0 {
			t.Errorf("first start at %v, want T0", got)
		}
		// The resume lands within one poll of the Cycle finishing at T0+3h.
		if resume := starts[1].Sub(start); resume < 3*time.Hour || resume > 3*time.Hour+time.Minute {
			t.Errorf("resume start at %v after T0, want within [3h, 3h+1poll]", resume)
		}
	})
}
