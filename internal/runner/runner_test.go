package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
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
	// nextUpdateStatusErr, when non-nil, makes the next UpdateStatus call return
	// it without mutating state, then clears itself — a one-shot injected
	// status-write failure. Guarded by mu.
	nextUpdateStatusErr error
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
	if f.nextUpdateStatusErr != nil {
		err := f.nextUpdateStatusErr
		f.nextUpdateStatusErr = nil
		return err
	}
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

func (f *fakeRunRepo) failNextUpdateStatus(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextUpdateStatusErr = err
}

type fakeDefRepo struct {
	def *crawler.CrawlDefinition
}

func (f *fakeDefRepo) Create(ctx context.Context, def *crawler.CrawlDefinition) error { return nil }
func (f *fakeDefRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlDefinition, error) {
	return f.def, nil
}
func (f *fakeDefRepo) List(ctx context.Context) ([]*crawler.CrawlDefinition, error) { return nil, nil }
func (f *fakeDefRepo) Delete(ctx context.Context, id uuid.UUID) error               { return nil }

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

// hangFrontier blocks Next until its release channel is closed, ignoring
// context cancellation entirely. It models a worker stuck in a call that does
// not honor ctx, so a run's WaitGroup entry never completes on stop.
type hangFrontier struct{ release chan struct{} }

func (hangFrontier) AddURL(ctx context.Context, url crawler.URL) error { return nil }
func (h hangFrontier) Next(ctx context.Context) (crawler.URL, error) {
	<-h.release
	return crawler.URL{}, context.Canceled
}
func (hangFrontier) MarkDone(ctx context.Context, url string) error { return nil }

// timeoutFrontier models the redis frontier when Next is cancelled mid-read:
// go-redis aborts the in-flight socket read via a past deadline, so Next returns
// a net i/o timeout (os.ErrDeadlineExceeded), NOT a wrapped context.Canceled.
// This is the failure shape (see #32) that supervise must key off the run
// context to classify, since the error itself is not context.Canceled. entered,
// if non-nil, is closed the first time Next is reached so a test can wait until
// the crawl loop is blocked inside the read before cancelling it.
type timeoutFrontier struct{ entered chan struct{} }

func (timeoutFrontier) AddURL(ctx context.Context, url crawler.URL) error { return nil }
func (f timeoutFrontier) Next(ctx context.Context) (crawler.URL, error) {
	if f.entered != nil {
		close(f.entered)
	}
	<-ctx.Done()
	return crawler.URL{}, fmt.Errorf("frontier: next: %w", os.ErrDeadlineExceeded)
}
func (timeoutFrontier) MarkDone(ctx context.Context, url string) error { return nil }

// realErrorFrontier models a genuine, non-cancellation crawl error surfacing
// just as the run context is cancelled: Next blocks until cancellation, then
// returns a plain error that is neither context.Canceled nor an i/o timeout.
// During a shutdown drain the cancelled run context is the dominant signal, so
// supervise parks such a run (paused, resumable) rather than failing it — even
// though the error itself carries no cancellation shape. Contrast the deferred
// case (the same error under a LIVE context), which terminalStatus keeps failed.
type realErrorFrontier struct{}

func (realErrorFrontier) AddURL(ctx context.Context, url crawler.URL) error { return nil }
func (realErrorFrontier) Next(ctx context.Context) (crawler.URL, error) {
	<-ctx.Done()
	return crawler.URL{}, errors.New("boom")
}
func (realErrorFrontier) MarkDone(ctx context.Context, url string) error { return nil }

// --- tests ---

