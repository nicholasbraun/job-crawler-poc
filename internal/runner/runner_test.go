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

func (f *fakeRunRepo) FailInterrupted(ctx context.Context, errMsg string) ([]uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []uuid.UUID{}
	for id, run := range f.runs {
		if run.Status == crawler.RunStatusRunning || run.Status == crawler.RunStatusStopping {
			run.Status = crawler.RunStatusFailed
			run.Error = errMsg
			ids = append(ids, id)
		}
	}
	return ids, nil
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
