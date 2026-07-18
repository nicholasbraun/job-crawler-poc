package atsingest_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
)

// hasGreenhouse is the registered-provider predicate for the routing table: only
// "greenhouse" has a board fetcher, so a Lever tenant exercises the clientless-ATS
// fallback (recognized by Identify, but not routed to the lane).
func hasGreenhouse(provider string) bool { return provider == "greenhouse" }

func TestRouteSeeds(t *testing.T) {
	tests := []struct {
		name       string
		seed       crawler.Seed
		hasFetcher func(string) bool
		wantCrawl  bool                 // the seed stays on the crawl-and-fence path
		wantTask   *atsingest.FetchTask // the routed task, or nil for none
	}{
		{
			name:       "greenhouse tenant with a registered fetcher routes to the lane",
			seed:       crawler.Seed{URL: "https://boards.greenhouse.io/acme/jobs/123", Scope: "greenhouse:acme", Owner: "acme.com"},
			hasFetcher: hasGreenhouse,
			wantCrawl:  false,
			wantTask:   &atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"},
		},
		{
			name:       "greenhouse tenant with no registered fetcher stays a crawl seed",
			seed:       crawler.Seed{URL: "https://boards.greenhouse.io/acme/jobs/123", Scope: "greenhouse:acme", Owner: "acme.com"},
			hasFetcher: func(string) bool { return false },
			wantCrawl:  true,
		},
		{
			name:       "self-hosted seed stays a crawl seed",
			seed:       crawler.Seed{URL: "https://careers.acme.com/jobs", Scope: "acme.com", Owner: "acme.com"},
			hasFetcher: hasGreenhouse,
			wantCrawl:  true,
		},
		{
			name:       "bare board host with no tenant stays a crawl seed",
			seed:       crawler.Seed{URL: "https://boards.greenhouse.io", Scope: "greenhouse.io", Owner: "greenhouse.io"},
			hasFetcher: hasGreenhouse,
			wantCrawl:  true,
		},
		{
			name:       "unparseable url stays a crawl seed",
			seed:       crawler.Seed{URL: "http://\x7f", Owner: "acme.com"},
			hasFetcher: hasGreenhouse,
			wantCrawl:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			crawl, tasks := atsingest.RouteSeeds([]crawler.Seed{tt.seed}, tt.hasFetcher)

			if tt.wantCrawl {
				if len(tasks) != 0 {
					t.Errorf("tasks = %v, want none", tasks)
				}
				if len(crawl) != 1 || crawl[0] != tt.seed {
					t.Errorf("crawl = %v, want [%v] (seed returned unchanged)", crawl, tt.seed)
				}
				return
			}

			if len(crawl) != 0 {
				t.Errorf("crawl = %v, want none (seed routed to the lane, not crawled)", crawl)
			}
			if len(tasks) != 1 {
				t.Fatalf("tasks = %v, want exactly 1", tasks)
			}
			if tasks[0] != *tt.wantTask {
				t.Errorf("task = %+v, want %+v", tasks[0], *tt.wantTask)
			}
		})
	}
}

// TestRouteSeedsPartitionsMixedBatch checks a realistic batch: a routable
// greenhouse tenant is diverted to a task while a self-hosted seed and a
// clientless-ATS (Lever) seed both stay on the crawl path.
func TestRouteSeedsPartitionsMixedBatch(t *testing.T) {
	seeds := []crawler.Seed{
		{URL: "https://boards.greenhouse.io/acme/jobs/1", Owner: "acme.com"},
		{URL: "https://careers.self.com/jobs", Owner: "self.com"},
		{URL: "https://jobs.lever.co/beta", Owner: "beta.com"},
	}

	crawl, tasks := atsingest.RouteSeeds(seeds, hasGreenhouse)

	if len(tasks) != 1 {
		t.Fatalf("tasks = %v, want exactly one (greenhouse:acme)", tasks)
	}
	want := atsingest.FetchTask{Provider: "greenhouse", TenantSlug: "acme", Owner: "acme.com"}
	if tasks[0] != want {
		t.Errorf("task = %+v, want %+v", tasks[0], want)
	}
	if len(crawl) != 2 {
		t.Fatalf("crawl = %v, want 2 (self-hosted + clientless-ATS Lever)", crawl)
	}
}

// TestRouteSeedsReturnsNonNilSlices guards the invariant that both partitions are
// always non-nil, so callers can range/append without a nil check.
func TestRouteSeedsReturnsNonNilSlices(t *testing.T) {
	crawl, tasks := atsingest.RouteSeeds(nil, hasGreenhouse)
	if crawl == nil || tasks == nil {
		t.Fatalf("want non-nil slices, got crawl=%v tasks=%v", crawl, tasks)
	}
	if len(crawl) != 0 || len(tasks) != 0 {
		t.Errorf("want empty slices for empty input, got crawl=%v tasks=%v", crawl, tasks)
	}
}
