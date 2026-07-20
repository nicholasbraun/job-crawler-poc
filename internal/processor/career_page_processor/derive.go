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

// nameLeadingWords are single leading words stripped from a title -- hiring
// boilerplate ("Join Acme" -> "Acme") and leading German articles
// ("der Commerzbank" -> "Commerzbank"). English "the" is deliberately excluded:
// it is commonly part of a real name (e.g. "The Guardian").
var nameLeadingWords = []string{"join", "careers", "career", "jobs", "der", "die", "das"}

// boilerplateWords are the words that, on their own, make a title part generic
// hiring boilerplate rather than a company name.
var boilerplateWords = map[string]bool{
	"careers": true, "career": true, "jobs": true, "job": true, "hiring": true,
	"openings": true, "opening": true, "positions": true, "position": true,
	"vacancies": true, "vacancy": true, "open": true, "join": true, "us": true,
	"team": true, "work": true, "current": true, "karriere": true,
	"stellen": true, "stellenangebote": true, "stellenangebot": true, "board": true,
	// German hiring boilerplate seen on pages without JSON-LD ("Offene Stellen",
	// "Stellenausschreibung", "Karriereseite und Stellenangebote").
	"offene": true, "stellenausschreibung": true, "stellenausschreibungen": true,
	"karriereseite": true, "karriereportal": true, "und": true, "and": true,
	// Generic nav / placeholder labels that are not company names.
	"landing": true, "page": true, "internships": true, "internship": true,
	"deals": true, "deal": true,
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

// deriveName runs the Name Ladder (ADR-0025), higher-trust first, returning the
// name and the rung that produced it. The meta and llm rungs are dormant until
// their inputs (Content.SiteName, the Verdict name) are populated by later work,
// but the logic is live: an empty input abstains to the next rung. The domain
// rung always yields a name, so the ladder never returns "".
func deriveName(content *crawler.Content, identity catalog.Identity, llmName string) (string, crawler.NameSource) {
	if name := organizationName(content.JSONLD); name != "" {
		return name, crawler.NameSourceJSONLD
	}
	if name := metaName(content, identity); name != "" {
		return name, crawler.NameSourceMeta
	}
	if name := strings.TrimSpace(llmName); name != "" {
		return name, crawler.NameSourceLLM
	}
	if name := titleName(content.Title); name != "" {
		return name, crawler.NameSourceTitle
	}
	return nameFallback(identity), crawler.NameSourceDomain
}

// metaName returns the site's og:site_name for a self-hosted Company, or "" when
// the Company is on an ATS board (there the metadata is the ATS's brand, not the
// employer -- ADR-0025) or the metadata is empty / pure hiring boilerplate.
func metaName(content *crawler.Content, identity catalog.Identity) string {
	if identity.ATSProvider != "" {
		return ""
	}
	name := strings.Join(strings.Fields(content.SiteName), " ")
	if name == "" || isBoilerplate(name) {
		return ""
	}
	return name
}

// titleName derives a human-readable company name from a career-page title by
// normalizing whitespace and stripping common board boilerplate. Unlike a bare
// domain or LLM read, a <title> earns trust only from a STRUCTURAL cue -- a
// connector ("Jobs at X"), a separator ("Careers - X"), a boilerplate suffix
// ("X Careers"), or a leading hiring/article word ("Join X"). Absent any cue the
// title is a bare label the ladder cannot tell from junk, so it abstains,
// returning "" for the domain rung to answer (ADR-0025). It also returns "" when
// the cue leaves nothing but hiring boilerplate.
func titleName(title string) string {
	// Collapse any run of whitespace (including embedded newlines) to a single
	// space and trim, so a title like "der IHK Berlin\n- IHK Berlin" is handled
	// as one line rather than leaking a literal newline into the stored name.
	name := strings.Join(strings.Fields(title), " ")

	cued := false

	// Leading boilerplate before a connector: "Jobs at <Company>",
	// "Current openings at <Company>". Take the text after the last connector.
	lower := strings.ToLower(name)
	for _, c := range nameConnectors {
		if idx := strings.LastIndex(lower, c); idx != -1 {
			name = strings.TrimSpace(name[idx+len(c):])
			cued = true
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
			cued = true
			break
		}
	}

	// Trailing boilerplate: "<Company> Careers", "<Company> Jobs".
	for _, s := range nameSuffixes {
		if strings.HasSuffix(strings.ToLower(name), s) {
			name = strings.TrimSpace(name[:len(name)-len(s)])
			cued = true
		}
	}

	// A leading word like "Join <Company>" (or a title that is only boilerplate).
	if fields := strings.Fields(name); len(fields) >= 1 {
		for _, w := range nameLeadingWords {
			if strings.EqualFold(fields[0], w) {
				name = strings.TrimSpace(name[len(fields[0]):])
				cued = true
				break
			}
		}
	}

	// A bare title with no structural cue is indistinguishable from junk, so it
	// abstains to the domain rung rather than being kept (ADR-0025).
	if !cued {
		return ""
	}
	// A cue fired but left nothing usable: an empty remainder, or a remainder
	// that is entirely hiring boilerplate ("Careers", "Offene Stellen").
	if name == "" || isBoilerplate(name) {
		return ""
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

// organizationName scans JSON-LD blocks for a company name, preferring a
// JobPosting's hiringOrganization (on a job board this is the employer, not the
// board) over a standalone Organization node. The two-pass search lets a
// hiringOrganization anywhere on the page win over an Organization node that may
// merely describe the hosting site. Returns "" when neither is present.
func organizationName(blocks []string) string {
	for _, block := range blocks {
		var v any
		if err := json.Unmarshal([]byte(block), &v); err != nil {
			continue
		}
		if name := hiringOrganizationName(v); name != "" {
			return name
		}
	}
	for _, block := range blocks {
		var v any
		if err := json.Unmarshal([]byte(block), &v); err != nil {
			continue
		}
		if name := standaloneOrganizationName(v); name != "" {
			return name
		}
	}
	return ""
}

// hiringOrganizationName walks a decoded JSON-LD value (including any @graph) and
// returns the name of the first JobPosting hiringOrganization it finds.
func hiringOrganizationName(v any) string {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			if name := hiringOrganizationName(item); name != "" {
				return name
			}
		}
	case map[string]any:
		if graph, ok := node["@graph"]; ok {
			if name := hiringOrganizationName(graph); name != "" {
				return name
			}
		}
		if org, ok := node["hiringOrganization"]; ok {
			if name := nameFromOrgValue(org); name != "" {
				return name
			}
		}
	}
	return ""
}

// standaloneOrganizationName walks a decoded JSON-LD value (including any @graph)
// and returns the name of the first top-level Organization node it finds.
func standaloneOrganizationName(v any) string {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			if name := standaloneOrganizationName(item); name != "" {
				return name
			}
		}
	case map[string]any:
		if graph, ok := node["@graph"]; ok {
			if name := standaloneOrganizationName(graph); name != "" {
				return name
			}
		}
		if isOrganizationType(node["@type"]) {
			if name := nameFromOrgValue(node); name != "" {
				return name
			}
		}
	}
	return ""
}

// nameFromOrgValue extracts the "name" from a JSON-LD organization value, which
// is normally an object carrying a "name", but may be a bare string (the name
// itself) or an array of either.
func nameFromOrgValue(v any) string {
	switch val := v.(type) {
	case string:
		return strings.TrimSpace(val)
	case map[string]any:
		if s, ok := val["name"].(string); ok {
			return strings.TrimSpace(s)
		}
	case []any:
		for _, item := range val {
			if name := nameFromOrgValue(item); name != "" {
				return name
			}
		}
	}
	return ""
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
