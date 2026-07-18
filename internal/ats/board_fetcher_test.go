package ats_test

import (
	"context"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
)

// stubFetcher is an inline test double for BoardFetcher: it returns its canned
// listings and error, and records the tenant it was called with.
type stubFetcher struct {
	listings    []*crawler.JobListing
	err         error
	calledWith  string
	calledCount int
}

func (s *stubFetcher) Fetch(_ context.Context, tenant string) ([]*crawler.JobListing, error) {
	s.calledWith = tenant
	s.calledCount++
	return s.listings, s.err
}

func TestRegistryFetcher(t *testing.T) {
	t.Run("resolves a registered provider to its fetcher", func(t *testing.T) {
		want := &stubFetcher{listings: []*crawler.JobListing{{Title: "Backend Engineer"}}}
		reg := ats.NewRegistry(ats.WithFetcher("greenhouse", want))

		f, ok := reg.Fetcher("greenhouse")
		if !ok {
			t.Fatal("Fetcher(greenhouse) ok = false, want true")
		}
		got, err := f.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if len(got) != 1 || got[0].Title != "Backend Engineer" {
			t.Errorf("resolved fetcher returned %v, want the registered stub's listings", got)
		}
		if want.calledWith != "acme" {
			t.Errorf("stub called with tenant %q, want %q", want.calledWith, "acme")
		}
	})

	t.Run("an unregistered provider signals crawl fallback", func(t *testing.T) {
		reg := ats.NewRegistry(ats.WithFetcher("greenhouse", &stubFetcher{}))

		f, ok := reg.Fetcher("lever")
		if ok {
			t.Error("Fetcher(lever) ok = true, want false for a provider with no client")
		}
		if f != nil {
			t.Errorf("Fetcher(lever) = %v, want nil", f)
		}
	})

	t.Run("a later registration replaces an earlier one", func(t *testing.T) {
		first := &stubFetcher{listings: []*crawler.JobListing{{Title: "first"}}}
		second := &stubFetcher{listings: []*crawler.JobListing{{Title: "second"}}}
		reg := ats.NewRegistry(
			ats.WithFetcher("greenhouse", first),
			ats.WithFetcher("greenhouse", second),
		)

		f, ok := reg.Fetcher("greenhouse")
		if !ok {
			t.Fatal("Fetcher(greenhouse) ok = false, want true")
		}
		got, err := f.Fetch(t.Context(), "acme")
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if len(got) != 1 || got[0].Title != "second" {
			t.Errorf("resolved fetcher returned %v, want the second registration's listings", got)
		}
		if first.calledCount != 0 {
			t.Errorf("first (replaced) fetcher was called %d times, want 0", first.calledCount)
		}
	})
}

func TestNewDefaultRegistry(t *testing.T) {
	reg := ats.NewDefaultRegistry()

	if _, ok := reg.Fetcher(ats.ProviderGreenhouse); !ok {
		t.Errorf("NewDefaultRegistry did not wire the Greenhouse fetcher")
	}
	// #128 ships the Lever board-API client, so the default registry now resolves
	// it too rather than falling back to the crawl path.
	if _, ok := reg.Fetcher(ats.ProviderLever); !ok {
		t.Errorf("NewDefaultRegistry did not wire the Lever fetcher")
	}
	// #134 ships the Personio (XML) board-API client, so the default registry
	// resolves it too rather than falling back to the crawl path.
	if _, ok := reg.Fetcher(ats.ProviderPersonio); !ok {
		t.Errorf("NewDefaultRegistry did not wire the Personio fetcher")
	}
	// #135 ships the Workable (widget-JSON) board-API client, so the default
	// registry resolves it too rather than falling back to the crawl path.
	if _, ok := reg.Fetcher(ats.ProviderWorkable); !ok {
		t.Errorf("NewDefaultRegistry did not wire the Workable fetcher")
	}
}
