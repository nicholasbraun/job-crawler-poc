package catalog

import (
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// ResolveSeeds pairs each Catalog seed's two ADR-0021 provenance keys: Scope,
// the URL-derived CompanyKey computed here via Identify (the fence key a later
// guardrail tests discovered links against), and Owner, the Company's
// catalog-stored CompanyKey carried on the CatalogSeed (the attribution key).
// This is the single place the two keys are paired, and where they may
// legitimately diverge — an imported Company whose explicit key differs from its
// URL-derived one keeps the stored key as Owner while Scope follows the URL.
//
// A seed whose URL fails to parse, or whose URL yields no CompanyKey (a
// host-less URL such as a schemeless "careers.acme.com" or a "mailto:" address,
// for which Identify returns an empty key), is dropped. This makes the invariant
// the fence relies on hold at the source: every returned Seed carries a real,
// non-empty Scope and never accidentally roams — an empty Scope must mean "roam"
// (Discovery only), never "a Keyword seed we failed to key". Returns an empty
// (non-nil) slice for nil or empty input.
func ResolveSeeds(seeds []crawler.CatalogSeed) []crawler.Seed {
	resolved := []crawler.Seed{}
	for _, s := range seeds {
		u, err := crawler.NewURL(s.URL)
		if err != nil {
			continue
		}
		scope := Identify(u).CompanyKey
		if scope == "" {
			continue
		}
		resolved = append(resolved, crawler.Seed{
			URL:   s.URL,
			Scope: scope,
			Owner: s.CompanyKey,
		})
	}
	return resolved
}
