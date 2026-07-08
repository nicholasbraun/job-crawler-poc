package discoveryprocessor

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

// careerKeywords mark a URL or title as career-related (a careers/jobs hub).
var careerKeywords = []string{
	"career", "careers", "jobs", "vacanc", "positions", "openings",
	"hiring", "join-us", "karriere", "stellen", "stellenangebote",
}

// Gate decides whether a fetched page is a Career Page — the index that lists a
// Company's open jobs — and how confident that decision is. accept reports
// whether to catalog the page; certain reports whether the decision is
// structurally definitive (an ATS board root), letting the career-page pool skip
// the LLM confirmation.
//
// On a recognized ATS host the decision is purely structural. On any other host
// the gate is a high-recall pre-filter that leaves precision to the LLM: it
// accepts a page that is career-signalled and is not itself a single posting, or
// that links to at least one job posting — regardless of how few openings it
// lists, so a small careers page is not dropped. A lone posting that lists no
// other jobs is rejected without an LLM call.
func Gate(u crawler.URL, content *crawler.Content) (accept, certain bool) {
	switch catalog.Classify(u) {
	case catalog.RoleCareerPage:
		return true, true
	case catalog.RoleJobListing:
		return false, false
	default:
		isPosting := isJobPostingPath(u.RawURL)
		careerish := containsAny(u.RawURL, careerKeywords) || containsAny(content.Title, careerKeywords)
		listsJobs := countJobPostingLinks(u, content) > 0
		return (careerish && !isPosting) || listsJobs, false
	}
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
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	segs := []string{}
	for _, s := range strings.Split(parsed.Path, "/") {
		if s != "" {
			segs = append(segs, s)
		}
	}

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
