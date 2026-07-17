package atsingest_test

import (
	"sync"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
)

// TestLaneDedupsTenant asserts a tenant submitted twice is fetched once — the
// per-run once-per-tenant guarantee (ADR-0022).
func TestLaneDedupsTenant(t *testing.T) {
	fetcher := &stubFetcher{}
	lane := newTestLane(t, fetcher, &spyRepo{}, 2)

	task := atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}
	if err := lane.Submit(t.Context(), task); err != nil {
		t.Fatalf("Submit 1: %v", err)
	}
	if err := lane.Submit(t.Context(), task); err != nil {
		t.Fatalf("Submit 2: %v", err)
	}
	lane.Close()

	if got := fetcher.callCount(); got != 1 {
		t.Errorf("Fetch called %d times, want 1 (the same tenant is deduped)", got)
	}
}

// TestLaneSubmitDedupsUnderConcurrency drives many concurrent submissions of the
// same tenant through the shared dedup set; exactly one must reach a fetch. Run
// under -race, it exercises the compare-and-set in tenantSet.
func TestLaneSubmitDedupsUnderConcurrency(t *testing.T) {
	fetcher := &stubFetcher{}
	lane := newTestLane(t, fetcher, &spyRepo{}, 4)

	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = lane.Submit(t.Context(), atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"})
		}()
	}
	wg.Wait()
	lane.Close()

	if got := fetcher.callCount(); got != 1 {
		t.Errorf("Fetch called %d times under concurrent submit, want 1", got)
	}
}

// TestLaneCloseWaitsForInFlightFetch is the run-completion guarantee: Close (and
// therefore the run) must not return while a fetch is still in flight.
func TestLaneCloseWaitsForInFlightFetch(t *testing.T) {
	fetcher := &stubFetcher{
		started: make(chan string),
		release: make(chan struct{}),
	}
	lane := newTestLane(t, fetcher, &spyRepo{}, 1)

	if err := lane.Submit(t.Context(), atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	<-fetcher.started // a worker is now inside Fetch, blocked on release

	done := make(chan struct{})
	go func() {
		lane.Close()
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Close returned while a fetch was still in flight")
	case <-time.After(50 * time.Millisecond):
		// Still blocked, as required.
	}

	close(fetcher.release) // let the fetch complete

	select {
	case <-done:
		// Close returned only after the fetch finished.
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return after the in-flight fetch was released")
	}

	if got := fetcher.callCount(); got != 1 {
		t.Errorf("Fetch called %d times, want 1", got)
	}
}

// TestLanePrimeAsyncDrainsAllTasks covers the all-ATS-seeds / empty-Frontier case:
// PrimeAsync submits every routed task and Close drains them all before returning,
// even when Close is called immediately after priming.
func TestLanePrimeAsyncDrainsAllTasks(t *testing.T) {
	fetcher := &stubFetcher{}
	lane := newTestLane(t, fetcher, &spyRepo{}, 3)

	tasks := []atsingest.FetchTask{
		{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"},
		{Provider: "greenhouse", TenantSlug: "globex", Owner: "globex.com"},
		{Provider: "greenhouse", TenantSlug: "initech", Owner: "initech.com"},
	}
	lane.PrimeAsync(t.Context(), tasks)
	lane.Close()

	if got := fetcher.callCount(); got != 3 {
		t.Errorf("Fetch called %d times, want 3 (all primed tenants fetched before Close returns)", got)
	}
}
