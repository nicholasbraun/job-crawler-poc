// Package catalog derives ATS-aware Company identity from a crawled URL
// (ADR-0001) and classifies a URL as a Career Page (the index that lists a
// Company's open jobs) versus a single Job Listing beneath it. It is pure,
// table-tested logic with no repository dependencies: given a URL it computes
// the globally-unique, provider-qualified CompanyKey used to attribute a Career
// Page to a Company, while keeping the host-based Politeness Domain separate so
// multi-tenant ATS hosts are never collapsed into a single fake company.
package catalog

import (
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"golang.org/x/net/publicsuffix"
)

// Identity is the ATS-aware identity computed for a Career Page URL.
type Identity struct {
	// CompanyKey is globally unique and provider-qualified: "greenhouse:acme"
	// for an ATS tenant, or the eTLD+1 "acme.com" for a self-hosted page. It is
	// the natural key a Company is upserted on.
	CompanyKey string
	// ATSProvider is the ATS host family ("greenhouse", "lever", …) or "" for a
	// self-hosted career page.
	ATSProvider string
	// PolitenessDomain is the host used for rate limiting (= URL.Hostname). It
	// is deliberately host-based and may be shared across ATS tenants.
	PolitenessDomain string
}

// Role classifies a URL's position within a recognized ATS: the tenant's board
// root (a Career Page) versus a single posting beneath it (a Job Listing).
// RoleUnknown means the URL is not on a recognized ATS host, so its role must be
// decided from page content rather than URL structure.
type Role int

const (
	RoleUnknown Role = iota
	RoleCareerPage
	RoleJobListing
)

// ruleKind distinguishes how an ATS lays out its tenants.
type ruleKind int

const (
	ruleNone      ruleKind = iota
	rulePath               // tenant under a path segment on a shared board host
	ruleSubdomain          // tenant on a per-tenant subdomain
)

// pathRule matches an ATS whose tenants live under a path segment on a shared
// board host (e.g. boards.greenhouse.io/acme). The Career Page is the "/{slug}"
// root; the slug is the first path segment.
type pathRule struct {
	provider string
	hosts    []string
}

// subdomainRule matches an ATS whose tenants live on a per-tenant subdomain
// (e.g. acme.recruitee.com). The Career Page is the host root; the slug is the
// leftmost host label preceding suffix.
type subdomainRule struct {
	provider string
	suffix   string
}

// pathRules are checked before subdomainRules so a path-based board host that
// also shares a subdomain suffix (apply.workable.com vs {slug}.workable.com)
// is not mis-slugged as a tenant subdomain.
var pathRules = []pathRule{
	{provider: "greenhouse", hosts: []string{
		"boards.greenhouse.io", "job-boards.greenhouse.io",
		// EU-region board hosts: same tenant-under-path layout, different subdomain.
		"boards.eu.greenhouse.io", "job-boards.eu.greenhouse.io",
	}},
	{provider: "lever", hosts: []string{"jobs.lever.co"}},
	{provider: "ashby", hosts: []string{"jobs.ashbyhq.com"}},
	{provider: "workable", hosts: []string{"apply.workable.com"}},
}

var subdomainRules = []subdomainRule{
	{provider: "personio", suffix: "jobs.personio.de"},
	{provider: "personio", suffix: "jobs.personio.com"},
	{provider: "recruitee", suffix: "recruitee.com"},
	{provider: "workable", suffix: "workable.com"},
}

// atsMatch describes how a host maps to a known ATS tenant.
type atsMatch struct {
	provider string
	kind     ruleKind
	// slug is the tenant label for subdomain rules; for path rules the slug is
	// taken from the URL path instead, so this is empty.
	slug string
}

// matchHost resolves host to a known ATS rule, or reports ok=false.
func matchHost(host string) (atsMatch, bool) {
	for _, r := range pathRules {
		for _, h := range r.hosts {
			if host == h {
				return atsMatch{provider: r.provider, kind: rulePath}, true
			}
		}
	}
	for _, r := range subdomainRules {
		if label, ok := subdomainLabel(host, r.suffix); ok {
			return atsMatch{provider: r.provider, kind: ruleSubdomain, slug: label}, true
		}
	}
	return atsMatch{}, false
}

// Identify computes the ATS-aware Identity of u. A URL whose host matches a
// known ATS provider yields a provider-qualified CompanyKey; anything else
// falls back to the publicsuffix eTLD+1 of the host with an empty ATSProvider
// (self-hosted).
func Identify(u crawler.URL) Identity {
	host := u.Hostname
	id := Identity{
		PolitenessDomain: host,
	}

	if provider, slug := matchATS(u); slug != "" {
		id.ATSProvider = provider
		id.CompanyKey = provider + ":" + slug
		return id
	}

	id.CompanyKey = eTLDPlusOne(host)
	return id
}

// Classify reports whether u is an ATS Career Page (the tenant board root), an
// ATS Job Listing (a posting beneath the root), or on an unrecognized host
// (RoleUnknown), where the caller must decide from page content.
func Classify(u crawler.URL) Role {
	m, ok := matchHost(u.Hostname)
	if !ok {
		return RoleUnknown
	}

	segs := pathSegments(u.RawURL)
	switch m.kind {
	case rulePath:
		// The board root is exactly "/{slug}"; anything deeper is a posting,
		// and a bare host with no tenant slug is not a company Career Page.
		if len(segs) == 1 {
			return RoleCareerPage
		}
		return RoleJobListing
	case ruleSubdomain:
		// The board root is the host root; any path segment is a posting.
		if len(segs) == 0 {
			return RoleCareerPage
		}
		return RoleJobListing
	default:
		return RoleUnknown
	}
}

