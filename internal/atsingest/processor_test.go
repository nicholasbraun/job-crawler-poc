package atsingest_test

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
	"github.com/nicholasbraun/job-crawler-poc/internal/listingid"
)

// TestProcessorSavesEveryPosting asserts a Collection Cycle has NO keyword pruning
// (ADR-0038): every fetched posting is saved, each carrying the fetcher's canonical
// posting URL and the Corpus identity stamped from the provider posting id (ADR-0034).
func TestProcessorSavesEveryPosting(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Go Engineer", URL: "https://boards.greenhouse.io/acme/jobs/1", SourceID: "1", Description: "build services"},
		{Title: "Sales Rep", URL: "https://boards.greenhouse.io/acme/jobs/2", SourceID: "2", Description: "close deals"},
	}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
		CompanyNames:   map[string]string{"acme.com": "Acme Inc"},
	})

	task := &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}
	if err := proc.Process(t.Context(), task); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if got := fetcher.lastTenant(); got != "acme" {
		t.Errorf("fetched tenant = %q, want acme", got)
	}
	if len(repo.saved) != 2 {
		t.Fatalf("saved %d listings, want 2 (no keyword pruning)", len(repo.saved))
	}
	got := repo.saved[0]
	if got.Source != crawler.SourceLaneATS {
		t.Errorf("Source = %q, want %q", got.Source, crawler.SourceLaneATS)
	}
	if want := listingid.FromATS("greenhouse", "acme", got.SourceID); got.CanonicalURL != want {
		t.Errorf("CanonicalURL = %q, want %q (identity from provider posting id)", got.CanonicalURL, want)
	}
}

// TestProcessorStampsCareerPageID asserts each saved posting carries the task's
// CareerPageID (ADR-0035) so the crawl-lane refetch and dormancy scope resolve.
func TestProcessorStampsCareerPageID(t *testing.T) {
	page := uuid.New()
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Go Engineer", URL: "https://boards.greenhouse.io/acme/jobs/1", SourceID: "1"},
	}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
		Liveness:       &spyLiveness{},
		Dormancy:       &spyDormancy{},
	})

	task := &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com", CareerPageID: page}
	if err := proc.Process(t.Context(), task); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d, want 1", len(repo.saved))
	}
	if repo.saved[0].CareerPageID != page {
		t.Errorf("CareerPageID = %v, want %v (stamped from the task)", repo.saved[0].CareerPageID, page)
	}
}

// TestProcessorFallsBackToURLIdentityWithoutSourceID asserts the keep-distinct
// fallback (ADR-0034): a posting the fetcher could not id keys on its
// canonicalized URL rather than collapsing the whole tenant to one key.
func TestProcessorFallsBackToURLIdentityWithoutSourceID(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Go Engineer", URL: "https://boards.greenhouse.io/acme/jobs/1", Description: "build services"},
	}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
	})

	if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d listings, want 1", len(repo.saved))
	}
	got := repo.saved[0]
	if want := listingid.FromURL(got.URL); got.CanonicalURL != want {
		t.Errorf("CanonicalURL = %q, want the URL-derived identity %q", got.CanonicalURL, want)
	}
}

