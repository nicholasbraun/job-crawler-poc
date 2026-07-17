package catalog

import crawler "github.com/nicholasbraun/job-crawler-poc/internal"

// InScope reports whether child belongs to the Keyword Crawl Scope keyed by
// scope — the seed's URL-derived CompanyKey (ADR-0021). It is the fence that
// confines a Keyword Crawl to its seed Company: a self-hosted seed to its
// registrable domain and every subdomain, an ATS seed to its single
// provider:tenant. Sibling ATS tenants, other registrable domains, and
// off-catalog hosts are rejected — deliberately stricter than a blanket
// ATS-host allowlist, so a self-hosted seed does not follow a link even onto a
// known ATS host.
//
// An empty scope means "roam" — only the Discovery Crawl carries no Scope, and
// ResolveSeeds guarantees a Keyword seed never does — so every child passes.
func InScope(scope string, child crawler.URL) bool {
	if scope == "" {
		return true
	}
	return Identify(child).CompanyKey == scope
}
