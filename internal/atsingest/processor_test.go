package atsingest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
)

// TestProcessorSavesOnlyKeywordMatches asserts the relevance gate and that the
// saved record carries the fetcher's canonical posting URL under the crawl
// definition — the (definitionID, URL) upsert key.
func TestProcessorSavesOnlyKeywordMatches(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Go Engineer", URL: "https://boards.greenhouse.io/acme/jobs/1", Description: "build services"},
		{Title: "Sales Rep", URL: "https://boards.greenhouse.io/acme/jobs/2", Description: "close deals"},
	}}
	repo := &spyRepo{}
	defID := uuid.New()
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
		DefinitionID:   defID,
		Keywords:       []string{"go"},
		CompanyNames:   map[string]string{"acme.com": "Acme Inc"},
	})

	task := &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}
	if err := proc.Process(t.Context(), task); err != nil {
		t.Fatalf("Process: %v", err)
	}

	if got := fetcher.lastTenant(); got != "acme" {
		t.Errorf("fetched tenant = %q, want acme", got)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d listings, want 1 (only the keyword match)", len(repo.saved))
	}
	got := repo.saved[0]
	if got.Title != "Go Engineer" {
		t.Errorf("saved title = %q, want %q", got.Title, "Go Engineer")
	}
	if got.URL != "https://boards.greenhouse.io/acme/jobs/1" {
		t.Errorf("saved URL = %q, want the canonical posting URL", got.URL)
	}
	if repo.lastDefID != defID {
		t.Errorf("saved under definition %v, want %v", repo.lastDefID, defID)
	}
}

// TestProcessorMatchesOnDescription proves the title-OR-description relevance
// rule: a posting whose title lacks the keyword but whose description carries it
// is still saved.
func TestProcessorMatchesOnDescription(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{
		{Title: "Backend Engineer", URL: "u", Description: "experience with Go and Kubernetes"},
	}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
		DefinitionID:   uuid.New(),
		Keywords:       []string{"go"},
	})

	if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 1 {
		t.Fatalf("saved %d, want 1 (matched via description)", len(repo.saved))
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
			DefinitionID:   uuid.New(),
			Keywords:       []string{"go"},
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
			DefinitionID:   uuid.New(),
			Keywords:       []string{"go"},
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

// TestProcessorUnregisteredProviderIsNoOp asserts the clientless-provider
// fail-safe: no Fetch, no Save, and Process returns nil so the pool is never
// errored by a provider that routing should have filtered.
func TestProcessorUnregisteredProviderIsNoOp(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{{Title: "Go Engineer", URL: "u"}}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveNone,
		Repository:     repo,
		DefinitionID:   uuid.New(),
		Keywords:       []string{"go"},
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
		DefinitionID:   uuid.New(),
		Keywords:       []string{"go"},
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
		DefinitionID:   uuid.New(),
		Keywords:       []string{"go"},
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

// TestProcessorEmptyKeywordsSaveNothing is the defensive case: with no keywords
// the relevance gate rejects everything, exactly like the crawl lane's Reject.
func TestProcessorEmptyKeywordsSaveNothing(t *testing.T) {
	fetcher := &stubFetcher{listings: []*crawler.JobListing{{Title: "Go Engineer", URL: "u"}}}
	repo := &spyRepo{}
	proc := atsingest.NewProcessor(&atsingest.ProcessorConfig{
		ResolveFetcher: resolveTo(fetcher),
		Repository:     repo,
		DefinitionID:   uuid.New(),
		Keywords:       nil,
	})

	if err := proc.Process(t.Context(), &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme"}); err != nil {
		t.Fatalf("Process: %v", err)
	}
	if len(repo.saved) != 0 {
		t.Errorf("saved %d listings with no keywords, want 0", len(repo.saved))
	}
}