// CareerPageURL returns the canonical Career Page (board-root) URL for a URL on
// a known ATS host — every greenhouse tenant URL collapses to
// "https://job-boards.greenhouse.io/{slug}", so pagination and posting variants
// upsert to a single Career Page per Company. ok is false when the host is not a
// recognized ATS (self-hosted), in which case the caller uses the page's own URL.
func CareerPageURL(u crawler.URL) (string, bool) {
	m, ok := matchHost(u.Hostname)
	if !ok {
		return "", false
	}

	parsed, err := url.Parse(u.RawURL)
	if err != nil {
		return "", false
	}
	base := parsed.Scheme + "://" + parsed.Host

	switch m.kind {
	case rulePath:
		slug := strings.ToLower(firstPathSegment(u.RawURL))
		if slug == "" {
			return "", false
		}
		return base + "/" + slug, true
	case ruleSubdomain:
		return base, true
	default:
		return "", false
	}
}

// matchATS returns the provider and tenant slug for a URL on a known ATS host,
// or an empty slug when the host is not recognized (or is a board host with no
// tenant segment).
func matchATS(u crawler.URL) (provider, slug string) {
	m, ok := matchHost(u.Hostname)
	if !ok {
		return "", ""
	}
	switch m.kind {
	case rulePath:
		return m.provider, strings.ToLower(firstPathSegment(u.RawURL))
	case ruleSubdomain:
		return m.provider, strings.ToLower(m.slug)
	default:
		return "", ""
	}
}

// pathSegments returns the non-empty path segments of rawURL.
func pathSegments(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	segs := []string{}
	for _, s := range strings.Split(parsed.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}
	return segs
}

// firstPathSegment returns the first non-empty path segment of rawURL, or "".
func firstPathSegment(rawURL string) string {
	if segs := pathSegments(rawURL); len(segs) > 0 {
		return segs[0]
	}
	return ""
}

// subdomainLabel returns the leftmost host label preceding suffix (the tenant
// slug) when host is "{label}.{suffix}", and false otherwise (including when
// host equals suffix, i.e. no tenant).
func subdomainLabel(host, suffix string) (string, bool) {
	prefix, ok := strings.CutSuffix(host, "."+suffix)
	if !ok || prefix == "" {
		return "", false
	}
	// The tenant slug is the label immediately left of the suffix.
	labels := strings.Split(prefix, ".")
	return labels[len(labels)-1], true
}

// aggregatorHosts are registrable domains (eTLD+1) that are never a single
// company's Career Page hub: multi-company job boards, job aggregators,
// professional networks, and VC-portfolio board platforms. Cataloguing them
// pollutes the Catalog with non-hub pages (#45) and, downstream, mints a fake
// Company that swallows many real employers (#46). A per-tenant ATS or
// recruiting-platform host (e.g. smartrecruiters, join.com/companies) is
// deliberately absent -- those are legitimate single-company hubs whose only
// defect is identity attribution, canonicalized separately in #46. Matched on
// eTLD+1, so every subdomain (de.linkedin.com, jobsinvc.getro.com) folds in.
// This is a curated denylist, extended as the gold-set harness (#44) surfaces
// more.
var aggregatorHosts = map[string]struct{}{
	// Job boards and aggregators (multi-company listings).
	"builtin.com":          {}, // + builtin<city> siblings
	"builtinnyc.com":       {},
	"indeed.com":           {},
	"indeed.de":            {},
	"glassdoor.com":        {},
	"glassdoor.de":         {},
	"stepstone.de":         {},
	"stepstone.com":        {},
	"monster.com":          {},
	"monster.de":           {},
	"crunchboard.com":      {},
	"remoteok.com":         {}, // remote-work job aggregator (multi-company)
	"beck-stellenmarkt.de": {}, // legal job board
	"lto.de":               {}, // legal news site job board
	// Professional networks and employer-review sites.
	"linkedin.com": {},
	"linkedin.de":  {},
	"xing.com":     {},
	"kununu.com":   {}, // employer reviews (XING-owned)
	// VC-portfolio board platforms.
	"getro.com":       {}, // powers many portfolio boards; tenants on *.getro.com fold in via eTLD+1
	"speedinvest.com": {},
	"hv.capital":      {}, // HV Capital; ".capital" is the live gTLD domain (not hvcapital.com)
}

// IsAggregatorHost reports whether u sits on a known multi-company aggregator,
// VC-portfolio board, or professional network -- a host that never represents a
// single company's Career Page. Discovery rejects such pages at the gate so they
// never reach the Catalog or Company identity. Matched on the registrable domain,
// so every subdomain of a listed host (e.g. jobsinvc.getro.com) is covered.
func IsAggregatorHost(u crawler.URL) bool {
	_, ok := aggregatorHosts[eTLDPlusOne(strings.ToLower(u.Hostname))]
	return ok
}

// RegistrableDomain returns the eTLD+1 of host — e.g. "remote.com" for
// "jobs.remote.com" — or "" when host is empty. Used to reduce a company's own
// website host to its registrable domain for DisplayDomain.
func RegistrableDomain(host string) string {
	if host == "" {
		return ""
	}
	return eTLDPlusOne(host)
}

// eTLDPlusOne returns the registrable domain (eTLD+1) of host — e.g.
// "acme.com" for "careers.acme.com", "acme.co.uk" for "jobs.acme.co.uk".
// It falls back to the raw host when the publicsuffix lookup fails.
func eTLDPlusOne(host string) string {
	if domain, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return domain
	}
	return host
}
