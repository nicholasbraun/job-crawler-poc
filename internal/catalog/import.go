package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// ImportRecord is one decoded NDJSON line of a Catalog Import file (ADR-0015).
// Pointer fields carry JSON-presence semantics (ADR-0013): nil means the field
// was absent from the line, a pointer to the zero value means it was written
// explicitly (e.g. "atsProvider": "" deliberately sets self-hosted).
type ImportRecord struct {
	CompanyKey    *string            `json:"companyKey"`
	ATSProvider   *string            `json:"atsProvider"`
	Name          *string            `json:"name"`
	DisplayDomain *string            `json:"displayDomain"`
	Website       *string            `json:"website"`
	FirstSeen     *time.Time         `json:"firstSeen"`
	LastSeen      *time.Time         `json:"lastSeen"`
	CareerPages   []ImportPageRecord `json:"careerPages"`
}

// ImportPageRecord is one Career Page nested in an ImportRecord.
type ImportPageRecord struct {
	URL       string     `json:"url"`
	FirstSeen *time.Time `json:"firstSeen"`
	LastSeen  *time.Time `json:"lastSeen"`
}

// ResolvedRecord is the write-ready form of one import line: identity resolved
// down the Identity Ladder, URLs canonicalised, derivation defaults applied.
// Pages holds the record's valid Career Pages in file order (entries that
// canonicalise identically are kept — ADR-0013 merges converge); PageErrors
// holds the per-page data errors of a record whose identity did not depend on
// its pages (sub-line best-effort: the Company and valid Pages still land).
type ResolvedRecord struct {
	Company    ResolvedCompany
	Pages      []ResolvedPage
	PageErrors []error
}

// ResolvedCompany is the Company half of a resolved import line. CompanyKey is
// the Identity Ladder's product and never empty. Each mutable field pairs the
// value to write with a Present flag preserving JSON presence: a present field
// is an explicit file value a presence-wins merge must apply on update; an
// absent field holds the derivation default, valid only as a first-insert
// default — applying it on update could overwrite richer catalogued data the
// file never mentioned (e.g. a Catalog Doctor correction).
type ResolvedCompany struct {
	CompanyKey           string
	ATSProvider          string // default: key prefix before ":" ("" = self-hosted)
	ATSProviderPresent   bool
	Name                 string // default: ATS tenant slug, else the key itself
	NamePresent          bool
	DisplayDomain        string // default: Website's registrable domain, else a self-hosted key, else ""
	DisplayDomainPresent bool
	Website              string // no default; the file's value verbatim
	WebsitePresent       bool
	FirstSeen            *time.Time // nil = absent; merge defaults to now() on first insert only
	LastSeen             *time.Time
}

// ResolvedPage is one Career Page of a resolved import line, in the exact form
// the Catalog stores: URL is the storage form (StoredCareerPageURL), and
// PolitenessDomain is the page URL's own lowercased host — never carried by the
// file, always derived, so imported rows are indistinguishable from discovered
// ones (ADR-0013).
type ResolvedPage struct {
	URL              string
	PolitenessDomain string
	FirstSeen        *time.Time
	LastSeen         *time.Time
}

// DecodeImportRecord decodes one NDJSON line into an ImportRecord, reporting a
// malformed line as a data error. Unknown fields are ignored so files produced
// from arbitrary sources (ADR-0015) are not rejected for extra keys.
func DecodeImportRecord(line []byte) (ImportRecord, error) {
	var rec ImportRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return ImportRecord{}, fmt.Errorf("invalid json: %w", err)
	}
	return rec, nil
}

