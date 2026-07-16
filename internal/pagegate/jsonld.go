// JSON-LD hub Structural Signal for the Gate's final-rung Confidence Score
// (ADR-0016). It reads the raw structured-data blocks the parser already
// extracted into Content.JSONLD; it never touches the network or the frontier.
package pagegate

import (
	"encoding/json"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// jsonLDHub reports whether content's JSON-LD structurally marks the page a
// Career Page hub -- a structured-data openings index. It fires on an ItemList
// that wraps at least one JobPosting, or on two or more JobPosting nodes anywhere
// in the page's JSON-LD. A single JobPosting marks one Job Listing, not a hub, so
// it fires nothing; absent or unparseable JSON-LD likewise fires nothing. The
// signal only ever adds credit -- a lone posting earns none -- so it cannot turn a
// single posting into a False-Certain, and an unreadable block costs missed LLM
// savings, never a Leak (the ADR-0016 fail-safe).
func jsonLDHub(content *crawler.Content) bool {
	postings := 0
	itemListOfJob := false
	for _, block := range content.JSONLD {
		var node any
		if err := json.Unmarshal([]byte(block), &node); err != nil {
			continue // an unparseable block contributes nothing (fail-safe)
		}
		p, il := scanJobPostings(node)
		postings += p
		itemListOfJob = itemListOfJob || il
	}
	return itemListOfJob || postings >= 2
}

// scanJobPostings walks a decoded JSON-LD value -- arrays, objects, and any
// @graph / itemListElement / item nesting -- reporting how many JobPosting nodes
// it contains and whether an ItemList among them wraps at least one JobPosting.
func scanJobPostings(v any) (jobPostings int, itemListOfJob bool) {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			p, il := scanJobPostings(item)
			jobPostings += p
			itemListOfJob = itemListOfJob || il
		}
	case map[string]any:
		self := 0
		if isLDType(node["@type"], "jobposting") {
			self = 1
		}
		// Recurse into every value so nested JobPostings (an ItemList's
		// itemListElement, a ListItem's item, an @graph) are all counted once.
		descendants := 0
		for _, val := range node {
			p, il := scanJobPostings(val)
			descendants += p
			itemListOfJob = itemListOfJob || il
		}
		jobPostings = self + descendants
		if isLDType(node["@type"], "itemlist") && descendants > 0 {
			itemListOfJob = true
		}
	}
	return jobPostings, itemListOfJob
}

// isLDType reports whether a JSON-LD @type value (a string, or an array of
// strings) names the given schema.org type, matched case-insensitively as a
// substring so a bare "JobPosting" and a "https://schema.org/JobPosting" both
// hit. Mirrors isOrganizationType in the career_page_processor derive logic.
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
