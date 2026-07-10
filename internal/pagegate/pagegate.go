// Package pagegate holds the pure, pre-LLM gate logic (ADR-0007 step 2): cheap
// URL-path and page-structure checks that resolve a page's classifier or
// extractor verdict without a model call. CareerPage serves the discovery path
// (accept a Career Page hub, and report whether that decision is certain enough
// to skip the LLM classifier); ShouldExtract serves the keyword path (skip the
// LLM extractor for pages a URL signal already resolves). The same career-hub
// path signal reads oppositely on the two paths: on discovery it accepts a hub
// as a Career Page, on extract it marks a hub an index to crawl rather than
// extract.
package pagegate

import (
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// jobPathSegments are path tokens that, when followed by a further segment,
// mark a URL as a single job posting (e.g. "/careers/senior-go", "/jobs/123").
var jobPathSegments = []string{
	"jobs", "job", "careers", "career", "vacancies", "vacancy",
	"positions", "position", "openings", "opening", "stellenangebote",
	"stellen", "stelle",
}

// careerKeywords mark a URL or title as career-related (a careers/jobs hub). This
// is the high-recall content heuristic: a match accepts a page but leaves the
// verdict to the LLM (uncertain), so it can hold weaker, ambiguous tokens ("join")
// that DefaultLLMGateConfig deliberately keeps out of its certain-accept signals.
var careerKeywords = []string{
	"career", "careers", "jobs", "vacanc", "positions", "openings",
	"hiring", "join", "karriere", "stellen", "stellenangebote",
}

// CareerPage decides whether a discovery candidate is a Career Page (accept)
// and whether that decision is structurally definitive (certain), letting the
// career-page pool skip the LLM classifier. On a recognized ATS host the
// decision is purely structural. On any other host a strong-negative reject
// path rejects without the LLM; a bare career-hub path accepts as certain; an
// otherwise career-signalled non-posting (or a page that links to postings) is
// accepted but left to the LLM to confirm.
func CareerPage(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) (accept, certain bool) {
	switch catalog.Classify(u) {
	case catalog.RoleCareerPage:
		return true, true
	case catalog.RoleJobListing:
		return false, false
	default:
		if pathHasSegment(u.RawURL, cfg.RejectPathSignals) {
			return false, false
		}
		isPosting := isJobPostingPath(u.RawURL)
		if !isPosting && pathHasSegment(u.RawURL, cfg.CareerPathSignals) {
			return true, true
		}
		careerish := containsAny(u.RawURL, careerKeywords) || containsAny(content.Title, careerKeywords)
		listsJobs := countJobPostingLinks(u, content) > 0
		return (careerish && !isPosting) || listsJobs, false
	}
}

// ShouldExtract reports whether a keyword-relevant page should reach the LLM
// extractor. It is false when a cheap URL signal already resolves it: an ATS
// board root or a career-hub index (crawled for its individual postings, not
// itself a single posting) and strong-negative reject paths.
func ShouldExtract(u crawler.URL, cfg crawler.LLMGateConfig) bool {
	if catalog.Classify(u) == catalog.RoleCareerPage {
		return false
	}
	if pathHasSegment(u.RawURL, cfg.RejectPathSignals) {
		return false
	}
	if !isJobPostingPath(u.RawURL) && pathHasSegment(u.RawURL, cfg.CareerPathSignals) {
		return false
	}
	return true
}

// containsAny reports whether s contains any of keywords, case-insensitively.
func containsAny(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// countJobPostingLinks counts the distinct same-host job-posting URLs linked
// from content. Restricting to base's host avoids counting outbound links to
// unrelated job boards.
func countJobPostingLinks(base crawler.URL, content *crawler.Content) int {
	seen := map[string]struct{}{}
	for _, href := range content.URLs {
		link, err := base.Parse(href)
		if err != nil {
			continue
		}
		if link.Hostname == base.Hostname && isJobPostingPath(link.RawURL) {
			seen[link.RawURL] = struct{}{}
		}
	}
	return len(seen)
}

// isJobPostingPath reports whether rawURL's path points at a single posting:
// a job-section segment ("careers", "jobs", …) followed by a posting identifier.
func isJobPostingPath(rawURL string) bool {
	segs := pathSegmentsOf(rawURL)
	for i, s := range segs {
		for _, kw := range jobPathSegments {
			// The job segment must be followed by a further segment (the posting
			// slug or id); a bare "/careers" is the index, not a posting.
			if strings.EqualFold(s, kw) && i+1 < len(segs) {
				return true
			}
		}
	}
	return false
}

// pathHasSegment reports whether any of rawURL's path segments equals (case-
// insensitively) any of segments. It is an exact per-segment match, not a
// substring test, so "press" does not match "impressum". Empty segments (a nil
// or empty signal list) yields false.
func pathHasSegment(rawURL string, segments []string) bool {
	if len(segments) == 0 {
		return false
	}
	for _, seg := range pathSegmentsOf(rawURL) {
		for _, want := range segments {
			if strings.EqualFold(seg, want) {
				return true
			}
		}
	}
	return false
}

// pathSegmentsOf returns the non-empty path segments of rawURL, or nil when it
// cannot be parsed.
func pathSegmentsOf(rawURL string) []string {
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
