package runner

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
)

// --- fakes ---

type fakeRunRepo struct {
	mu   sync.Mutex
	runs map[uuid.UUID]*crawler.CrawlRun
}

func newFakeRunRepo() *fakeRunRepo {
	return &fakeRunRepo{runs: map[uuid.UUID]*crawler.CrawlRun{}}
}

func (f *fakeRunRepo) Create(ctx context.Context, run *crawler.CrawlRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *run
	f.runs[run.ID] = &cp
	return nil
}

func (f *fakeRunRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *run
	return &cp, nil
}

func (f *fakeRunRepo) List(ctx context.Context) ([]*crawler.CrawlRun, error) { return nil, nil }

func (f *fakeRunRepo) ListByStatus(ctx context.Context, statuses ...crawler.RunStatus) ([]*crawler.CrawlRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[crawler.RunStatus]bool{}
	for _, s := range statuses {
		want[s] = true
	}
	out := []*crawler.CrawlRun{}
	for _, run := range f.runs {
		if want[run.Status] {
			cp := *run
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeRunRepo) GetStatus(ctx context.Context, id uuid.UUID) (crawler.RunStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[id]
	if !ok {
		return "", errors.New("not found")
	}
	return run.Status, nil
}

func (f *fakeRunRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status crawler.RunStatus, finishedAt *time.Time, errMsg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	run := f.runs[id]
	run.Status = status
	run.FinishedAt = finishedAt
	run.Error = errMsg
	return nil
}

func (f *fakeRunRepo) UpdateCounters(ctx context.Context, id uuid.UUID, counters crawler.RunCounters) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[id].Counters = counters
	return nil
}

func (f *fakeRunRepo) setStatus(id uuid.UUID, status crawler.RunStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[id].Status = status
}

type fakeDefRepo struct {
	def *crawler.CrawlDefinition
}

func (f *fakeDefRepo) Create(ctx context.Context, def *crawler.CrawlDefinition) error { return nil }
func (f *fakeDefRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlDefinition, error) {
	return f.def, nil
}
func (f *fakeDefRepo) List(ctx context.Context) ([]*crawler.CrawlDefinition, error) { return nil, nil }

// doneFrontier hands out no URLs and immediately reports the work complete.
type doneFrontier struct{}

func (doneFrontier) AddURL(ctx context.Context, url crawler.URL) error { return nil }
func (doneFrontier) Next(ctx context.Context) (crawler.URL, error) {
	return crawler.URL{}, frontier.ErrDone
}
func (doneFrontier) MarkDone(ctx context.Context, url string) error { return nil }

// blockingFrontier never hands out a URL and never drains: Next blocks until the
// run's context is cancelled. It models a perpetual/in-flight crawl so a test
// can stop one run and observe another keep running.
type blockingFrontier struct{}

func (blockingFrontier) AddURL(ctx context.Context, url crawler.URL) error { return nil }
func (blockingFrontier) Next(ctx context.Context) (crawler.URL, error) {
	<-ctx.Done()
	return crawler.URL{}, ctx.Err()
}
func (blockingFrontier) MarkDone(ctx context.Context, url string) error { return nil }

// --- tests ---

func TestTerminalStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus crawler.RunStatus
		wantErrMsg bool
	}{
		{"nil completes", nil, crawler.RunStatusCompleted, false},
		{"stop requested", orchestrator.ErrStopRequested, crawler.RunStatusStopped, false},
		{"context canceled", context.Canceled, crawler.RunStatusStopped, false},
		{"wrapped stop requested", errors.Join(errors.New("ctx"), orchestrator.ErrStopRequested), crawler.RunStatusStopped, false},
		{"other error fails", errors.New("boom"), crawler.RunStatusFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := terminalStatus(tt.err)
			if status != tt.wantStatus {
				t.Errorf("status: got %q, want %q", status, tt.wantStatus)
			}
			if (msg != "") != tt.wantErrMsg {
				t.Errorf("errMsg presence: got %q, want present=%v", msg, tt.wantErrMsg)
			}
		})
	}
}

func TestStopPollerThrottleAndLatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id := uuid.New()
		repo := newFakeRunRepo()
		repo.Create(t.Context(), &crawler.CrawlRun{ID: id, Status: crawler.RunStatusRunning})

		r := New(repo, nil, nil)
		poll := r.newStopPoller(id)

		if poll(t.Context()) {
			t.Fatal("should not stop while running")
		}

		// Flip to stopping but stay inside the throttle window: the cached
		// result must still be false.
		repo.setStatus(id, crawler.RunStatusStopping)
		if poll(t.Context()) {
			t.Fatal("should be throttled (cached false) within the poll interval")
		}

		time.Sleep(statusPollInterval)
		synctest.Wait()
		if !poll(t.Context()) {
			t.Fatal("should observe stopping after the interval elapses")
		}

		// Latched: even if the status flips back, it keeps returning true.
		repo.setStatus(id, crawler.RunStatusRunning)
		if !poll(t.Context()) {
			t.Fatal("should stay latched once stopping was observed")
		}
	})
}