// TestProcessorAttributesOwner asserts the ADR-0021 override: the Owner's Catalog
// name wins over the provider board's own company field, and the Owner is the
// durable CompanyKey.
func TestProcessorAttributesOwner(t *testing.T) {
	t.Run("snapshot hit: catalog name overwrites the provider-supplied company", func(t *testing.T) {
		fetcher := &stubFetcher{listings: []*crawler.JobListing{
			{Title: "Go Engineer", URL: "u", Company: "provider-supplied wrong co"},
		}}
		repo := &spyRepo{}
		proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
			ResolveFetcher: resolveTo(fetcher),
			Repository:     repo,
			CompanyNames:   map[string]string{"acme.com": "Acme Inc"},
		})

		if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}); err != nil {
			t.Fatalf("Process: %v", err)
		}
		got := repo.saved[0]
		if got.Company != "Acme Inc" {
			t.Errorf("Company = %q, want %q (Owner snapshot wins over provider field)", got.Company, "Acme Inc")
		}
		if got.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey = %q, want %q (the Owner)", got.CompanyKey, "acme.com")
		}
	})

	t.Run("snapshot miss: provider company still discarded, Owner persisted", func(t *testing.T) {
		fetcher := &stubFetcher{listings: []*crawler.JobListing{
			{Title: "Go Engineer", URL: "u", Company: "provider-supplied wrong co"},
		}}
		repo := &spyRepo{}
		proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
			ResolveFetcher: resolveTo(fetcher),
			Repository:     repo,
			CompanyNames:   map[string]string{"other.com": "Other Inc"},
		})

		if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}); err != nil {
			t.Fatalf("Process: %v", err)
		}
		got := repo.saved[0]
		if got.Company != "" {
			t.Errorf("Company = %q, want empty on snapshot miss (provider field never used)", got.Company)
		}
		if got.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey = %q, want %q (the Owner)", got.CompanyKey, "acme.com")
		}
	})
}

// TestProcessorResolvesCountryAtSave asserts the ATS-lane country resolution
// (ADR-0029): at save the processor resolves each listing's Country via the real
// Country Resolver, preferring the provider's structured CountryHint (a valid ISO
// code used directly, else a country name resolved) and falling back to the
// composed Location. An unresolvable hint and location yield the empty Country,
// and the listing is still saved (kept; ADR-0028).
func TestProcessorResolvesCountryAtSave(t *testing.T) {
	tests := []struct {
		name        string
		hint        string
		location    string
		wantCountry string
	}{
		{"no hint falls back to composed location", "", "Berlin, Germany", "DE"},
		{"valid iso code hint wins", "PT", "Remote job", "PT"},
		{"country-name hint resolves", "United States", "", "US"},
		{"unresolvable region hint kept as empty", "European Union", "", ""},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := &stubFetcher{listings: []*crawler.JobListing{
				{
					Title:       "Go Engineer",
					URL:         "https://board/" + strconv.Itoa(i),
					Description: "build services",
					CountryHint: tt.hint,
					Location:    tt.location,
				},
			}}
			repo := &spyRepo{}
			proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
				ResolveFetcher: resolveTo(fetcher),
				Repository:     repo,
			})

			if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"}); err != nil {
				t.Fatalf("Process: %v", err)
			}
			if len(repo.saved) != 1 {
				t.Fatalf("saved %d listings, want 1 (kept even when unresolved)", len(repo.saved))
			}
			if got := repo.saved[0].Country; got != tt.wantCountry {
				t.Errorf("Country = %q, want %q", got, tt.wantCountry)
			}
		})
	}
}

// TestProcessorUnregisteredProviderIsNoOp asserts the clientless-provider
// fail-safe: no Fetch, no Save, and Process returns nil so the pool is never
// errored by a provider that routing should have filtered.
func TestProcessorUnregisteredProviderIsNoOp(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{{Title: "Go Engineer", URL: "u"}}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveNone,
		Repository:     repo,
	})

	if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "lever", TenantSlug: "beta", Owner: "beta.com"}); err != nil {
		t.Fatalf("Process should be a no-op for a clientless provider, got %v", err)
	}
	if fetcher.callCount() != 0 {
		t.Errorf("fetcher called %d times, want 0", fetcher.callCount())
	}
	if len(repo.saved) != 0 {
		t.Errorf("saved %d listings, want 0", len(repo.saved))
	}
}