func TestTerminalStatus(t *testing.T) {
	ioTimeout := fmt.Errorf("frontier: next: %w", os.ErrDeadlineExceeded)
	tests := []struct {
		name       string
		err        error
		ctxErr     error
		wantStatus crawler.RunStatus
		wantErrMsg bool
	}{
		{"nil completes", nil, nil, crawler.RunStatusCompleted, false},
		{"stop requested", orchestrator.ErrStopRequested, nil, crawler.RunStatusStopped, false},
		{"context canceled", context.Canceled, nil, crawler.RunStatusStopped, false},
		{"wrapped stop requested", errors.Join(errors.New("ctx"), orchestrator.ErrStopRequested), nil, crawler.RunStatusStopped, false},
		{"other error fails", errors.New("boom"), nil, crawler.RunStatusFailed, true},
		// A cancellation that surfaced as an i/o timeout is stopped only because
		// the run context was cancelled (Stop/Shutdown) — this is the #32 path.
		{"io timeout under cancelled ctx is stopped", ioTimeout, context.Canceled, crawler.RunStatusStopped, false},
		// The same error with a live context is a genuine transient failure and
		// must still fail (the deferred, unrecoverable case stays failed).
		{"io timeout under live ctx still fails", ioTimeout, nil, crawler.RunStatusFailed, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, msg := terminalStatus(tt.err, tt.ctxErr)
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

// TestReconcileSkipsPausedAdoptsPausing verifies the Option-C reconcile contract:
// a deliberately human-parked `paused` run is never auto-resumed (the factory is
// not invoked for it and its row is left untouched), while a transient `pausing`
// run IS adopted — the factory is invoked and its status is kept `pausing` (not
// flipped to running) so a later status watcher can re-derive the park.
func TestReconcileSkipsPausedAdoptsPausing(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	pausedID := uuid.New()
	pausingID := uuid.New()
	runs.Create(t.Context(), &crawler.CrawlRun{ID: pausedID, DefinitionID: defID, Status: crawler.RunStatusPaused})
	runs.Create(t.Context(), &crawler.CrawlRun{ID: pausingID, DefinitionID: defID, Status: crawler.RunStatusPausing})

	var launchMu sync.Mutex
	launched := map[uuid.UUID]bool{}
	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		launchMu.Lock()
		launched[runID] = true
		launchMu.Unlock()
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: shouldStop,
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	if err := r.Reconcile(t.Context()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// launch invokes the factory synchronously, so by the time Reconcile returns
	// the launched map reflects exactly which runs were adopted.
	launchMu.Lock()
	if launched[pausedID] {
		t.Error("paused run must not be adopted (reconcile never auto-resumes a human park)")
	}
	if !launched[pausingID] {
		t.Error("pausing run must be adopted by reconcile")
	}
	launchMu.Unlock()

	// The skipped paused run is left exactly as it was: still paused, not finished.
	paused, err := runs.Get(t.Context(), pausedID)
	if err != nil {
		t.Fatalf("Get paused: %v", err)
	}
	if paused.Status != crawler.RunStatusPaused {
		t.Errorf("paused run: got %q, want paused (untouched)", paused.Status)
	}
	if paused.FinishedAt != nil {
		t.Errorf("paused run must not be finished; got FinishedAt=%v", paused.FinishedAt)
	}

	// The adopted pausing run keeps its pausing status: reconcile does not flip it
	// to running, so a later watcher can re-derive the park. Asserted before the
	// watcher's first 1s tick (which parks the adopted run as paused) and before
	// the final Shutdown.
	pausing, err := runs.Get(t.Context(), pausingID)
	if err != nil {
		t.Fatalf("Get pausing: %v", err)
	}
	if pausing.Status != crawler.RunStatusPausing {
		t.Errorf("adopted pausing run: got %q, want pausing (not flipped to running)", pausing.Status)
	}

	// Drain the adopted run's blocking goroutine.
	r.Shutdown(t.Context())
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

// TestShutdownLeavesRunRunningForResume verifies the Option-C contract: a
// graceful Shutdown leaves an un-stopped, un-paused run resumable rather than
// terminating it — the row stays running (no FinishedAt) and its frontier is
// preserved (the cleaner is not invoked), so a later Reconcile adopts and
// auto-resumes it like a crash. A run the user explicitly Stopped before the
// drain must still terminate as stopped and have its frontier cleaned.
func TestShutdownLeavesRunRunningForResume(t *testing.T) {
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

	var cleanMu sync.Mutex
	cleaned := map[uuid.UUID]bool{}
	cleaner := func(ctx context.Context, runID uuid.UUID) error {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		cleaned[runID] = true
		return nil
	}

	r := New(runs, defs, factory, WithFrontierCleaner(cleaner))

	parked, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start parked: %v", err)
	}
	stopped, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start stopped: %v", err)
	}

	// Explicitly stop one run and let it drain terminal before the shutdown, so
	// the two fates are unambiguous.
	if err := r.Stop(t.Context(), stopped.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	s := waitForFinish(t, runs, stopped.ID)
	if s.Status != crawler.RunStatusStopped {
		t.Errorf("stopped run: got %q, want stopped", s.Status)
	}

	// Graceful shutdown parks the still-running run instead of terminating it.
	r.Shutdown(t.Context())

	got, err := runs.Get(t.Context(), parked.ID)
	if err != nil {
		t.Fatalf("Get parked: %v", err)
	}
	if got.Status != crawler.RunStatusRunning {
		t.Errorf("left-running run status: got %q, want running (resumable)", got.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("left-running run must not be finished; got FinishedAt=%v", got.FinishedAt)
	}

	cleanMu.Lock()
	if cleaned[parked.ID] {
		t.Error("left-running run's frontier must be preserved for resume, not cleaned")
	}
	if !cleaned[stopped.ID] {
		t.Error("explicitly stopped run's frontier must be cleaned")
	}
	cleanMu.Unlock()

	// A fresh process reconciles and genuinely adopts the left-running run.
	// Prove adoption observably: only an adopted (in-active-set) run can be
	// Stopped — a non-adopted run would return ErrRunNotActive — and it must
	// then drain to stopped.
	r2 := New(runs, defs, factory, WithFrontierCleaner(cleaner))
	if err := r2.Reconcile(t.Context()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := r2.Stop(t.Context(), parked.ID); err != nil {
		t.Fatalf("Stop after reconcile (run should have been adopted): %v", err)
	}
	resumed := waitForFinish(t, runs, parked.ID)
	if resumed.Status != crawler.RunStatusStopped {
		t.Errorf("adopted run after Stop: got %q, want stopped", resumed.Status)
	}
	r2.Shutdown(t.Context())
}

// TestShutdownLeavesRunRunningDespiteFrontierTimeout is the regression test for
// #32 under the Option-C contract: a graceful Shutdown must leave a busy run
// resumable even when its frontier surfaces the cancellation as a net i/o
// timeout rather than a wrapped context.Canceled. The run must stay running (no
// FinishedAt) with its frontier preserved (the cleaner is not invoked), so a
// later Reconcile can resume it — not marked failed with its resume state
// destroyed. Unlike TestShutdownLeavesRunRunningForResume (whose blockingFrontier
// returns context.Canceled on cancel, so it passes even with the bug present),
// timeoutFrontier reproduces the real failure shape, locking in that the drain
// branch keys off runCtx.Err() not the error shape.
func TestShutdownLeavesRunRunningDespiteFrontierTimeout(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   timeoutFrontier{},
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

	parked, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Shutdown never writes a stopping desired-state, so the run can only exit
	// via the frontier's i/o timeout — exactly the shape the fix must park.
	// Shutdown blocks until the run has drained, so the row is written by the
	// time it returns.
	r.Shutdown(t.Context())

	got, err := runs.Get(t.Context(), parked.ID)
	if err != nil {
		t.Fatalf("Get parked: %v", err)
	}
	if got.Status != crawler.RunStatusRunning {
		t.Errorf("left-running run status: got %q, want running (resumable despite i/o timeout)", got.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("left-running run must not be finished; got FinishedAt=%v", got.FinishedAt)
	}

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if cleaned[parked.ID] {
		t.Error("left-running run's frontier must be preserved for resume, not cleaned")
	}
}

// TestStopClassifiesFrontierTimeoutAsStopped is the second-latent-bug regression
// for #32: a user Stop that unblocks a frontier read mid-flight surfaces as a net
// i/o timeout, and the run must still terminate stopped (not failed) with its
// frontier cleaned.
func TestStopClassifiesFrontierTimeoutAsStopped(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	entered := make(chan struct{})
	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   timeoutFrontier{entered: entered},
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

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Wait until the crawl loop is blocked inside Next before stopping, so the
	// cancellation lands mid-read and Next returns the net i/o timeout — not
	// ErrStopRequested from the in-loop poll (which would be classified stopped
	// even without the fix). This forces the run through the ctxErr branch.
	<-entered

	if err := r.Stop(t.Context(), run.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	final := waitForFinish(t, runs, run.ID)
	if final.Status != crawler.RunStatusStopped {
		t.Errorf("stopped run: got %q, want stopped (i/o timeout under Stop must not be failed)", final.Status)
	}

	// A terminated run's frontier is cleaned (unlike a parked one). Cleanup fires
	// just after the terminal status write, outside the lock, so poll for it.
	waitFor(t, func() bool {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		return cleaned[run.ID]
	}, "frontier cleaner to run for the stopped run")
}

// TestShutdownLeavesRunRunningWithRealErrorDuringDrain locks in the broadened
// drain condition under Option C: during a graceful Shutdown the cancelled run
// context is the dominant signal, so even a genuine, non-cancellation error
// surfacing as the run drains leaves the run running (frontier preserved) rather
// than failing it. This favours resume over a terminal failure during shutdown;
// a genuinely broken run just re-fails after Reconcile. It is the counterpart to
// the terminalStatus "io timeout under live ctx still fails" case: the same class
// of error under a LIVE (non-cancelled) context stays failed.
func TestShutdownLeavesRunRunningWithRealErrorDuringDrain(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   realErrorFrontier{},
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

	parked, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Shutdown never writes a stopping desired-state, so the run exits only via
	// the frontier's plain "boom" error once its context is cancelled — a real
	// error, not a cancellation shape. Shutdown blocks until the run has drained,
	// so the row is written by the time it returns.
	r.Shutdown(t.Context())

	got, err := runs.Get(t.Context(), parked.ID)
	if err != nil {
		t.Fatalf("Get parked: %v", err)
	}
	if got.Status != crawler.RunStatusRunning {
		t.Errorf("left-running run status: got %q, want running (drain leaves running even on a real error)", got.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("left-running run must not be finished; got FinishedAt=%v", got.FinishedAt)
	}

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if cleaned[parked.ID] {
		t.Error("left-running run's frontier must be preserved for resume, not cleaned")
	}
}

func TestStartRejectedWhileDraining(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   doneFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: shouldStop,
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	// Shutdown with no active runs latches draining and returns immediately.
	r.Shutdown(t.Context())

	if _, err := r.Start(t.Context(), defID); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start after shutdown: got %v, want ErrShuttingDown", err)
	}
}

func TestShutdownBoundedByContext(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	// The run's Next ignores cancellation, so its WaitGroup entry never
	// completes; keep it released only when the test ends so the goroutine can
	// unwind without leaking past the process.
	release := make(chan struct{})
	defer close(release)

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   hangFrontier{release: release},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)
	if _, err := r.Start(t.Context(), defID); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		r.Shutdown(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown did not honor its context deadline; drain hung")
	}
}

func TestDesiredStateStopViaWatcher(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	// blockingFrontier parks inside Next (honoring ctx.Done), and the in-loop
	// ShouldStop is disabled, so the only thing that can stop this run is the
	// per-run desired-state watcher polling GetStatus (see #16).
	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)
	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Flip the desired state directly (not via Stop, which also cancels): only
	// the watcher's status poll can drive this run to stopped.
	runs.setStatus(run.ID, crawler.RunStatusStopping)

	final := waitForFinish(t, runs, run.ID)
	if final.Status != crawler.RunStatusStopped {
		t.Errorf("run status: got %q, want stopped", final.Status)
	}
}

func TestFactoryErrorCleansFrontier(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		return nil, errors.New("boom")
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

	if _, err := r.Start(t.Context(), defID); err == nil {
		t.Fatal("Start: expected factory error, got nil")
	}

	// The just-created run is marked failed and its frontier keys are cleaned
	// even though it never reached supervise (see #24).
	failed, err := runs.ListByStatus(t.Context(), crawler.RunStatusFailed)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(failed) != 1 {
		t.Fatalf("failed runs: got %d, want 1", len(failed))
	}

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if !cleaned[failed[0].ID] {
		t.Errorf("frontier cleaner was not invoked for the failed run %s", failed[0].ID)
	}
}

// TestPauseParksRunAsPaused verifies acceptance criterion 1: pausing a running
// run drives it running → pausing → paused with FinishedAt unset and its
// Frontier preserved (the cleaner is not invoked). blockingFrontier's perpetual
// Next models both a Discovery (perpetual) and a mid-flight Keyword run, so this
// also covers criterion 4 (pause works for both crawl kinds).
func TestPauseParksRunAsPaused(t *testing.T) {
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

	var cleanMu sync.Mutex
	cleaned := map[uuid.UUID]bool{}
	cleaner := func(ctx context.Context, runID uuid.UUID) error {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		cleaned[runID] = true
		return nil
	}

	r := New(runs, defs, factory, WithFrontierCleaner(cleaner))

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := r.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	// The run parks as paused; FinishedAt stays nil (paused is non-terminal), so
	// poll on the status rather than waitForFinish.
	final := waitForStatus(t, runs, run.ID, crawler.RunStatusPaused)
	if final.FinishedAt != nil {
		t.Errorf("paused run must not be finished; got FinishedAt=%v", final.FinishedAt)
	}

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if cleaned[run.ID] {
		t.Error("paused run's frontier must be preserved for resume, not cleaned")
	}
}

// TestPauseUpdateStatusFailureDoesNotLatchPauseRequested verifies that when the
// status write inside Pause fails, the in-memory pauseRequested flag is NOT
// latched — so a later Pause (with a working status write) still parks the run.
// Regression: latching the flag before the write turned any transient write
// failure into a permanent 409 lockout via Pause's re-entry guard.
func TestPauseUpdateStatusFailureDoesNotLatchPauseRequested(t *testing.T) {
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

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// First Pause: the status write fails, so Pause returns the error and must
	// leave pauseRequested unset and the run still running.
	wantErr := errors.New("transient status write failure")
	runs.failNextUpdateStatus(wantErr)
	if err := r.Pause(t.Context(), run.ID); !errors.Is(err, wantErr) {
		t.Fatalf("Pause with failing UpdateStatus: got err %v, want %v", err, wantErr)
	}

	r.mu.Lock()
	active, ok := r.active[run.ID]
	latched := ok && active.pauseRequested
	r.mu.Unlock()
	if !ok {
		t.Fatal("run must remain active after a failed pause write")
	}
	if latched {
		t.Fatal("pauseRequested must not be latched after a failed status write")
	}

	// Second Pause: UpdateStatus now succeeds, so the run must actually park as
	// paused (status pausing→paused, context cancelled, FinishedAt unset).
	if err := r.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("second Pause: %v", err)
	}
	final := waitForStatus(t, runs, run.ID, crawler.RunStatusPaused)
	if final.FinishedAt != nil {
		t.Errorf("paused run must not be finished; got FinishedAt=%v", final.FinishedAt)
	}
}

// TestPausePrecedenceOverShutdown verifies carry-forward requirement 1: when a
// pause races a graceful shutdown, the pause wins. Even though Shutdown latches
// draining and cancels the run, the run must park as paused (not left running by
// the Option-C shutdown branch), with FinishedAt unset and its Frontier
// preserved — because pauseRequested is set before Shutdown's cancel and the
// paused-park branch is evaluated before the shutdown-drain branch.
func TestPausePrecedenceOverShutdown(t *testing.T) {
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

	var cleanMu sync.Mutex
	cleaned := map[uuid.UUID]bool{}
	cleaner := func(ctx context.Context, runID uuid.UUID) error {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		cleaned[runID] = true
		return nil
	}

	r := New(runs, defs, factory, WithFrontierCleaner(cleaner))

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := r.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	// Shutdown latches draining and cancels; the pause must still win.
	r.Shutdown(t.Context())

	got, err := runs.Get(t.Context(), run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != crawler.RunStatusPaused {
		t.Errorf("run status: got %q, want paused (pause wins over shutdown drain)", got.Status)
	}
	if got.FinishedAt != nil {
		t.Errorf("paused run must not be finished; got FinishedAt=%v", got.FinishedAt)
	}

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if cleaned[run.ID] {
		t.Error("paused run's frontier must be preserved, not cleaned")
	}
}

// TestStopPrecedenceOverPause verifies carry-forward requirement 1's converse:
// an explicit Stop issued on a pausing run still terminates it as stopped with
// its Frontier cleaned. The paused-park branch's `&& !ar.stopRequested` guard
// lets the Stop win over a concurrent pause. hangFrontier ignores context
// cancellation until released, so the run is still in flight when both Pause and
// Stop have been issued.
func TestStopPrecedenceOverPause(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	release := make(chan struct{})
	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   hangFrontier{release: release},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
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

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Pause first (sets pauseRequested, cancels — the run is still blocked in the
	// hang frontier), then Stop (sets stopRequested). Only now release Next so the
	// loop unwinds and supervise makes its fate decision.
	if err := r.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := r.Stop(t.Context(), run.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	close(release)

	final := waitForFinish(t, runs, run.ID)
	if final.Status != crawler.RunStatusStopped {
		t.Errorf("run status: got %q, want stopped (explicit Stop wins over pause)", final.Status)
	}

	waitFor(t, func() bool {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		return cleaned[run.ID]
	}, "frontier cleaner to run for the stopped run")
}

// TestWatcherParksPausingRun verifies acceptance criterion 2 / carry-forward
// requirement 2 (the crash-mid-pause recovery path): a run whose durable status
// is flipped directly to pausing — with no in-memory pause flag, as after a
// crash-and-relaunch — is parked as paused by the desired-state watcher, which
// re-derives the intent (sets the flag + cancels). This is the analog of
// TestDesiredStateStopViaWatcher.
func TestWatcherParksPausingRun(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
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

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Flip the desired state directly (not via Pause, which also sets the flag and
	// cancels): only the watcher's status poll can re-derive the park.
	runs.setStatus(run.ID, crawler.RunStatusPausing)

	final := waitForStatus(t, runs, run.ID, crawler.RunStatusPaused)
	if final.FinishedAt != nil {
		t.Errorf("paused run must not be finished; got FinishedAt=%v", final.FinishedAt)
	}

	cleanMu.Lock()
	defer cleanMu.Unlock()
	if cleaned[run.ID] {
		t.Error("paused run's frontier must be preserved, not cleaned")
	}
}

// TestReconcileRelaunchesPausingReDrivesToPaused verifies acceptance criterion 3
// / carry-forward requirement 3: boot-time reconcile adopts a durable pausing
// run without flipping it to running (#57's behavior), and the watcher then
// re-drives it to paused — never running, never terminal.
func TestReconcileRelaunchesPausingReDrivesToPaused(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	pausingID := uuid.New()
	runs.Create(t.Context(), &crawler.CrawlRun{ID: pausingID, DefinitionID: defID, Status: crawler.RunStatusPausing})

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	if err := r.Reconcile(t.Context()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	final := waitForStatus(t, runs, pausingID, crawler.RunStatusPaused)
	if final.FinishedAt != nil {
		t.Errorf("re-driven paused run must not be finished; got FinishedAt=%v", final.FinishedAt)
	}
}

// TestPauseRejectsNonRunning verifies carry-forward requirement 4 at the runner
// seam: Pause on an unknown (non-active) run returns ErrRunNotActive, as does a
// redundant Pause on a run already pausing or a run already stopping. The API
// maps this sentinel to 409.
func TestPauseRejectsNonRunning(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	if err := r.Pause(t.Context(), uuid.New()); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("Pause on unknown id: got %v, want ErrRunNotActive", err)
	}

	run, err := r.Start(t.Context(), defID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := r.Pause(t.Context(), run.ID); err != nil {
		t.Fatalf("first Pause: %v", err)
	}
	// A second Pause on an already-pausing run is redundant and rejected.
	if err := r.Pause(t.Context(), run.ID); !errors.Is(err, ErrRunNotActive) {
		t.Errorf("redundant Pause: got %v, want ErrRunNotActive", err)
	}

	r.Shutdown(t.Context())
}

// TestResumeContinuesFromCounters verifies acceptance criterion 1 / carry-forward
// requirement 3: resuming a paused run relaunches it seeded from its persisted
// counters (not reset to zero) through the same engine factory, and it proceeds
// to completion. doneFrontier drains immediately, so the resumed run runs to
// completed with FinishedAt set and its frontier cleaned on terminal completion.
func TestResumeContinuesFromCounters(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	pausedID := uuid.New()
	runs.Create(t.Context(), &crawler.CrawlRun{ID: pausedID, DefinitionID: defID, Status: crawler.RunStatusPaused, Counters: crawler.RunCounters{PagesCrawled: 7}})

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

	if err := r.Resume(t.Context(), pausedID); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	final := waitForFinish(t, runs, pausedID)
	if final.Status != crawler.RunStatusCompleted {
		t.Errorf("resumed run: got %q, want completed", final.Status)
	}
	if final.FinishedAt == nil {
		t.Error("resumed run should have FinishedAt set once completed")
	}
	if final.Counters.PagesCrawled != 7 {
		t.Errorf("resumed counters: got %d pages, want 7 (seeded from persisted, not reset)", final.Counters.PagesCrawled)
	}

	// A terminated run's frontier is cleaned. Cleanup fires just after the
	// terminal status write, outside the lock, so poll for it.
	waitFor(t, func() bool {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		return cleaned[pausedID]
	}, "frontier cleaner to run for the completed run")
}

// TestConcurrentResumeLaunchesOneEngine verifies the hard race-safety requirement
// (carry-forward 1): two concurrent Resume calls on the same paused run reserve
// the run's active-set slot inside one critical section, so the factory is invoked
// exactly once — one call wins (nil), the other loses (ErrRunNotPaused) — and the
// run ends up running against a single engine.
func TestConcurrentResumeLaunchesOneEngine(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	pausedID := uuid.New()
	runs.Create(t.Context(), &crawler.CrawlRun{ID: pausedID, DefinitionID: defID, Status: crawler.RunStatusPaused})

	var factoryCount atomic.Int64
	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		factoryCount.Add(1)
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
			OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
			ShouldStop: func(context.Context) bool { return false },
		})
		return &Engine{Orchestrator: o, Close: func() {}}, nil
	}

	r := New(runs, defs, factory)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = r.Resume(t.Context(), pausedID)
		}(i)
	}
	wg.Wait()

	if got := factoryCount.Load(); got != 1 {
		t.Errorf("factory invocations: got %d, want exactly 1 (one engine per frontier)", got)
	}

	nils, notPaused := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			nils++
		case errors.Is(err, ErrRunNotPaused):
			notPaused++
		default:
			t.Errorf("unexpected Resume error: %v", err)
		}
	}
	if nils != 1 || notPaused != 1 {
		t.Errorf("Resume outcomes: got %d nil / %d ErrRunNotPaused, want 1 / 1", nils, notPaused)
	}

	got, err := runs.Get(t.Context(), pausedID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != crawler.RunStatusRunning {
		t.Errorf("resumed run status: got %q, want running", got.Status)
	}

	// Drain the surviving engine's blocking goroutine.
	r.Shutdown(t.Context())
}

// TestResumeRejectsNonPaused verifies acceptance criterion 3 / carry-forward
// requirement 2 at the runner seam: Resume on a run that is not paused returns
// ErrRunNotPaused and never invokes the factory. It covers a durable non-paused
// run absent from the active set (running, completed) and a live running run
// present in the active set (rejected via the in-active-set guard).
func TestResumeRejectsNonPaused(t *testing.T) {
	t.Run("durable running run is rejected without relaunch", func(t *testing.T) {
		defID := uuid.New()
		defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
		runs := newFakeRunRepo()
		id := uuid.New()
		runs.Create(t.Context(), &crawler.CrawlRun{ID: id, DefinitionID: defID, Status: crawler.RunStatusRunning})

		var factoryCount atomic.Int64
		factory := func(context.Context, uuid.UUID, crawler.CrawlDefinition, *Counters, func(context.Context) bool) (*Engine, error) {
			factoryCount.Add(1)
			return nil, errors.New("factory must not be called")
		}
		r := New(runs, defs, factory)

		if err := r.Resume(t.Context(), id); !errors.Is(err, ErrRunNotPaused) {
			t.Errorf("Resume on running run: got %v, want ErrRunNotPaused", err)
		}
		if factoryCount.Load() != 0 {
			t.Error("factory must not be invoked for a non-paused run")
		}
		got, _ := runs.Get(t.Context(), id)
		if got.Status != crawler.RunStatusRunning {
			t.Errorf("status must be unchanged: got %q, want running", got.Status)
		}
	})

	t.Run("terminal completed run is rejected", func(t *testing.T) {
		defID := uuid.New()
		defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
		runs := newFakeRunRepo()
		id := uuid.New()
		finished := time.Now()
		runs.Create(t.Context(), &crawler.CrawlRun{ID: id, DefinitionID: defID, Status: crawler.RunStatusCompleted, FinishedAt: &finished})

		var factoryCount atomic.Int64
		factory := func(context.Context, uuid.UUID, crawler.CrawlDefinition, *Counters, func(context.Context) bool) (*Engine, error) {
			factoryCount.Add(1)
			return nil, errors.New("factory must not be called")
		}
		r := New(runs, defs, factory)

		if err := r.Resume(t.Context(), id); !errors.Is(err, ErrRunNotPaused) {
			t.Errorf("Resume on completed run: got %v, want ErrRunNotPaused", err)
		}
		if factoryCount.Load() != 0 {
			t.Error("factory must not be invoked for a terminal run")
		}
	})

	t.Run("live running run in the active set is rejected", func(t *testing.T) {
		defID := uuid.New()
		defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
		runs := newFakeRunRepo()

		var factoryCount atomic.Int64
		factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
			factoryCount.Add(1)
			o := orchestrator.NewOrchestrator(orchestrator.Config{
				Frontier:   blockingFrontier{},
				OnNextURL:  func(context.Context, *crawler.URL) error { return nil },
				ShouldStop: func(context.Context) bool { return false },
			})
			return &Engine{Orchestrator: o, Close: func() {}}, nil
		}
		r := New(runs, defs, factory)

		run, err := r.Start(t.Context(), defID)
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		startCount := factoryCount.Load()

		if err := r.Resume(t.Context(), run.ID); !errors.Is(err, ErrRunNotPaused) {
			t.Errorf("Resume on live running run: got %v, want ErrRunNotPaused", err)
		}
		if factoryCount.Load() != startCount {
			t.Error("Resume must not invoke the factory for a run already in the active set")
		}

		r.Shutdown(t.Context())
	})
}

// TestStopOnNotActiveRun verifies acceptance criterion 1 / carry-forward
// requirement 1+5: Stop on a run absent from the active set consults its durable
// status. A human-parked paused run is driven straight to terminal stopped
// (FinishedAt set) with its Frontier cleaned (the cleaner IS invoked) — the exact
// contrast to TestPauseParksRunAsPaused, which parks paused without invoking the
// cleaner. Any other not-active state (terminal, unknown id) is unchanged: Stop
// returns ErrRunNotActive and touches nothing.
func TestStopOnNotActiveRun(t *testing.T) {
	newRunner := func(runs *fakeRunRepo, cleaned *map[uuid.UUID]bool, cleanMu *sync.Mutex) *Runner {
		defID := uuid.New()
		defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
		// A paused run holds no engine, so Stop must never invoke the factory.
		factory := func(context.Context, uuid.UUID, crawler.CrawlDefinition, *Counters, func(context.Context) bool) (*Engine, error) {
			return nil, errors.New("factory must not be called")
		}
		cleaner := func(ctx context.Context, runID uuid.UUID) error {
			cleanMu.Lock()
			defer cleanMu.Unlock()
			(*cleaned)[runID] = true
			return nil
		}
		return New(runs, defs, factory, WithFrontierCleaner(cleaner))
	}

	t.Run("paused run terminates as stopped and cleans its frontier", func(t *testing.T) {
		runs := newFakeRunRepo()
		id := uuid.New()
		runs.Create(t.Context(), &crawler.CrawlRun{ID: id, Status: crawler.RunStatusPaused})

		var cleanMu sync.Mutex
		cleaned := map[uuid.UUID]bool{}
		r := newRunner(runs, &cleaned, &cleanMu)

		if err := r.Stop(t.Context(), id); err != nil {
			t.Fatalf("Stop on paused run: %v", err)
		}

		got, err := runs.Get(t.Context(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status != crawler.RunStatusStopped {
			t.Errorf("status: got %q, want stopped", got.Status)
		}
		if got.FinishedAt == nil {
			t.Error("stopped paused run should have FinishedAt set")
		}

		// The preserved frontier is dropped now the run is terminal; cleanup fires
		// just after the terminal write, outside the lock, so poll for it.
		waitFor(t, func() bool {
			cleanMu.Lock()
			defer cleanMu.Unlock()
			return cleaned[id]
		}, "frontier cleaner to run for the stopped paused run")
	})

	t.Run("completed run returns ErrRunNotActive and is untouched", func(t *testing.T) {
		runs := newFakeRunRepo()
		id := uuid.New()
		finished := time.Now()
		runs.Create(t.Context(), &crawler.CrawlRun{ID: id, Status: crawler.RunStatusCompleted, FinishedAt: &finished})

		var cleanMu sync.Mutex
		cleaned := map[uuid.UUID]bool{}
		r := newRunner(runs, &cleaned, &cleanMu)

		if err := r.Stop(t.Context(), id); !errors.Is(err, ErrRunNotActive) {
			t.Errorf("Stop on completed run: got %v, want ErrRunNotActive", err)
		}

		got, _ := runs.Get(t.Context(), id)
		if got.Status != crawler.RunStatusCompleted {
			t.Errorf("status must be unchanged: got %q, want completed", got.Status)
		}
		cleanMu.Lock()
		defer cleanMu.Unlock()
		if cleaned[id] {
			t.Error("a terminal run's frontier must not be cleaned by a rejected Stop")
		}
	})

	t.Run("unknown id returns ErrRunNotActive", func(t *testing.T) {
		runs := newFakeRunRepo()
		var cleanMu sync.Mutex
		cleaned := map[uuid.UUID]bool{}
		r := newRunner(runs, &cleaned, &cleanMu)

		if err := r.Stop(t.Context(), uuid.New()); !errors.Is(err, ErrRunNotActive) {
			t.Errorf("Stop on unknown id: got %v, want ErrRunNotActive", err)
		}
	})
}

// TestStopAndResumeRacePausedRun verifies carry-forward requirement 2: a Stop and
// a Resume racing the same paused run leave it in exactly one coherent state, with
// the factory invoked at most once and no leaked frontier. Because every decision
// (active-set check, status read, terminal/reserve write) sits under r.mu, the two
// critical sections cannot interleave:
//   - Stop wins first  → factory never runs (0 invocations); Stop returns nil and
//     Resume then reads a non-paused status → ErrRunNotPaused.
//   - Resume wins first → factory runs once; Resume returns nil and Stop finds the
//     run in the active set, taking the normal active-run stopping path (nil).
//
// In BOTH orderings the run converges to terminal stopped with FinishedAt set and
// its frontier cleaned — never two engines, never a leaked frontier.
func TestStopAndResumeRacePausedRun(t *testing.T) {
	defID := uuid.New()
	defs := &fakeDefRepo{def: &crawler.CrawlDefinition{ID: defID, Name: "test", Kind: crawler.CrawlKindDiscovery}}
	runs := newFakeRunRepo()

	pausedID := uuid.New()
	runs.Create(t.Context(), &crawler.CrawlRun{ID: pausedID, DefinitionID: defID, Status: crawler.RunStatusPaused})

	var factoryCount atomic.Int64
	factory := func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *Counters, shouldStop func(context.Context) bool) (*Engine, error) {
		factoryCount.Add(1)
		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   blockingFrontier{},
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

	var wg sync.WaitGroup
	var stopErr, resumeErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		stopErr = r.Stop(t.Context(), pausedID)
	}()
	go func() {
		defer wg.Done()
		resumeErr = r.Resume(t.Context(), pausedID)
	}()
	wg.Wait()

	switch got := factoryCount.Load(); got {
	case 0:
		// Stop took the paused-terminal path; Resume then saw a non-paused status.
		if stopErr != nil {
			t.Errorf("Stop-wins: stopErr = %v, want nil", stopErr)
		}
		if !errors.Is(resumeErr, ErrRunNotPaused) {
			t.Errorf("Stop-wins: resumeErr = %v, want ErrRunNotPaused", resumeErr)
		}
	case 1:
		// Resume reserved the slot; Stop found the run active and stopped it.
		if resumeErr != nil {
			t.Errorf("Resume-wins: resumeErr = %v, want nil", resumeErr)
		}
		if stopErr != nil {
			t.Errorf("Resume-wins: stopErr = %v, want nil", stopErr)
		}
	default:
		t.Fatalf("factory invocations: got %d, want 0 or 1 (never two engines on one frontier)", got)
	}

	// Either ordering must converge to terminal stopped with a cleaned frontier.
	final := waitForFinish(t, runs, pausedID)
	if final.Status != crawler.RunStatusStopped {
		t.Errorf("final status: got %q, want stopped", final.Status)
	}
	if final.FinishedAt == nil {
		t.Error("stopped run should have FinishedAt set")
	}
	waitFor(t, func() bool {
		cleanMu.Lock()
		defer cleanMu.Unlock()
		return cleaned[pausedID]
	}, "frontier cleaner to run (no leaked frontier in either race outcome)")

	// Join any supervise goroutine the resume-wins path started.
	r.Shutdown(t.Context())
}

// waitForStatus polls until the run reaches want (regardless of FinishedAt) or
// the test times out. Used for non-terminal parks like paused, where
// waitForFinish (which keys off FinishedAt) never fires.
func waitForStatus(t *testing.T, runs *fakeRunRepo, id uuid.UUID, want crawler.RunStatus) *crawler.CrawlRun {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		run, err := runs.Get(t.Context(), id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if run.Status == want {
			return run
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run did not reach status %q before deadline", want)
	return nil
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
