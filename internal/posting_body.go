package crawler

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

// DescriptionSource is where a Job Listing's stored description came from
// (ADR-0041 / CONTEXT "Description Source"): a closed set, so a typo can never
// create a silent third state. It sits alongside SourceLane and WorkArrangement
// as a domain enum.
type DescriptionSource string

const (
	// DescriptionSourceStructuredData is a body read from the page's lone
	// structured-data JobPosting description — the cleanest crawl-lane text.
	DescriptionSourceStructuredData DescriptionSource = "structured_data"
	// DescriptionSourcePageContent is a body taken from the page's main content,
	// used when the page publishes no unambiguous structured posting. It carries
	// page chrome where the page has no semantic container (~40% more text than a
	// structured body); accepted by ADR-0041.
	DescriptionSourcePageContent DescriptionSource = "page_content"
	// DescriptionSourceATSBoard is a body supplied by an ATS board API on an ATS
	// Fetch (ADR-0022). The ATS ingest processor stamps it; the crawl-lane heal
	// must never rewrite a listing carrying it.
	DescriptionSourceATSBoard DescriptionSource = "ats_board"
	// DescriptionSourceLLMSummary marks a LEGACY, model-authored summary from
	// before ADR-0041. New code never writes it; it exists so the refetch lane can
	// find the rows it must heal from the page it already fetched.
	DescriptionSourceLLMSummary DescriptionSource = "llm_summary"
)

// DefaultDescriptionMaxChars caps a stored Posting Body at 16000 runes. It is a
// ROW-SIZE guard, not a quality knob: real postings run to ~12k characters at p99,
// so it only ever fires on a pathological parse (59k characters observed live,
// where a <body> fallback swallowed a whole site).
//
// It is deliberately NOT the extractor's prompt window (LLM_EXTRACT_MAX_CHARS):
// that dial tunes prompt latency for local models and must never decide what the
// Corpus stores (ADR-0041). The two are bounded by different knobs on purpose.
const DefaultDescriptionMaxChars = 16000

// PostingBody derives a Job Listing's Posting Body from a parsed page and reports
// the Description Source that produced it (ADR-0041). Precedence: the description
// of the page's LONE structured-data posting — exactly one JobPosting node and no
// openings index, the exact complement of the Extract Gate's openings-index reject
// — reduced to plain text; otherwise the page's main content. Both are capped to
// maxChars runes.
//
// A page whose lone posting carries no usable description falls through to main
// content rather than storing an empty body. A non-positive maxChars falls back to
// DefaultDescriptionMaxChars, so a forgotten knob can never blank the Corpus. A nil
// page yields an empty body attributed to page content.
//
// Pure: no model, no network, no database.
func PostingBody(content *Content, maxChars int) (body string, source DescriptionSource) {
	if maxChars <= 0 {
		maxChars = DefaultDescriptionMaxChars
	}
	if content == nil {
		return "", DescriptionSourcePageContent
	}
	if structured := htmlToText(lonePostingDescription(content.JSONLD)); structured != "" {
		return capChars(structured, maxChars), DescriptionSourceStructuredData
	}
	return capChars(content.MainContent, maxChars), DescriptionSourcePageContent
}

// lonePostingDescription returns the raw description of the page's single
// structured-data JobPosting, or "" when the structured data is ambiguous or
// silent: two or more posting nodes, an ItemList wrapping a posting (an openings
// index), no posting at all, or a posting with no string description. An
// unparseable block contributes nothing and never fails the read (the same
// fail-safe pagegate.jsonLDHub applies).
//
// This walk duplicates pagegate's scanJobPostings on purpose: pagegate imports this
// package, so a shared predicate here would have to be built before the spec that
// needs it (ADR-0042 / #253) has landed. #253 collapses the two.
func lonePostingDescription(blocks []string) string {
	postings, openingsIndex, description := 0, false, ""
	for _, block := range blocks {
		var node any
		if err := json.Unmarshal([]byte(block), &node); err != nil {
			continue
		}
		p, il, d := scanJobPostings(node)
		postings += p
		openingsIndex = openingsIndex || il
		if description == "" {
			description = d
		}
	}
	if postings != 1 || openingsIndex {
		return ""
	}
	return description
}

// scanJobPostings walks a decoded JSON-LD value — arrays, objects, and any @graph /
// itemListElement / item nesting — reporting how many JobPosting nodes it contains,
// whether an ItemList among them wraps at least one JobPosting, and the description
// of the first JobPosting carrying one (meaningful only when exactly one node was
// found).
func scanJobPostings(v any) (jobPostings int, itemListOfJob bool, description string) {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			p, il, d := scanJobPostings(item)
			jobPostings += p
			itemListOfJob = itemListOfJob || il
			if description == "" {
				description = d
			}
		}
	case map[string]any:
		self := 0
		if isLDType(node["@type"], "jobposting") {
			self = 1
			if s, ok := node["description"].(string); ok {
				description = s
			}
		}
		// Recurse into every value so nested JobPostings (an ItemList's
		// itemListElement, a ListItem's item, an @graph) are all counted once.
		descendants := 0
		for _, val := range node {
			p, il, d := scanJobPostings(val)
			descendants += p
			itemListOfJob = itemListOfJob || il
			if description == "" {
				description = d
			}
		}
		jobPostings = self + descendants
		if isLDType(node["@type"], "itemlist") && descendants > 0 {
			itemListOfJob = true
		}
	}
	return jobPostings, itemListOfJob, description
}

// isLDType reports whether a JSON-LD @type value (a string, or an array of strings)
// names the given schema.org type, matched case-insensitively as a substring so a
// bare "JobPosting" and a "https://schema.org/JobPosting" both hit.
func isLDType(t any, want string) bool {
	switch tv := t.(type) {
	case string:
		return strings.Contains(strings.ToLower(tv), want)
	case []any:
		for _, item := range tv {
			if s, ok := item.(string); ok && strings.Contains(strings.ToLower(s), want) {
				return true
			}
		}
	}
	return false
}

var htmlTagRegex = regexp.MustCompile("<[^>]*>")

// htmlToText reduces a structured-data description — plain text or single-encoded
// HTML — to flat text. Tags are stripped to SPACES before the single unescape:
// unescaping first would turn an entity-encoded angle bracket in the text ("team of
// &lt;10 engineers") into a literal "<" the tag regex would then swallow along with
// the run of text up to the next real tag; stripping to "" instead of " " would glue
// words across block tags. Whitespace — including the NBSP that &nbsp; unescapes to,
// which Fields treats as space — is then collapsed to single spaces and trimmed,
// matching how the parser already normalizes MainContent. Mirrors
// ats.htmlSingleEncodedToText, which cannot be reused: internal/ats imports this
// package.
func htmlToText(s string) string {
	stripped := htmlTagRegex.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(html.UnescapeString(stripped)), " ")
}

// capChars truncates s to at most maxChars RUNES (never bytes: a byte cut could
// split a multi-byte character). Shared with SourceHash in source_hash.go, whose
// value must stay byte-identical to the extractor's historical stamp.
func capChars(s string, maxChars int) string {
	r := []rune(s)
	if len(r) <= maxChars {
		return s
	}
	return string(r[:maxChars])
}
