package atsingest

// FetchTask is one unit of ATS-Fetch work: pull a single tenant's board from an
// ATS provider's board API and attribute its postings to an Owner Company
// (ADR-0022). It is the ATS Fetch lane's pool work item. Seed-time routing
// (RouteSeeds) produces one per routed Keyword-Crawl Seed; #129's embed trigger
// will produce more from boards embedded on crawled pages. Both submit through
// Lane.Submit, which dedups by (Provider, TenantSlug) so a tenant is fetched at
// most once a run.
type FetchTask struct {
	// Provider is the ATS provider family key (e.g. "greenhouse"), equal to the
	// value catalog.Identify emits and the Registry resolves a BoardFetcher by.
	Provider string
	// TenantSlug is the provider-scoped tenant identifier (e.g. "acme"): the board
	// API path segment the fetcher reads.
	TenantSlug string
	// Owner is the ADR-0021 Owner CompanyKey the fetched postings are attributed
	// to — the Catalog key of the Company whose Seed (or, in #129, embedding page)
	// produced this task. The lane stamps it onto every saved Job Listing; the
	// provider board's own company field is never used.
	Owner string
}
