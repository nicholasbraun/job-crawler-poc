package atsingest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

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
				Keywords:       []string{"go"},
			})
		},
	})
}
