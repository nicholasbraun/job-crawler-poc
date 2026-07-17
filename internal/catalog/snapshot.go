package catalog

import crawler "github.com/nicholasbraun/job-crawler-poc/internal"

// NewCompanySnapshot builds the per-run CompanyKey → Company-name lookup a Keyword
// Crawl uses to attribute a saved Job Listing to its Owner Company (ADR-0021)
// rather than to the extractor's guess. Keyed by each Company's stored CompanyKey;
// the name is the one derived and stored at catalog time. Returns a non-nil
// (possibly empty) map.
func NewCompanySnapshot(companies []*crawler.Company) map[string]string {
	snapshot := make(map[string]string, len(companies))
	for _, c := range companies {
		snapshot[c.CompanyKey] = c.Name
	}
	return snapshot
}