func TestStartRunCompletes(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   doneFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: shouldStop,
		})
		return &Engine{Orchestrator: o, SeedURLs: nil, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != crawler.RunStatusRunning {
		t.Errorf("new run status: got %q, want running", run.Status)
	}

	// Wait for the run to finish on its own (do not Shutdown — that forces a stop).
	final := waitForFinish(t, runs, run.ID)
	if final.Status != crawler.RunStatusCompleted {
		t.Errorf("final status: got %q, want completed", final.Status)
	}
	if final.FinishedAt == nil {
		t.Error("finished run should have FinishedAt set")
	}
}

func TestReconcileAdoptsInterruptedRuns(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	runningID := uuid.New()
	stoppingID := uuid.New()
	terminalID := uuid.New()
	finished := time.Now()
	// A running run (with progress already persisted), a stopping run, and a
	// terminal run that must be ignored by reconcile.
	runs.Create(t.Context(), &crawler.CrawlRun{ID: runningID, DefinitionID: defID, Status: crawler.RunStatusRunning, Counters: crawler.RunCounters{PagesCrawled: 5}})
	runs.Create(t.Context(), &crawler.CrawlRun{ID: stoppingID, DefinitionID: defID, Status: crawler.RunStatusStopping})
	runs.Create(t.Context(), &crawler.CrawlRun{ID: terminalID, DefinitionID: defID, Status: crawler.RunStatusCompleted, FinishedAt: &finished})

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   doneFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: shouldStop,
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	var cleanMu sync.Mutex
	cleaned := map[uuid.UUID]bool{}
	cleaner := func(ctx context.Context, runID uuid.UUID) error {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		cleaned[runID] = true
		return nil
	}

	r := New(runs, defs, factory, WithFrontierCleaner(cleaner))

	if err := r.Reconcile(t.Context()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// The running run resumes and drains to completed; its persisted counters
	// must be seeded (not reset to zero) so the flush doesn't regress them.
	running := waitForFinish(t, runs, runningID)
	if running.Status != crawler.RunStatusCompleted {
		t.Errorf("running run: got %q, want completed", running.Status)
	}
	if running.Counters.PagesCrawled != 5 {
		t.Errorf("adopted counters: got %d pages, want 5 (seeded from persisted)", running.Counters.PagesCrawled)
	}

	// The stopping run resumes, observes its desired-state stop, and drains to
	// stopped.
	stopping := waitForFinish(t, runs, stoppingID)
	if stopping.Status != crawler.RunStatusStopped {
		t.Errorf("stopping run: got %q, want stopped", stopping.Status)
	}

	// The terminal run is not adopted and left exactly as it was.
	terminal, err := runs.Get(t.Context(), terminalID)
	if err != nil {
		t.Fatalf("Get terminal run: %v", err)
	}
	if terminal.Status != crawler.RunStatusCompleted {
		t.Errorf("terminal run: got %q, want completed (untouched)", terminal.Status)
	}

	// Both adopted runs must have their frontier cleaned up on finish; the
	// terminal run, never adopted, must not. Cleanup fires just after the
	// terminal status write, so poll for it.
	waitFor(t, func() bool {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		return cleaned[runningID] && cleaned[stoppingID]
	}, "frontier cleaner to run for both adopted runs")

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if cleaned[terminalID] {
		t.Error("frontier cleaner must not run for the un-adopted terminal run")
	}
}

func TestConcurrentRunsIndependent(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: shouldStop,
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	runA, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start A: %v", err)
	}
	runB, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start B: %v", err)
	}

	// Stopping A drains only A.
	if err := r.Stop(t.Context(), runA.ID); err != nil {
		t.Fatalf("Stop A: %v", err)
	}
	a := waitForFinish(t, runs, runA.ID)
	if a.Status != crawler.RunStatusStopped {
		t.Errorf("run A: got %q, want stopped", a.Status)
	}

	// B is unaffected: it keeps blocking (never finishes on its own).
	b, err := runs.Get(t.Context(), runB.ID)
	if err != nil {
		t.Fatalf("Get B: %v", err)
	}
	if b.FinishedAt != nil {
		t.Errorf("run B should still be running; got status %q finished=%v", b.Status, b.FinishedAt)
	}

	// Drain B so its goroutine exits cleanly.
	r.Shutdown(t.Context())
}

// waitFor polls cond until it returns true or the test times out.
func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitForFinish polls until the run reaches a terminal status (FinishedAt set)
// or the test times out.
func waitForFinish(t *testing.T, runs *fakeRunRepo, id uuid.UUID) *crawler.CrawlRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := runs.Get(t.Context(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if run.FinishedAt != nil {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run did not finish before deadline")
	return nil
}
