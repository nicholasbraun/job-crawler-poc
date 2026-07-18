package atsingest

import (
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// RouteSeeds partitions resolved Keyword-Crawl Seeds into the Seeds that stay on
// the crawl-and-fence path and the tenants the ATS Fetch lane pulls directly from
// a provider board API (ADR-0022). A Seed whose URL catalog.Identify resolves to
// an ATS provider that hasFetcher reports registered becomes a FetchTask, and its
// tenant is NOT crawled; every other Seed — self-hosted, or an ATS provider with
// no board-API client (a clientless ATS) — is returned unchanged in crawl, so it
// enters the Frontier and fences to its Company per ADR-0021.
//
// hasFetcher is a predicate (not the Registry) so this stays pure, table-tested
// partition logic. A Seed whose URL fails to parse is kept on the crawl path (the
// orchestrator already tolerates a bad seed). Both returned slices are non-nil.
func RouteSeeds(seeds []crawler.Seed, hasFetcher func(provider string) bool) (crawl []crawler.Seed, tasks []FetchTask) {
	crawl = []crawler.Seed{}
	tasks = []FetchTask{}
	for _, s := range seeds {
		u, err := crawler.NewURL(s.URL)
		if err != nil {
			crawl = append(crawl, s)
			continue
		}
		id := catalog.Identify(u)
		// A bare board host with no tenant yields ATSProvider == "" (Identify falls
		// back to eTLD+1), so it never routes here — only a real tenant does. When a
		// provider is recognized, its CompanyKey is "provider:slug" by construction,
		// so trimming the "provider:" prefix recovers the tenant slug exactly.
		if id.ATSProvider != "" && hasFetcher(id.ATSProvider) {
			slug := strings.TrimPrefix(id.CompanyKey, id.ATSProvider+":")
			tasks = append(tasks, FetchTask{
				Provider:   id.ATSProvider,
				TenantSlug: slug,
				Owner:      s.Owner,
			})
			continue
		}
		crawl = append(crawl, s)
	}
	return crawl, tasks
}
