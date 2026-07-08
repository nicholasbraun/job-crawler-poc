package careerpageprocessor

import (
	"encoding/json"
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// nameConnectors split a career-page title into boilerplate and the company
// name, keeping the text after the last occurrence (e.g. "Current openings at
// Remote" -> "Remote").
var nameConnectors = []string{" at ", " bei ", " @ "}

// nameSeparators split a title into parts around a delimiter; the boilerplate
// part is dropped and the company part kept (e.g. "Careers - PostHog" or
// "PostHog | Careers" -> "PostHog").
var nameSeparators = []string{" — ", " – ", " - ", " | ", " · ", " :: "}

// nameSuffixes are trailing boilerplate stripped from a title (e.g.
// "Acme Careers" -> "Acme").
var nameSuffixes = []string{" careers", " career", " jobs", " hiring", " job board"}

// nameLeadingWords are single leading words stripped from a title
// (e.g. "Join Acme" -> "Acme").
var nameLeadingWords = []string{"join", "careers", "career", "jobs"}

// boilerplateWords are the words that, on their own, make a title part generic
// hiring boilerplate rather than a company name.
var boilerplateWords = map[string]bool{
	"careers": true, "career": true, "jobs": true, "job": true, "hiring": true,
	"openings": true, "opening": true, "positions": true, "position": true,
	"vacancies": true, "vacancy": true, "open": true, "join": true, "us": true,
	"team": true, "work": true, "current": true, "karriere": true,
	"stellen": true, "stellenangebote": true, "board": true,
}

// isBoilerplate reports whether every word of part is hiring boilerplate, so the
// part carries no company name (e.g. "Open Positions", "Careers").
func isBoilerplate(part string) bool {
	fields := strings.Fields(strings.ToLower(part))
	if len(fields) == 0 {
		return true
	}
	for _, f := range fields {
		if !boilerplateWords[strings.Trim(f, ".,:|-–—·")] {
			return false
		}
	}
	return true
}

// nameFallback is the last-resort company name when the title yields nothing
// usable: the ATS tenant slug ("greenhouse:acme" -> "acme"), or the registrable
// domain for a self-hosted company.
func nameFallback(identity catalog.Identity) string {
	if _, slug, ok := strings.Cut(identity.CompanyKey, ":"); ok {
		return slug
	}
	return identity.CompanyKey
}

// companyName derives a human-readable company name from a career-page title by
// stripping common board boilerplate. It falls back to fallback (the tenant
// slug) when nothing usable remains.
func companyName(title, fallback string) string {
	name := strings.TrimSpace(title)

	// Leading boilerplate before a connector: "Jobs at <Company>",
	// "Current openings at <Company>". Take the text after the last connector.
	lower := strings.ToLower(name)
	for _, c := range nameConnectors {
		if idx := strings.LastIndex(lower, c); idx != -1 {
			name = strings.TrimSpace(name[idx+len(c):])
			break
		}
	}

	// "<Company> | Careers" / "Careers - <Company>": drop the boilerplate side
	// and keep the company side.
	for _, sep := range nameSeparators {
		if strings.Contains(name, sep) {
			for _, part := range strings.Split(name, sep) {
				if p := strings.TrimSpace(part); p != "" && !isBoilerplate(p) {
					name = p
					break
				}
			}
			break
		}
	}

	// Trailing boilerplate: "<Company> Careers", "<Company> Jobs".
	for _, s := range nameSuffixes {
		if strings.HasSuffix(strings.ToLower(name), s) {
			name = strings.TrimSpace(name[:len(name)-len(s)])
		}
	}

	// A leading word like "Join <Company>" (or a title that is only boilerplate).
	if fields := strings.Fields(name); len(fields) >= 1 {
		for _, w := range nameLeadingWords {
			if strings.EqualFold(fields[0], w) {
				name = strings.TrimSpace(name[len(fields[0]):])
				break
			}
		}
	}

	if name == "" {
		return fallback
	}
	return name
}

// companyDomain derives the company's own registrable domain. For a self-hosted
// page that is the eTLD+1 (which is also the CompanyKey). For an ATS tenant the
// corporate domain is not in the board URL, so it is extracted best-effort from
// the page's Organization JSON-LD; when absent the result is "" (stored NULL).
func companyDomain(identity catalog.Identity, content *crawler.Content) string {
	if identity.ATSProvider == "" {
		return identity.CompanyKey
	}
	return organizationDomain(content.JSONLD)
}

// organizationDomain scans JSON-LD blocks for an Organization (or a
// hiringOrganization) URL and returns its registrable domain, or "" if none is
// found.
func organizationDomain(blocks []string) string {
	for _, block := range blocks {
		var v any
		if err := json.Unmarshal([]byte(block), &v); err != nil {
			continue
		}
		if host := organizationHost(v); host != "" {
			return catalog.RegistrableDomain(host)
		}
	}
	return ""
}

// organizationHost walks a decoded JSON-LD value and returns the host of the
// first Organization/hiringOrganization "url" (or "sameAs") it finds.
func organizationHost(v any) string {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			if h := organizationHost(item); h != "" {
				return h
			}
		}
	case map[string]any:
		if graph, ok := node["@graph"]; ok {
			if h := organizationHost(graph); h != "" {
				return h
			}
		}
		if org, ok := node["hiringOrganization"]; ok {
			if h := organizationHost(org); h != "" {
				return h
			}
		}
		if isOrganizationType(node["@type"]) {
			if h := hostFromURLValue(node["url"]); h != "" {
				return h
			}
			if h := hostFromURLValue(node["sameAs"]); h != "" {
				return h
			}
		}
	}
	return ""
}

func isOrganizationType(t any) bool {
	switch tv := t.(type) {
	case string:
		return strings.Contains(strings.ToLower(tv), "organization")
	case []any:
		for _, item := range tv {
			if s, ok := item.(string); ok && strings.Contains(strings.ToLower(s), "organization") {
				return true
			}
		}
	}
	return false
}

// hostFromURLValue extracts the host from a JSON-LD url/sameAs value, which may
// be a string or an array of strings.
func hostFromURLValue(v any) string {
	switch val := v.(type) {
	case string:
		return hostOf(val)
	case []any:
		for _, item := range val {
			if s, ok := item.(string); ok {
				if h := hostOf(s); h != "" {
					return h
				}
			}
		}
	}
	return ""
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}