// TestProcessorFetchErrorIsReturned asserts a board-API failure is wrapped and
// propagated so the pool logs it and the tenant is retried on the next run.
func TestProcessorFetchErrorIsReturned(t *testing.T) {
	boom := errors.New("board api down")
	fetcher := &stubFetcher{err: boom}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
	})

	err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"})
	if err == nil {
		t.Fatal("Process returned nil, want the fetch error propagated")
	}
	if !errors.Is(err, boom) {
		t.Errorf("Process error = %v, want wrapping %v", err, boom)
	}
	if len(repo.saved) != 0 {
		t.Errorf("saved %d listings on fetch error, want 0", len(repo.saved))
	}
}

// TestProcessorIncompleteBoardSavesPartial asserts the save-presence / skip-sweep
// contract (ADR-0035): ErrBoardIncomplete is the one non-fatal fetch error — the
// partial slice riding alongside it is still persisted and the task succeeds, and
// the absence-sweep runs with complete=false so it closes nothing.
func TestProcessorIncompleteBoardSavesPartial(t *testing.T) {
	page := uuid.New()
	fetcher := &stubFetcher{
		listings: []*crawler.JobListing{
			{Title: "Go One", URL: "https://board/1", SourceID: "1", Description: "build services"},
			{Title: "Go Two", URL: "https://board/2", SourceID: "2", Description: "ship services"},
		},
		err: ats.ErrBoardIncomplete,
	}
	repo := &spyRepo{}
	liveness := &spyLiveness{}
	var savedTaps, incompleteTaps int
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher:    resolveTo(fetcher),
		Repository:        repo,
		Liveness:          liveness,
		Dormancy:          &spyDormancy{},
		CycleStart:        time.Now(),
		OnSaved:           func(context.Context) { savedTaps++ },
		OnBoardIncomplete: func(context.Context) { incompleteTaps++ },
	})

	if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", CareerPageID: page}); err != nil {
		t.Fatalf("Process returned %v, want nil (ErrBoardIncomplete is swallowed, not a task failure)", err)
	}
	if len(repo.saved) != 2 {
		t.Fatalf("saved %d listings, want 2 (the partial slice is persisted)", len(repo.saved))
	}
	if savedTaps != 2 {
		t.Errorf("OnSaved fired %d times, want 2", savedTaps)
	}
	if incompleteTaps != 1 {
		t.Errorf("OnBoardIncomplete fired %d times, want 1", incompleteTaps)
	}
	calls := liveness.calls()
	if len(calls) != 1 || calls[0].complete {
		t.Fatalf("CloseAbsent calls = %+v, want one with complete=false (skip the sweep)", calls)
	}
}

// TestProcessorClosesAbsentOnCompleteFetch asserts the absence-sweep runs with
// complete=true, scoped to the task's CareerPageID and watermarked at CycleStart,
// after a clean fetch (ADR-0035); the count closed is tallied through OnClosed.
func TestProcessorClosesAbsentOnCompleteFetch(t *testing.T) {
	page := uuid.New()
	cycleStart := time.Now()
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Go One", URL: "https://board/1", SourceID: "1"},
	}}
	liveness := &spyLiveness{closeReturn: 3}
	var closedTaps int
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     &spyRepo{},
		Liveness:       liveness,
		Dormancy:       &spyDormancy{},
		CycleStart:     cycleStart,
		OnClosed:       func(_ context.Context, n int) { closedTaps += n },
	})

	if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", CareerPageID: page}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	calls := liveness.calls()
	if len(calls) != 1 {
		t.Fatalf("CloseAbsent calls = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.careerPageID != page || !c.complete || !c.notSeenSince.Equal(cycleStart) {
		t.Errorf("CloseAbsent(%v, %v, %v), want (%v, %v, true)", c.careerPageID, c.notSeenSince, c.complete, page, cycleStart)
	}
	if closedTaps != 3 {
		t.Errorf("OnClosed tallied %d, want 3", closedTaps)
	}
}

