package collection

import (
	"strings"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// RouteSeeds resolves each CollectionSeed's Scope (via catalog.Identify) and
// partitions the seeds into the three Collection Cycle lanes (ADR-0035/0036):
//
//   - crawlSeeds: non-ATS (self-hosted or clientless-ATS) seeds → the walk
//     frontier. Each carries Scope (the URL-derived fence key) and Owner (the
//     Catalog attribution key), so the walk fences and attributes per ADR-0021.
//   - tasks: ATS FetchTasks carrying CareerPageID → the ATS Fetch lane, which pulls
//     the tenant's board straight from the provider API.
//   - refetchPages: the non-ATS CAREER pages (CareerPageID != Nil) → the refetch +
//     dormancy lane. A Pageless-Company seed (CareerPageID == Nil) is excluded — it
//     has no Career Page to scope liveness to.
//
// hasFetcher is the registry predicate, kept a param so this stays pure, table-tested
// partition logic. A seed whose URL fails to parse or yields no CompanyKey is dropped,
// mirroring catalog.ResolveSeeds so every routed seed carries a real Scope. All three
// returned slices are non-nil.
func RouteSeeds(seeds []crawler.CollectionSeed, hasFetcher func(provider string) bool) (crawlSeeds []crawler.Seed, tasks []atsingest.FetchTask, refetchPages []crawler.CollectionSeed) {
	crawlSeeds = []crawler.Seed{}
	tasks = []atsingest.FetchTask{}
	refetchPages = []crawler.CollectionSeed{}

	for _, s := range seeds {
		u, err := crawler.NewURL(s.URL)
		if err != nil {
			continue
		}
		id := catalog.Identify(u)
		if id.CompanyKey == "" {
			continue
		}
		// A recognized ATS tenant with a registered board fetcher is pulled directly and
		// never crawled (ADR-0022). Its CompanyKey is "provider:slug" by construction, so
		// trimming the "provider:" prefix recovers the tenant slug exactly.
		if id.ATSProvider != "" && hasFetcher(id.ATSProvider) {
			slug := strings.TrimPrefix(id.CompanyKey, id.ATSProvider+":")
			tasks = append(tasks, atsingest.FetchTask{
				Provider:     id.ATSProvider,
				TenantSlug:   slug,
				Owner:        s.CompanyKey,
				CareerPageID: s.CareerPageID,
			})
			continue
		}
		crawlSeeds = append(crawlSeeds, crawler.Seed{
			URL:   s.URL,
			Scope: id.CompanyKey,
			Owner: s.CompanyKey,
		})
		// A crawled Career Page (not a pageless Company Website) also feeds the refetch +
		// dormancy lane, which owns liveness of its known-open postings.
		if s.CareerPageID != uuid.Nil {
			refetchPages = append(refetchPages, s)
		}
	}
	return crawlSeeds, tasks, refetchPages
}
