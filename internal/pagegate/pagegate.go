// Package pagegate holds the pure, pre-LLM gate logic (ADR-0007 step 2): cheap
// URL-path and page-structure checks that resolve a page's classifier or
// extractor verdict without a model call. CareerPage serves the discovery path
// (accept a Career Page hub, and report whether that decision is certain enough
// to skip the LLM classifier); ShouldExtract serves the keyword path (skip the
// LLM extractor for a page that is not a single posting), reading both the URL
// and the parsed page structure (ADR-0019). The same career-hub path signal and
// the same Structural Signals read oppositely on the two paths: on discovery they
// accept a hub as a Career Page, on extract they REJECT a hub as an index to
// crawl rather than extract — with the ATS posting deterministically exempt. On
// its final rung ShouldExtract additionally requires Positive Evidence that the
// page IS one posting (ADR-0044, positive_evidence.go), so clearing every reject
// rung is no longer enough to spend an extractor call.
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

// strongTitleRoots are the careers-hub word stems that, as the LEADING token of a
// page Title, mark it a careers hub for the title-strength signal (ADR-0016). They
// are prefix-matched against that leading token, so one stem covers a family
// ("career"->"careers", "vacan"->"vacancy"/"vacancies", "position"->"positions").
// This is a STRONGER lexical signal than careerKeywords: it fires only when a hub
// word LEADS the title, not merely appears somewhere in the URL or title.
// Deliberate exclusions: "join" and "hiring" stay only in careerKeywords — a title
// leading "Join our newsletter" or "Hiring freeze" is not a careers hub; and
// "intern"/"praktikum" are left out because they would fire on an internships
// editorial page (the National Geographic internships negative).
var strongTitleRoots = []string{
	"job", "career", "karriere", "stellen", "vacan", "position", "opening", "recruit",
}