// TestProcessorRecordsDormancy asserts the board-reach probe feeds Career-Page
// dormancy (ADR-0035): a clean/incomplete fetch is Alive; a board-status error is
// Dead; and an embed-discovered board (Nil CareerPageID) records nothing.
func TestProcessorRecordsDormancy(t *testing.T) {
	page := uuid.New()

	t.Run("successful fetch records Alive", func(t *testing.T) {
		dormancy := &spyDormancy{}
		proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
			ResolveFetcher:    resolveTo(&stubFetcher{listings: []*crawler.JobListing{{URL: "u", SourceID: "1"}}}),
			Repository:        &spyRepo{},
			Liveness:          &spyLiveness{},
			Dormancy:          dormancy,
			DormancyThreshold: 5,
		})
		if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", CareerPageID: page}); err != nil {
			t.Fatalf("Process: %v", err)
		}
		probes := dormancy.recorded()
		if len(probes) != 1 || probes[0].outcome != crawler.ProbeAlive || probes[0].careerPageID != page || probes[0].threshold != 5 {
			t.Fatalf("dormancy probes = %+v, want one Alive for page %v at threshold 5", probes, page)
		}
	})

	t.Run("board-status error records Dead", func(t *testing.T) {
		dormancy := &spyDormancy{}
		proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
			ResolveFetcher: resolveTo(&stubFetcher{err: ats.ErrBoardStatus}),
			Repository:     &spyRepo{},
			Liveness:       &spyLiveness{},
			Dormancy:       dormancy,
		})
		err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", CareerPageID: page})
		if err == nil || !errors.Is(err, ats.ErrBoardStatus) {
			t.Fatalf("Process error = %v, want the board-status error propagated", err)
		}
		probes := dormancy.recorded()
		if len(probes) != 1 || probes[0].outcome != crawler.ProbeDead {
			t.Fatalf("dormancy probes = %+v, want one Dead", probes)
		}
	})

	t.Run("embed board with Nil CareerPageID records nothing", func(t *testing.T) {
		dormancy := &spyDormancy{}
		liveness := &spyLiveness{}
		proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
			ResolveFetcher: resolveTo(&stubFetcher{listings: []*crawler.JobListing{{URL: "u", SourceID: "1"}}}),
			Repository:     &spyRepo{},
			Liveness:       liveness,
			Dormancy:       dormancy,
		})
		if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"}); err != nil {
			t.Fatalf("Process: %v", err)
		}
		if len(dormancy.recorded()) != 0 {
			t.Errorf("dormancy recorded %d probes for a Nil-page embed board, want 0", len(dormancy.recorded()))
		}
		if len(liveness.calls()) != 0 {
			t.Errorf("CloseAbsent called %d times for a Nil-page embed board, want 0", len(liveness.calls()))
		}
	})
}

// TestProcessorContinuesAfterSaveError asserts one failed save neither drops the
// rest of the tenant nor swallows the error: the second posting is still saved,
// OnSaved fires only for the success, and the joined save error is returned.
func TestProcessorContinuesAfterSaveError(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Go One", URL: "https://boards.greenhouse.io/acme/jobs/1"},
		{Title: "Go Two", URL: "https://boards.greenhouse.io/acme/jobs/2"},
	}}
	repo := &spyRepo{failOn: func(jl *crawler.JobListing) bool { return jl.Title == "Go One" }}
	var savedTaps int
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
		OnSaved:        func(context.Context) { savedTaps++ },
	})

	err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"})
	if err == nil {
		t.Fatal("Process returned nil, want the save error returned")
	}
	if !errors.Is(err, errSaveFailed) {
		t.Errorf("Process error = %v, want wrapping the save failure", err)
	}
	if len(repo.saved) != 1 || repo.saved[0].Title != "Go Two" {
		t.Errorf("saved = %v, want just [Go Two] (one bad save must not drop the rest)", repo.saved)
	}
	if savedTaps != 1 {
		t.Errorf("OnSaved fired %d times, want 1 (only the successful save)", savedTaps)
	}
}
