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

// terminalHubWords are the openings-index tokens that, as a deep career URL's
// FINAL path segment, keep it a Career Page hub rather than a single posting,
// exempting it from the posting-path veto (ADR-0010). The set is the career
// path signals plus openings-index synonyms. It is fixed by the Gold Set's
// zero-Leak requirement -- the naive "job-segment + segment => posting" veto
// leaked six real deep-path hubs -- and is bench-guarded (ADR-0008). It is a
// package-level curated list, deliberately config-independent, so the exported
// IsPostingPath predicate the Catalog Doctor reuses needs no gate config.
var terminalHubWords = []string{
	// Career path signals (a terminal career token marks a hub, e.g. /a/b/careers).
	"careers", "career", "jobs", "karriere", "stellenangebote", "vacancies",
	// Openings-index synonyms.
	"open-positions", "open-jobs", "opportunities", "openings", "positions",
	"job-board", "alle-jobs", "all-jobs", "jobsearch", "job-search",
	"offene-stellen",
}

// CareerPage decides whether a discovery candidate is a Career Page (accept)
// and whether that decision is structurally definitive (certain), letting the
// career-page pool skip the LLM classifier. A known aggregator/board host is
// rejected outright (never a single-company hub). On a recognized ATS host the
// decision is purely structural. On any other host: a strong-negative reject
// path rejects without the LLM; the deterministic posting-path veto (ADR-0010)
// then rejects a single posting or deep career sub-page -- a job-section segment
// followed by a further segment -- ahead of the link-count heuristic, so a
// posting that links sibling postings can no longer re-admit itself; a URL whose
// final path segment is a Terminal-Hub Word (a real deep hub such as
// /careers/open-positions) is exempted from the veto. A bare career-hub root path
// (the career signal is the last path segment) then accepts as certain; otherwise
// the final rung computes an additive Confidence Score (ADR-0016) over the
// remaining cheap signals — a career keyword in the URL/title, the distinct
// same-host Job Listing link count folded in as min(count/K, 1), and a JSON-LD hub
// (a structured-data openings index) — and maps it to a verdict via the config's
// CertainThreshold/RejectThreshold: at or above certain it accepts as certain
// (skips the LLM), at or below reject it rejects, and the band between is accepted
// but left to the LLM to confirm. With DefaultLLMGateConfig a career keyword plus a
// saturated same-host openings index certain-accepts from this rung, as does a
// JSON-LD hub on its own; reject still means no signal at all.
func CareerPage(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) (accept, certain bool) {
	// Multi-company aggregators, VC-portfolio boards, and professional networks
	// are never a single company's hub. Reject them before any accept path so
	// they never become a candidate -- keeping them out of the Catalog and, in
	// turn, out of Company identity (#46).
	if catalog.IsAggregatorHost(u) {
		return false, false
	}
	switch catalog.Classify(u) {
	case catalog.RoleCareerPage:
		return true, true
	case catalog.RoleJobListing:
		return false, false
	default:
		if pathHasSegment(u.RawURL, cfg.RejectPathSignals) {
			return false, false
		}
		// Deterministic posting-path veto (ADR-0010): a job-section segment
		// followed by a further segment is a single posting or deep career
		// sub-page. It runs ahead of the link-count heuristic below, so a posting
		// that links sibling postings can no longer re-admit itself. Terminal-Hub-
		// Word paths (e.g. /careers/open-positions) are exempted as real deep hubs.
		if IsPostingPath(u) {
			return false, false
		}
		if careerHubRoot(u.RawURL, cfg.CareerPathSignals) {
			return true, true
		}
		// Final rung (ADR-0016): an additive Confidence Score over the cheap
		// final-rung signals, mapped to the Gate's three verdicts by the two
		// thresholds. Under DefaultLLMGateConfig a career keyword plus a saturated
		// same-host openings index certain-accepts here (see confidenceScore).
		score := confidenceScore(u, content, cfg)
		switch {
		case score >= cfg.CertainThreshold:
			return true, true
		case score <= cfg.RejectThreshold:
			return false, false
		default:
			return true, false
		}
	}
}