// ResolveImportRecord resolves one decoded import line down the Identity
// Ladder (ADR-0013): explicit companyKey, else derivation from the Website's
// registrable domain, else ATS-aware derivation from the Career Page URLs.
// An empty-string companyKey or website is treated as absent — "" is not an
// identity. Errors are user-facing per-record report entries (lowercase, no
// package prefix — deliberately unlike this package's infrastructure errors)
// and always name the offending value.
//
// Whole-record errors: an invalid Website; no identity source at all; and, when
// identity must come from the pages (rung 3), any invalid page URL or pages
// deriving multiple distinct companies. When identity came from the key or
// Website, invalid pages are collected in PageErrors instead and the rest of
// the record resolves (sub-line best-effort).
func ResolveImportRecord(rec ImportRecord) (ResolvedRecord, error) {
	websiteHost := ""
	if rec.Website != nil && *rec.Website != "" {
		u, err := parseImportURL("website", *rec.Website)
		if err != nil {
			return ResolvedRecord{}, err
		}
		websiteHost = u.Hostname
	}

	type validPage struct {
		url  crawler.URL
		page ImportPageRecord
	}
	validPages := []validPage{}
	pageErrs := []error{}
	for _, p := range rec.CareerPages {
		u, err := parseImportURL("career page url", p.URL)
		if err != nil {
			pageErrs = append(pageErrs, err)
			continue
		}
		validPages = append(validPages, validPage{url: u, page: p})
	}

	var key string
	switch {
	case rec.CompanyKey != nil && *rec.CompanyKey != "":
		// File-authoritative, verbatim, no shape validation: the Catalog
		// legitimately holds keys URL-derivation cannot reproduce (LLM or
		// Catalog Doctor attributions).
		key = *rec.CompanyKey
	case websiteHost != "":
		key = RegistrableDomain(websiteHost)
	default:
		if len(rec.CareerPages) == 0 {
			return ResolvedRecord{}, errors.New("record has no identity: set companyKey, website, or careerPages")
		}
		// Every page participates in the identity derivation, so one
		// unparseable page leaves the record's identity uncertain:
		// whole-line failure, no sub-line best-effort at this rung.
		if len(pageErrs) > 0 {
			return ResolvedRecord{}, pageErrs[0]
		}
		keys := []string{}
		for _, vp := range validPages {
			k := Identify(vp.url).CompanyKey
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
		slices.Sort(keys)
		if len(keys) > 1 {
			return ResolvedRecord{}, fmt.Errorf("career pages derive multiple companies (%s); split the record or set companyKey", strings.Join(keys, ", "))
		}
		key = keys[0]
	}

	// Derivation defaults mirror what discovery would store for the same key
	// (career_page_processor): provider from the key prefix, display domain
	// from the Website else a self-hosted key, name from the tenant slug.
	providerDefault := keyProvider(key)
	displayDefault := ""
	switch {
	case websiteHost != "":
		displayDefault = RegistrableDomain(websiteHost)
	case !strings.Contains(key, ":"):
		displayDefault = key
	}
	nameDefault := key
	if _, slug, ok := strings.Cut(key, ":"); ok {
		nameDefault = slug
	}

	company := ResolvedCompany{
		CompanyKey: key,
		FirstSeen:  rec.FirstSeen,
		LastSeen:   rec.LastSeen,
	}
	company.ATSProvider, company.ATSProviderPresent = presentOr(rec.ATSProvider, providerDefault)
	company.Name, company.NamePresent = presentOr(rec.Name, nameDefault)
	company.DisplayDomain, company.DisplayDomainPresent = presentOr(rec.DisplayDomain, displayDefault)
	company.Website, company.WebsitePresent = presentOr(rec.Website, "")

	pages := []ResolvedPage{}
	for _, vp := range validPages {
		pages = append(pages, ResolvedPage{
			URL: StoredCareerPageURL(vp.url),
			// The ATS board-root collapse never changes the host, so the
			// parsed original's hostname is the page's Politeness Domain.
			PolitenessDomain: vp.url.Hostname,
			FirstSeen:        vp.page.FirstSeen,
			LastSeen:         vp.page.LastSeen,
		})
	}

	return ResolvedRecord{Company: company, Pages: pages, PageErrors: pageErrs}, nil
}

// parseImportURL parses raw and requires an absolute http(s) URL, returning the
// crawler's normalized form (lowercased host, canonical string). field names
// the record field in the error ("website", "career page url").
func parseImportURL(field, raw string) (crawler.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return crawler.URL{}, fmt.Errorf("%s %q: %w", field, raw, err)
	}
	// url.Parse lowercases the scheme, so "HTTPS://" passes this check.
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return crawler.URL{}, fmt.Errorf("%s %q: must be an absolute http(s) url", field, raw)
	}
	u, err := crawler.NewURL(raw)
	if err != nil {
		return crawler.URL{}, fmt.Errorf("%s %q: %w", field, raw, err)
	}
	return u, nil
}

// keyProvider returns the ATS provider encoded in a CompanyKey's prefix
// ("greenhouse:acme" -> "greenhouse"); a prefixless key (an eTLD+1) is
// self-hosted ("").
func keyProvider(key string) string {
	provider, _, ok := strings.Cut(key, ":")
	if !ok {
		return ""
	}
	return provider
}

// presentOr returns the explicit file value when p is present, else def.
func presentOr(p *string, def string) (value string, present bool) {
	if p != nil {
		return *p, true
	}
	return def, false
}
