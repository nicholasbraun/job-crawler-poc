package atsingest_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// errSaveFailed is the canned error a spyRepo returns for a failOn-matched save.
var errSaveFailed = errors.New("spy: save failed")

// stubFetcher is an inline, concurrency-safe ats.BoardFetcher. It records the
// tenants it was asked for and returns a canned result; started (optional,
// unbuffered) signals when a Fetch reaches its blocking point, and release
// (optional) makes Fetch block until the channel is closed — used to prove Close
// waits for an in-flight fetch.
type stubFetcher struct {
	mu       sync.Mutex
	listings []*crawler.JobListing
	err      error
	calls    int
	tenants  []string
	started  chan string
	release  chan struct{}
}

var _ ats.BoardFetcher = (*stubFetcher)(nil)

func (f *stubFetcher) Fetch(ctx context.Context, tenant string) ([]*crawler.JobListing, error) {
	f.mu.Lock()
	f.calls++
	f.tenants = append(f.tenants, tenant)
	started, release, listings, err := f.started, f.release, f.listings, f.err
	f.mu.Unlock()

	if started != nil {
		started <- tenant
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return listings, err
}

func (f *stubFetcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *stubFetcher) lastTenant() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.tenants) == 0 {
		return ""
	}
	return f.tenants[len(f.tenants)-1]
}

// spyRepo records every saved listing (a copy, so later mutation of the source
// can't rewrite history). failOn, when set, forces a Save error for a matching
// listing so save-error handling can be exercised.
type spyRepo struct {
	mu     sync.Mutex
	saved  []*crawler.JobListing
	failOn func(*crawler.JobListing) bool
}

func (r *spyRepo) Save(ctx context.Context, jl *crawler.JobListing) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failOn != nil && r.failOn(jl) {
		return errSaveFailed
	}
	saved := *jl
	r.saved = append(r.saved, &saved)
	return nil
}

// resolveTo builds a ResolveFetcher that always returns f, ok=true.
func resolveTo(f ats.BoardFetcher) func(string) (ats.BoardFetcher, bool) {
	return func(string) (ats.BoardFetcher, bool) { return f, true }
}

// resolveNone is a ResolveFetcher that reports every provider as clientless.
func resolveNone(string) (ats.BoardFetcher, bool) { return nil, false }

// newTestLane builds a Lane whose workers all share fetcher and repo, so tests
// can count Fetch/Save calls across the pool.
func newTestLane(t *testing.T, fetcher ats.BoardFetcher, repo crawler.CorpusRepository, workers int) *atsingest.Lane {
	t.Helper()
	return atsingest.NewLane(t.Context(), atsingest.Config{
		MaxWorkers: workers,
		NewWorker: func() processor.Processor[atsingest.FetchTask] {
			return atsingest.NewProcessor(&atsingest.ProcessorConfig{
				ResolveFetcher: resolveTo(fetcher),
				Repository:     repo,
			})
		},
	})
}

// closeAbsentCall records one CloseAbsent invocation so a test can assert the
// absence-sweep interlock (scope, watermark, and the complete flag).
type closeAbsentCall struct {
	careerPageID uuid.UUID
	notSeenSince time.Time
	complete     bool
}

// spyLiveness is an inline crawler.CorpusLivenessRepository recording the
// absence-sweep calls the ATS processor makes; ListOpen/ApplyCrawlProbe are unused
// on the ATS lane and return zero values.
type spyLiveness struct {
	mu               sync.Mutex
	closeAbsentCalls []closeAbsentCall
	closeReturn      int
}

func (s *spyLiveness) ListOpen(context.Context, uuid.UUID) ([]*crawler.JobListing, error) {
	return []*crawler.JobListing{}, nil
}

func (s *spyLiveness) CloseAbsent(_ context.Context, careerPageID uuid.UUID, notSeenSince time.Time, complete bool) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeAbsentCalls = append(s.closeAbsentCalls, closeAbsentCall{careerPageID, notSeenSince, complete})
	return s.closeReturn, nil
}

func (s *spyLiveness) ApplyCrawlProbe(context.Context, string, crawler.ProbeOutcome, int) (crawler.LifecycleState, error) {
	return crawler.LifecycleState{}, nil
}

func (s *spyLiveness) calls() []closeAbsentCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]closeAbsentCall{}, s.closeAbsentCalls...)
}

// dormancyProbe records one RecordProbe invocation.
type dormancyProbe struct {
	careerPageID uuid.UUID
	outcome      crawler.ProbeOutcome
	threshold    int
}

// spyDormancy is an inline atsingest.DormancyRecorder recording each probe and
// returning a canned DormancyResult.
type spyDormancy struct {
	mu     sync.Mutex
	probes []dormancyProbe
	result crawler.DormancyResult
}

func (s *spyDormancy) RecordProbe(_ context.Context, careerPageID uuid.UUID, outcome crawler.ProbeOutcome, threshold int) (crawler.DormancyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.probes = append(s.probes, dormancyProbe{careerPageID, outcome, threshold})
	return s.result, nil
}

func (s *spyDormancy) recorded() []dormancyProbe {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dormancyProbe{}, s.probes...)
}