// confidenceScore sums the weight of each fired final-rung signal into the
// Gate's additive Confidence Score (ADR-0016). Accumulation is pure: weak
// signals may sum toward certain, and the config's thresholds — not this
// function — decide the verdict band. Today three signals contribute: a career
// keyword in the URL or title; the distinct same-host Job Listing link count,
// folded in continuously as min(count/K, 1) (K = cfg.JobLinkSaturationCount);
// and a JSON-LD hub — a structured-data openings index (an ItemList of
// JobPosting or two or more JobPosting nodes), which a lone JobPosting does not
// trip. Signal tickets add stronger structural signals here.
func confidenceScore(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) float64 {
	var score float64
	if containsAny(u.RawURL, careerKeywords) || containsAny(content.Title, careerKeywords) {
		score += cfg.CareerKeywordWeight
	}
	// Distinct same-host Job Listing links fold in continuously (ADR-0016): a dense
	// openings index saturates the signal at full JobLinkWeight, while a page linking
	// a single stray posting earns only a fraction and stays uncertain. Cross-host
	// postings are intentionally not counted -- the ATS-embed signal covers those.
	score += cfg.JobLinkWeight * jobLinkSaturation(countJobPostingLinks(u, content), cfg.JobLinkSaturationCount)
	// JSON-LD hub (strong, ADR-0016): a structured-data openings index -- an
	// ItemList of JobPosting or two or more JobPosting nodes -- alone clears
	// certainθ. A lone JobPosting (one Job Listing, not a hub) and absent or
	// unparseable JSON-LD earn nothing, so this signal never certain-accepts a
	// single posting.
	if jsonLDHub(content) {
		score += cfg.JSONLDHubWeight
	}
	return score
}

// jobLinkSaturation maps a distinct same-host Job Listing link count to the
// saturating fraction min(count/k, 1) in [0,1]. A non-positive k or count
// contributes nothing -- the fail-safe (ADR-0016) that keeps an unset saturation
// count from dividing by zero or over-weighting the signal.
func jobLinkSaturation(count, k int) float64 {
	if k <= 0 || count <= 0 {
		return 0
	}
	if count >= k {
		return 1
	}
	return float64(count) / float64(k)
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

// IsPostingPath reports whether u's path is structurally a single Job Listing or
// a deep career sub-page that the Discovery Gate deterministically rejects
// (ADR-0010): a job-section segment ("careers", "jobs", …) followed by a further
// segment, EXCEPT when the URL's final path segment is a Terminal-Hub Word (a
// real deep-path hub such as /careers/open-positions). It reads only the URL, no
// page content, so the Catalog Doctor replays the identical veto over stored
// Career Page URLs from a single implementation.
func IsPostingPath(u crawler.URL) bool {
	return isJobPostingPath(u.RawURL) && !isTerminalHubWord(u.RawURL)
}

// isTerminalHubWord reports whether rawURL's final non-empty path segment is a
// Terminal-Hub Word, exempting the URL from the posting-path veto.
func isTerminalHubWord(rawURL string) bool {
	segs := pathSegmentsOf(rawURL)
	if len(segs) == 0 {
		return false
	}
	last := segs[len(segs)-1]
	for _, w := range terminalHubWords {
		if strings.EqualFold(last, w) {
			return true
		}
	}
	return false
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

// careerHubRoot reports whether rawURL's LAST non-empty path segment equals
// (case-insensitively) one of signals -- i.e. the URL is a bare career-section
// root ("/careers", "/about/careers", "/ministerium/karriere"), not a labeled
// sub-page beneath it ("/careers/how-we-hire", "/karriere/arbeiten-bei-uns").
// Only a hub root is structurally certain enough to catalog without the LLM; a
// deeper career sub-page is ambiguous -- often a culture, hiring-process, or
// career-development page that is not itself a jobs hub (#45) -- so the gate
// leaves it to the LLM. This is strictly narrower than pathHasSegment (the
// signal must be terminal, not merely present), so it can only shrink the
// certain-accept set, never grow it.
func careerHubRoot(rawURL string, signals []string) bool {
	if len(signals) == 0 {
		return false
	}
	segs := pathSegmentsOf(rawURL)
	if len(segs) == 0 {
		return false
	}
	last := segs[len(segs)-1]
	for _, want := range signals {
		if strings.EqualFold(last, want) {
			return true
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
