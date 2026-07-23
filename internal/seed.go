package crawler

import "github.com/google/uuid"

// Seed is a crawl entry point plus the two ADR-0021 provenance keys every
// descendant URL inherits unchanged (Scope, the fence key; Owner, the
// attribution key). An empty Scope+Owner is the roam signal: a Discovery Crawl
// seeds bare URLs with no provenance and is meant to walk onto any host, while a
// Keyword Crawl fills both from the Catalog so the crawl stays fenced to — and
// attributes its Job Listings to — a single Company.
type Seed struct {
	URL   string
	Scope string
	Owner string
}

// CatalogSeed is a Keyword-Crawl seed drawn from the Catalog: a URL paired with
// its owning Company's stored CompanyKey (the seed's Owner). Scope is
// deliberately absent — it is derived from the URL later via catalog.Identify,
// which is why the two keys can legitimately diverge (an imported Company may
// carry an explicit key that differs from its URL-derived one). This is the
// shape the Catalog seed queries return.
type CatalogSeed struct {
	URL        string
	CompanyKey string
}

// CollectionSeed is a Collection Cycle seed resolved from the Catalog (ADR-0036):
// a URL, the owning Company's stored CompanyKey (the seed's Owner / attribution
// key), and — for a Career Page seed — the career_page.id it was drawn from so the
// crawl lane can attribute and sweep by page. CareerPageID is uuid.Nil for a
// Pageless Company's Website seed (no career page). It carries CareerPageID, which
// CatalogSeed lacks; Scope is derived later from the URL via catalog.Identify.
type CollectionSeed struct {
	URL          string
	CompanyKey   string
	CareerPageID uuid.UUID
}

// SeedsFromURLs maps bare URLs to Seeds with empty Scope and Owner — the
// roaming provenance a Discovery Crawl seeds with. It returns an empty
// (non-nil) slice for nil or empty input.
func SeedsFromURLs(urls []string) []Seed {
	seeds := []Seed{}
	for _, u := range urls {
		seeds = append(seeds, Seed{URL: u})
	}
	return seeds
}