// CareerPage decides whether a discovery candidate is a Career Page (accept)
// and whether that decision is structurally definitive (certain), letting the
// career-page pool skip the LLM classifier. A known aggregator/board host is
// rejected outright, and is the only CERTAIN reject (never a single-company hub;
// the verdict is a curated host list, not a calibration). On a recognized ATS host the
// decision is purely structural. On any other host: a strong-negative reject
// path rejects without the LLM; the deterministic posting-path veto (ADR-0010)
// then rejects a single posting or deep career sub-page -- a job-section segment
// followed by a further segment -- ahead of the link-count heuristic, so a
// posting that links sibling postings can no longer re-admit itself; a URL whose
// final path segment is a Terminal-Hub Word (a real deep hub such as
// /careers/open-positions) is exempted from the veto. A bare career-hub root path
// (the career signal is the last path segment) then accepts as certain; otherwise
// the final rung computes an additive Confidence Score (ADR-0016) over the
// remaining cheap signals — an ATS Embed (an iframe or provider-script board on
// the Company's own page), a career keyword in the URL/title, title strength (the
// Title leads with a careers-hub word, or the URL carries a career token as an
// exact path segment), the distinct same-host Job Listing link count folded in as
// min(count/K, 1), and a JSON-LD hub (a structured-data openings index) — and maps
// it to a verdict via the config's CertainThreshold/RejectThreshold: at or above
// certain it accepts as certain (skips the LLM), at or below reject it rejects, and
// the band between is accepted but left to the LLM to confirm. With
// DefaultLLMGateConfig a career keyword plus a saturated same-host openings index
// certain-accepts from this rung, as does a JSON-LD hub or an ATS Embed on its own;
// a page whose only signal is the weakest career keyword now rejects, and lexical
// evidence alone (career keyword + title strength) stays uncertain — certain still
// requires a Structural Signal.
func CareerPage(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) (accept, certain bool) {
	// Multi-company aggregators, VC-portfolio boards, and professional networks
	// are never a single company's hub. Reject them before any accept path so
	// they never become a candidate -- keeping them out of the Catalog and, in
	// turn, out of Company identity (#46).
	//
	// This is the one CERTAIN reject the Gate can make: the verdict comes from a
	// curated host list, so no page content can make it ambiguous -- unlike the
	// structural and score rejects below, which are calibrations. Reporting it as
	// certain is what lets the Collection Crawl's dormancy probe act on it without
	// an LLM confirm (stillCareerPage trusts only a certain verdict), so a host
	// added to the list retroactively closes the Catalog rows and Job Listings it
	// already minted instead of leaving them for the Catalog Doctor. Discovery and
	// the bench read accept=false as a reject regardless of certain, so this is
	// inert on those paths.
	if catalog.IsAggregatorHost(u) {
		return false, true
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
// function — decide the verdict band. Today five signals contribute: an ATS
// embed (the strongest — a Company page rendering a third-party ATS board inline,
// via an iframe to a known ATS host or a script to one with the provider's
// board-container marker present); a career keyword in the URL or title (the
// weakest); title strength, a stronger lexical signal that fires when the page
// reads as a careers hub — its Title leads with a careers-hub word, or the URL
// carries a career token as a distinct exact path segment; the distinct same-host
// Job Listing link count, folded in continuously as min(count/K, 1) (K =
// cfg.JobLinkSaturationCount); and a JSON-LD hub — a structured-data openings index
// (an ItemList of JobPosting or two or more JobPosting nodes), which a lone
// JobPosting does not trip. Signal tickets add stronger structural signals here.
func confidenceScore(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) float64 {
	var score float64
	// ATS embed (strongest, ADR-0016): a Company page rendering a third-party ATS
	// board inline. An iframe to a known ATS host, or a script to one with the
	// provider's board-container marker present, clears certainθ on its own; a
	// site-wide script with no marker, an unknown host, or a wrong marker fires
	// nothing (fail-safe), so it never certain-accepts a non-hub.
	if atsEmbed(content) {
		score += cfg.ATSEmbedWeight
	}
	if containsAny(u.RawURL, careerKeywords) || containsAny(content.Title, careerKeywords) {
		score += cfg.CareerKeywordWeight
	}
	// Title strength (ADR-0016): the page reads as a careers hub — its Title leads
	// with a careers-hub word, or the URL carries a career token as a distinct exact
	// path segment. Weighted above the weakest keyword substring so a page that reads
	// as a careers hub outweighs one that merely mentions a career keyword. This is
	// the term that lifts a real career sub-page (kfw's /karriere/studierende) out of
	// the reject band, while a career keyword buried in a compound slug
	// (/karriere-bei-bitsea) — matched only as a substring, never an exact segment —
	// is not lifted, so it still auto-rejects.
	if strongCareerSignal(u, content.Title, cfg.CareerPathSignals) {
		score += cfg.TitleStrengthWeight
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
	// single posting. Read via the domain's shared structured-posting read.
	if crawler.HasOpeningsIndex(content) {
		score += cfg.JSONLDHubWeight
	}
	return score
}

// strongCareerSignal reports the title-strength / strong-careers signal
// (ADR-0016): the page's Title leads with a careers-hub word, OR the URL carries a
// career token as a distinct exact path segment. The segment test reuses
// careerSegments (cfg.CareerPathSignals) so the career-token set keeps ONE source
// of truth (and the isolated finalRungConfig tests, which clear CareerPathSignals,
// stay coherent). In the final rung the segment component fires only on a
// NON-terminal career segment: a terminal career segment (e.g. /karriere) already
// certain-accepts at careerHubRoot before this rung, so here it distinguishes a
// real career sub-page (kfw's /karriere/studierende) — an exact segment — from a
// career keyword inside a compound slug (/karriere-bei-bitsea), which is not a
// segment and so never trips this signal.
func strongCareerSignal(u crawler.URL, title string, careerSegments []string) bool {
	return titleLeadsWithCareerWord(title) || pathHasSegment(u.RawURL, careerSegments)
}

// titleLeadsWithCareerWord reports whether title's LEADING run of ASCII letters is
// (prefix-matched) a strongTitleRoots word — i.e. the title reads as a careers hub
// from its first word ("Careers", "Jobs & Karriere"), not merely mentions one
// later. The leading token is the letters before the first non-letter; a title
// that opens with a non-ASCII letter (e.g. "Über uns") yields an empty token and
// false, which is acceptable — such titles are not careers hubs.
func titleLeadsWithCareerWord(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	end := 0
	for end < len(lower) && lower[end] >= 'a' && lower[end] <= 'z' {
		end++
	}
	lead := lower[:end]
	if lead == "" {
		return false
	}
	for _, root := range strongTitleRoots {
		if strings.HasPrefix(lead, root) {
			return true
		}
	}
	return false
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
// extractor (ADR-0019). It rejects (returns false) the page shapes that are not a
// single posting, reading two verdict families in sequence, first match wins. The
// URL rungs come first: an ATS board root (RoleCareerPage) is an index to crawl;
// an ATS posting (RoleJobListing) is deterministically a posting and is EXEMPT —
// its "more openings" sidebar can never drop it, so this exemption precedes every
// content rung; a strong-negative reject path, or a bare career-section index
// (a career segment on a non-posting path), rejects. These read the same
// career-hub path signal that CareerPage accepts, here with reject polarity. The
// content reject rungs then read the parsed page structure — the SAME Structural
// Signals CareerPage's Confidence Score sums, but with reject polarity: an ATS
// Embed, a JSON-LD openings index (an ItemList of JobPosting or >=2 JobPosting
// nodes), or a page saturated with distinct same-host Job Listing links marks a
// jobs index, not a posting, and rejects. A self-hosted posting is NOT exempt — it
// reaches the content rungs and relies on calibration (its lone JobPosting and
// sparse sidebar trip none of them), so a /jobs/all-style hub carrying structured
// openings data still rejects. A page that clears every reject rung then reaches
// the extractor only on Positive Evidence that it IS one posting (ADR-0044): a
// posting-shaped URL or a lone structured-data posting admits it alone, while the
// two text marks — an apply affordance and dense posting vocabulary — admit it only
// in agreement with each other. That rung fires only when
// cfg.RequirePositiveEvidence is set; unset, the gate keeps its previous blanket
// accept of everything nothing rejected. A page it admits is then withheld anyway
// when cfg.LearnedVeto is set and the page's Posting Score falls below the
// compiled-in VetoThreshold (ADR-0049) — the only rung here that removes a call the
// rest of the ladder was about to pay for, and the only one that is fitted rather
// than argued.
//
// ExtractDecision is the same decision with the rejecting rung reported, and
// ExtractGate is the fullest reading of it; this is the verdict-only reading for the
// callers that do not attribute.
func ShouldExtract(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) bool {
	return ExtractGate(u, content, cfg).Extract
}

// ExtractRung names the ShouldExtract rung that REJECTED a page. It is the
// attribution the Shadow Extraction lane records alongside each verdict (ADR-0044):
// a false-drop EXTRACT_REQUIRE_POSITIVE_EVIDENCE would undo (RungPositiveEvidence),
// one EXTRACT_LEARNED_VETO would undo (RungLearnedVeto), and one neither switch
// recovers (any reject rung) are different failures with different remedies, and a
// shadow accept rate that cannot tell them apart can read high for a cause the named
// revert does not fix.
//
// The values are metric label values, so they are stable strings: renaming one
// breaks a dashboard query and a historical series, not a compile.
type ExtractRung string

const (
	// RungNone is the absence of a reject -- the page cleared every rung and reaches
	// the extractor. It is the empty string so a zero value reads as "not rejected".
	RungNone ExtractRung = ""
	// The URL rungs, in the order ShouldExtract reads them.
	RungATSBoardRoot      ExtractRung = "ats_board_root"      // rung 1
	RungBareOrLocaleRoot  ExtractRung = "bare_or_locale_root" // rung 2b
	RungIndexTerminal     ExtractRung = "index_terminal"      // rung 2c
	RungRejectPath        ExtractRung = "reject_path"         // rung 3
	RungCareerIndex       ExtractRung = "career_index"        // rung 4
	RungATSEmbed          ExtractRung = "ats_embed"           // rung 5
	RungOpeningsIndex     ExtractRung = "openings_index"      // rung 6
	RungJobLinkSaturation ExtractRung = "job_link_saturation" // rung 7
	RungPositiveEvidence  ExtractRung = "positive_evidence"   // rung 8
	RungLearnedVeto       ExtractRung = "learned_veto"        // rung 9
	// RungUnknown labels a Shadow Extraction whose sample carries no rung -- an entry
	// enqueued by an older binary and redelivered after an upgrade. It is never
	// produced by this function; it exists so such a sample is visibly unattributed
	// rather than silently filed under a real rung.
	RungUnknown ExtractRung = "unknown"
)

// RejectRungs are every rung a Shadow Extraction can be attributed to: the rungs
// that reject, plus RungUnknown. RungNone is absent -- a page that cleared every
// rung is extracted for real and never sampled.
//
// It exists so the telemetry can create each series at zero BEFORE the first
// sample lands. A counter that springs into existence on its first increment is
// invisible to increase() over any window, because increase() measures growth from
// the first scraped sample -- so the FIRST false-drop on a given rung, which is
// exactly the event the live measurement exists to catch, reads as no change at
// all. Priming the series costs a handful of permanently-zero rows and makes "this
// rung has dropped nothing" a visible statement rather than a missing one.
func RejectRungs() []ExtractRung {
	return []ExtractRung{
		RungATSBoardRoot, RungBareOrLocaleRoot, RungIndexTerminal, RungRejectPath,
		RungCareerIndex, RungATSEmbed, RungOpeningsIndex, RungJobLinkSaturation,
		RungPositiveEvidence, RungLearnedVeto, RungUnknown,
	}
}

// ExtractVerdict is one Extract Gate decision reported in full: the verdict, the
// rung that rejected the page, and the Posting Score the Learned Veto judged it by
// (ADR-0049). ExtractDecision and ShouldExtract are narrowings of it for the callers
// that need less; all three run the ONE implementation, so no reading can describe a
// different sequence than the one that ran.
type ExtractVerdict struct {
	// Extract is the verdict: true when the page reaches the LLM extractor.
	Extract bool
	// Rung is the rung that rejected the page, or RungNone on an accept.
	Rung ExtractRung
	// Score is the page's Posting Score, and it is MEANINGLESS unless Scored is set.
	// A page any earlier rung rejected, an ATS posting exempt at rung 2, and every
	// page at all on a crawl with the Learned Veto off leave it at 0 -- but 0 is also
	// a legitimate score, so the two are indistinguishable without Scored. A reader
	// that folded those zeros into the live score distribution would report a stream
	// that scores lowest of all.
	Score float64
	// Scored reports that the Learned Veto rung actually RAN on this page: the switch
	// was on and every rung above it admitted the page. It is exactly the population
	// the score distribution is recorded over -- the rung's accepts AND its vetoes,
	// which is what makes that distribution show what the cut is cutting into and not
	// merely what it removed.
	Scored bool
}

// ExtractDecision is ShouldExtract with the REJECTING RUNG reported alongside the
// verdict. It is a narrowing of ExtractGate for the callers that attribute a reject
// but do not read the Posting Score. On an accept the rung is RungNone.
func ExtractDecision(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) (bool, ExtractRung) {
	v := ExtractGate(u, content, cfg)
	return v.Extract, v.Rung
}

// ExtractGate is ShouldExtract's decision reported in full (ADR-0049): the verdict,
// the rejecting rung, and — for the pages the Learned Veto actually judged — the
// Posting Score it judged them by. It is the ONE implementation the two narrower
// readings run, so the attribution and the score can never describe a different
// sequence than the one that ran.
func ExtractGate(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) ExtractVerdict {
	switch catalog.Classify(u) {
	case catalog.RoleCareerPage:
		// rung 1: an ATS board root is an index to crawl, not a posting.
		return ExtractVerdict{Rung: RungATSBoardRoot}
	case catalog.RoleJobListing:
		// rung 2: an ATS posting is deterministically a posting, so its "more
		// openings" sidebar cannot drop it. This exemption precedes every content
		// reject rung below. It is deliberately UNSCORED -- leaving Scored false is
		// what keeps the exemption deterministic, since a page that is never judged
		// cannot enter the Learned Veto's score distribution and skew it toward a
		// population no fit ever saw (ADR-0049).
		return ExtractVerdict{Extract: true, Rung: RungNone}
	}
	// rung 2b: a bare domain root (or a locale-only root like /en, /de-de) is never a
	// single job posting -- a posting always carries a slug/id beyond the host. This
	// sheds careers-landing and homepage pages the extractor would otherwise
	// hallucinate a listing from, keyed to a root URL that collides in the Corpus.
	if isBareOrLocaleRoot(u.RawURL) {
		return ExtractVerdict{Rung: RungBareOrLocaleRoot}
	}
	// rung 2c: a URL whose terminal path segment is a jobs index/section word
	// (careers, jobs, openings, job-offers, search-jobs, ..., any web extension
	// stripped) is a hub, not a posting -- even a posting-path URL like
	// /careers/openings, which rung 4 misses because its parent is a job segment.
	if isExtractIndexTerminal(u.RawURL) {
		return ExtractVerdict{Rung: RungIndexTerminal}
	}
	if pathHasSegment(u.RawURL, cfg.RejectPathSignals) {
		return ExtractVerdict{Rung: RungRejectPath} // rung 3: a strong-negative reject path.
	}
	if !isJobPostingPath(u.RawURL) && pathHasSegment(u.RawURL, cfg.CareerPathSignals) {
		return ExtractVerdict{Rung: RungCareerIndex} // rung 4: a bare career-section index, not a posting.
	}
	// Content reject rungs (ADR-0019). Self-hosted postings on a job-posting path
	// reach here (only ATS RoleJobListing is exempt), so a /jobs/all-style hub
	// carrying structured openings data still rejects.
	if atsEmbed(content) {
		return ExtractVerdict{Rung: RungATSEmbed} // rung 5: a page embedding a whole ATS board is a hub.
	}
	if crawler.HasOpeningsIndex(content) {
		// rung 6: a JSON-LD ItemList / >=2 JobPosting nodes is an openings index, read
		// via the domain's shared structured-posting read; a lone JobPosting does not
		// fire it.
		return ExtractVerdict{Rung: RungOpeningsIndex}
	}
	if jobLinkSaturation(countJobPostingLinks(u, content), cfg.ExtractJobLinkSaturationCount) >= 1 {
		// rung 7: a page saturated with distinct same-host job links is a jobs index.
		// Reuses both pure detectors with reject polarity; an
		// ExtractJobLinkSaturationCount <= 0 makes jobLinkSaturation return 0, so this
		// rung then never fires.
		return ExtractVerdict{Rung: RungJobLinkSaturation}
	}
	// rung 8 (ADR-0044): Positive Evidence. Every reject rung above has resolved
	// first, so this rung only ever decides pages nothing rejected -- a hub carrying
	// an apply affordance is already gone. When the rung is off the gate keeps its
	// previous blanket accept, which is the kill switch.
	if cfg.RequirePositiveEvidence && !hasPositiveEvidence(u, content) {
		return ExtractVerdict{Rung: RungPositiveEvidence}
	}
	// rung 9 (ADR-0049): the Learned Veto. It sees only the pages rung 8 admitted --
	// where 100% of the extract bill on the measured population is spent -- and it
	// only ever REMOVES a call, so every mark ADR-0044 argues still admits exactly
	// what it admitted. It is the one rung in this ladder that is FITTED rather than
	// argued, which is why it carries its own switch on top of rung 8's.
	//
	// The switch is read BEFORE the score and returns early, so a crawl with the veto
	// off does no signal extraction and no Score Vocabulary scan at all -- the rung
	// costs nothing on a crawl not using it, which is the condition it ships under,
	// and it leaves Scored false so nothing downstream records an un-judged page's
	// zero as a Posting Score. The comparison is "<", matching the trainer's "score
	// >= threshold survives" exactly; a page AT the threshold is kept, and that
	// boundary is what makes the shipped cut the one that was measured.
	if !cfg.LearnedVeto {
		return ExtractVerdict{Extract: true, Rung: RungNone}
	}
	score := Score(u, content)
	if score < VetoThreshold {
		return ExtractVerdict{Rung: RungLearnedVeto, Score: score, Scored: true}
	}
	return ExtractVerdict{Extract: true, Rung: RungNone, Score: score, Scored: true}
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

// IsHubOrRootURL reports whether u is structurally a jobs hub or a bare/locale root
// -- never a single Job Listing -- by the extract gate's URL-only reject rungs (a
// bare/locale-only root, or a terminal jobs-index segment). It reads only the URL,
// no page content, so the crawl-lane refetch can replay it over a stored listing to
// self-heal the Corpus when the gate tightens -- the same way the Catalog Doctor
// replays IsPostingPath. ShouldExtract applies these two rungs to a freshly-walked
// page; this exports their union for the retroactive check.
func IsHubOrRootURL(u crawler.URL) bool {
	// Mirror ShouldExtract's rung ordering: a catalog-classified ATS posting is a
	// single posting regardless of its URL shape, so it is exempt from the URL rungs
	// (never re-gated). This keeps the invariant that the re-gate closes only what the
	// live gate would reject.
	if catalog.Classify(u) == catalog.RoleJobListing {
		return false
	}
	return isBareOrLocaleRoot(u.RawURL) || isExtractIndexTerminal(u.RawURL)
}

// IsPostingPath reports whether u's path is structurally a single Job Listing or
// a deep career sub-page that the Discovery Gate deterministically rejects
// (ADR-0010): a job-section segment ("careers", "jobs", …) followed by a further
// segment, EXCEPT when the URL's final path segment is a Terminal-Hub Word (a
// real deep-path hub such as /careers/open-positions). It reads only the URL, no
// page content, so the Catalog Doctor replays the identical veto over stored
// Career Page URLs from a single implementation.
//
// The Extract Gate's Positive Evidence rung reads a SEPARATE, wider posting-URL
// predicate of its own (positive_evidence.go); the two are deliberately not merged,
// because widening this one would retroactively delete Catalog rows and shift
// job-link saturation counting.
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

// extractIndexTerminals are terminal path segments that mark a jobs index/section
// page (never a single posting) for the extract gate, on TOP of terminalHubWords.
// Kept a separate list so this reject is scoped to ShouldExtract and does not shift
// the classifier's IsPostingPath veto (which reads terminalHubWords).
var extractIndexTerminals = []string{
	"search-jobs", "job-offers", "job_offers", "job-openings", "work-with-us",
	"stellengesuche", "stellenausschreibungen", "stellenanzeigen",
}

// localeRootSegments are language/locale tokens that, as a URL's ONLY path segment,
// still leave it a site/section root, not a single posting (e.g. "acme.com/en",
// "firma.de/de-de"). A curated set (not any two letters) so a real one-segment
// posting slug or a department code like "/hr" is never mistaken for a locale.
// The locale set itself lives on the domain root as crawler.IsLocaleSegment, so
// this rung and catalog.Classify read one definition (#270 follow-up).

// isBareOrLocaleRoot reports whether rawURL points at a bare domain root (no path
// segments) or a locale-only root (its single segment is a language/locale token).
// Neither can be a single Job Listing, whose URL always carries a posting slug/id.
func isBareOrLocaleRoot(rawURL string) bool {
	segs := pathSegmentsOf(rawURL)
	if len(segs) == 0 {
		return true
	}
	return len(segs) == 1 && crawler.IsLocaleSegment(segs[0])
}

// isExtractIndexTerminal reports whether rawURL's terminal path segment -- with a
// trailing web extension (.html/.php/...) stripped -- is a jobs index/section word
// (terminalHubWords plus extractIndexTerminals). Such a page is a hub, not a single
// posting, even when it sits on a job-posting path.
func isExtractIndexTerminal(rawURL string) bool {
	segs := pathSegmentsOf(rawURL)
	if len(segs) == 0 {
		return false
	}
	last := strings.ToLower(stripWebExt(segs[len(segs)-1]))
	for _, w := range terminalHubWords {
		if last == strings.ToLower(w) {
			return true
		}
	}
	for _, w := range extractIndexTerminals {
		if last == w {
			return true
		}
	}
	return false
}

// stripWebExt drops a trailing static-page file extension so an index page served
// as "stellenangebote.html" matches the same word as a bare "stellenangebote".
func stripWebExt(s string) string {
	for _, ext := range []string{".html", ".htm", ".php", ".aspx", ".asp", ".jsp"} {
		if len(s) > len(ext) && strings.EqualFold(s[len(s)-len(ext):], ext) {
			return s[:len(s)-len(ext)]
		}
	}
	return s
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
