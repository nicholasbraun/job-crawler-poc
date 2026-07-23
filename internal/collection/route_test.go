package collection_test

import (
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// hasGreenhouse is the registered-provider predicate: only "greenhouse" has a
// board fetcher, so a Lever tenant exercises the clientless-ATS crawl fallback.
func hasGreenhouse(provider string) bool { return provider == "greenhouse" }

func TestRouteSeeds(t *testing.T) {
	page := uuid.New()

	t.Run("greenhouse tenant with a fetcher becomes a task carrying the career_page_id", func(t *testing.T) {
		crawl, tasks, refetch := collection.RouteSeeds([]crawler.CollectionSeed{
			{URL: "https://boards.greenhouse.io/acme/jobs/1", CompanyKey: "acme.com", CareerPageID: page},
		}, hasGreenhouse)

		if len(crawl) != 0 || len(refetch) != 0 {
			t.Fatalf("routed tenant should not crawl/refetch, got crawl=%v refetch=%v", crawl, refetch)
		}
		if len(tasks) != 1 {
			t.Fatalf("tasks = %v, want 1", tasks)
		}
		got := tasks[0]
		if got.Provider != "greenhouse" || got.TenantSlug != "acme" || got.Owner != "acme.com" || got.CareerPageID != page {
			t.Errorf("task = %+v, want greenhouse/acme owner acme.com page %v", got, page)
		}
	})

	t.Run("clientless-ATS and self-hosted career pages stay crawl seeds and feed the refetch lane", func(t *testing.T) {
		crawl, tasks, refetch := collection.RouteSeeds([]crawler.CollectionSeed{
			{URL: "https://jobs.lever.co/beta", CompanyKey: "beta.com", CareerPageID: uuid.New()},
			{URL: "https://careers.self.com/jobs", CompanyKey: "self.com", CareerPageID: page},
		}, hasGreenhouse)

		if len(tasks) != 0 {
			t.Fatalf("tasks = %v, want none", tasks)
		}
		if len(crawl) != 2 {
			t.Fatalf("crawl = %v, want 2 (clientless-ATS + self-hosted)", crawl)
		}
		// Both are career pages, so both refetch.
		if len(refetch) != 2 {
			t.Fatalf("refetch = %v, want 2", refetch)
		}
		// Scope is the URL-derived key, Owner the catalog key.
		for _, s := range crawl {
			if s.Scope == "" || s.Owner == "" {
				t.Errorf("crawl seed %+v missing Scope/Owner provenance", s)
			}
		}
	})

	t.Run("a pageless-company seed crawls but never refetches (no career page)", func(t *testing.T) {
		crawl, _, refetch := collection.RouteSeeds([]crawler.CollectionSeed{
			{URL: "https://selfco.com", CompanyKey: "selfco.com"}, // CareerPageID == Nil
		}, hasGreenhouse)

		if len(crawl) != 1 {
			t.Fatalf("crawl = %v, want 1", crawl)
		}
		if len(refetch) != 0 {
			t.Errorf("refetch = %v, want none for a pageless seed", refetch)
		}
	})

	t.Run("keyless and unparseable seeds are dropped", func(t *testing.T) {
		crawl, tasks, refetch := collection.RouteSeeds([]crawler.CollectionSeed{
			{URL: "mailto:jobs@acme.com", CompanyKey: "acme.com", CareerPageID: page},
			{URL: "http://\x7f", CompanyKey: "acme.com"},
		}, hasGreenhouse)
		if len(crawl) != 0 || len(tasks) != 0 || len(refetch) != 0 {
			t.Errorf("keyless/unparseable seeds should drop, got crawl=%v tasks=%v refetch=%v", crawl, tasks, refetch)
		}
	})

	t.Run("returns non-nil slices for empty input", func(t *testing.T) {
		crawl, tasks, refetch := collection.RouteSeeds(nil, hasGreenhouse)
		if crawl == nil || tasks == nil || refetch == nil {
			t.Errorf("want non-nil slices, got crawl=%v tasks=%v refetch=%v", crawl, tasks, refetch)
		}
	})
}
