// The Extract Gate's Positive Evidence rung (ADR-0044): the marks that a page
// clearing every reject rung IS one posting, rather than merely not a hub. It is
// pure -- URL string and parsed page content in, a bool out; no model, no network,
// no database.
//
// The evidence is TIERED, not a disjunction of equals, and that shape is a
// measurement, not a taste. Over 2199 live pages clearing the reject rungs,
// weighted to the live stream: an OR of all four marks keeps 88.5% of the pages
// the extractor accepts at a 0.505 call rate, while the tiered rule keeps 90.6% at
// 0.178 -- better recall at a third of the cost, because an apply affordance alone
// fires on a third of non-postings. Do not simplify this back to an OR.
//
// TWO NOTIONS OF "POSTING URL" LIVE IN THIS PACKAGE, AND THAT IS DELIBERATE.
// IsPostingPath (pagegate.go) is the SHARED predicate: the Discovery Gate's
// career-page veto (ADR-0010) and the Catalog Doctor's removal planner replay it
// over stored Catalog rows, and isJobPostingPath additionally decides job-link
// saturation counting on both gates. postingURL below is this rung's OWN, wider
// reading, with its OWN word list. Widening the shared one to cover the German
// long tail would retroactively hard-delete Catalog rows and shift saturation
// counting -- the config-coupling hazard ADR-0019 named and ADR-0044 re-states.
// The duplication is the price of that isolation. Merging them is not a cleanup;
// it is the bug.
//
// (Detached from the package clause on purpose: pagegate.go carries the package
// doc comment, and a package must have only one.)

package pagegate

import (
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// hasPositiveEvidence reports whether a page that has cleared every reject rung
// carries Positive Evidence of being a single posting (ADR-0044). Strong evidence
// admits alone: a posting-shaped URL, or a lone structured-data posting. The weak
// text marks admit only in agreement with each other.
//
// content must be non-nil, the same contract the content reject rungs already
// hold (atsEmbed dereferences it too); every ShouldExtract call site parses the
// page first.
func hasPositiveEvidence(u crawler.URL, content *crawler.Content) bool {
	if postingURL(u) {
		return true
	}
	// A lone structured posting, read through the domain's one structured-posting
	// read (ADR-0042) so the gate, the Posting Body and the Free Extraction cannot
	// drift into three answers about one page. Rung 6 has already rejected an
	// openings index, so reaching here with ok means exactly one posting node.
	//
	// It contributes NO unique admissions -- on the replay every page carrying it
	// already carried another mark. It is kept for PRECISION (it admits 0.2% of
	// non-postings), not coverage; its redundancy is recorded here so a later reader
	// does not mistake it for a bug and delete it.
	//
	// Deliberately NOT title-gated: ok is a statement about the page's SHAPE, and a
	// title-less lone posting node is still evidence the page is one posting. The
	// non-empty title ADR-0042 requires is a Free Extraction condition -- what the
	// mechanism needs to SAVE without a model -- not a condition on spending a call.
	if _, ok := crawler.LonePosting(content); ok {
		return true
	}
	// Weak marks, in agreement only (ADR-0044). Neither admits alone: an apply
	// affordance alone fires on 33% of non-postings, and it is the entire remaining
	// bill in the OR form the tiered rule dominates.
	return applyAffordance(content) && postingVocabulary(content)
}

// extractPostingWords are the job words this rung reads inside a URL's path
// segments and host. It is the Positive Evidence rung's OWN list and must never be
// merged with jobPathSegments (see the file comment): jobPathSegments is replayed
// by the Catalog Doctor and decides job-link saturation counting, so a word added
// here to catch a German posting must not be able to delete a Catalog row.
//
// It is a superset of jobPathSegments by construction -- every shape the shared
// predicate calls a posting is one this rung also calls a posting -- plus the
// compound-segment and job-word-host forms the shared predicate cannot see, which
// ADR-0044 measures as the single highest-leverage part of this design.
var extractPostingWords = []string{
	// English.
	"job", "jobs", "career", "careers", "vacancy", "vacancies",
	"position", "positions", "opening", "openings",
	"opportunity", "opportunities", "hiring", "recruiting", "recruitment", "apply",
	// German. Load-bearing, not decorative: German career sites publish postings at
	// compound paths (/jobs-karriere/<slug>, /stellenangebote_unternehmen/<firm>/<slug>)
	// and job-word hosts (karriere.<company>.de/<slug>) the shared predicate misses.
	"karriere", "stelle", "stellen", "stellenangebot", "stellenangebote",
	"stellenanzeige", "stellenanzeigen", "stellenausschreibung",
	"stellenausschreibungen", "stellenmarkt", "jobangebot", "jobangebote",
	"bewerbung", "bewerben",
}

// postingURL reports whether u's shape marks it a single posting for the Positive
// Evidence rung: a job word as a token of a path segment that is followed by a
// further segment (so a compound segment such as "jobs-karriere" or
// "stellenangebote_unternehmen" counts, and a bare index segment does not), or a
// job word in the host with any path at all.
//
// It reads u.RawURL rather than u.Hostname so it stays a pure function of the URL
// STRING, the way IsPostingPath is: the crawl-lane refetch builds a crawler.URL
// from a stored listing URL with no Hostname set, and this predicate must give that
// caller the same answer as the live walk.
func postingURL(u crawler.URL) bool {
	segs := pathSegmentsOf(u.RawURL)
	if len(segs) == 0 {
		return false
	}
	// A job word in the host marks every page beneath it a posting, provided there
	// IS a page beneath it: karriere.acme.de/senior-entwickler is a posting,
	// karriere.acme.de is the hub. (Rung 2b already sheds the bare root, but the
	// predicate stays correct read on its own.)
	if hostHasPostingWord(hostOf(u.RawURL)) {
		return true
	}
	// A job word inside a path segment, followed by a further segment -- the segment
	// names the section, the segments after it name the role.
	for _, seg := range segs[:len(segs)-1] {
		if segmentHasPostingWord(seg) {
			return true
		}
	}
	return false
}

// hostHasPostingWord reports whether host carries a job word as one of its
// dot/hyphen-separated labels ("karriere.acme.de", "jobs-acme.de"). Tokens rather
// than a substring test, for the same reason segmentHasPostingWord uses them.
func hostHasPostingWord(host string) bool {
	if host == "" {
		return false
	}
	for _, token := range splitURLTokens(host) {
		for _, word := range extractPostingWords {
			if token == word {
				return true
			}
		}
	}
	return false
}

// segmentHasPostingWord reports whether a path segment carries a job word as one
// of its delimiter-separated tokens, or is one outright. Tokens, never substrings:
// a substring test would read German "bestellen", "herstellen" and "Baustelle" as
// "stellen", and "disposition" as "position".
func segmentHasPostingWord(segment string) bool {
	for _, token := range splitURLTokens(segment) {
		for _, word := range extractPostingWords {
			if token == word {
				return true
			}
		}
	}
	return false
}

// splitURLTokens splits a host label or path segment on the separators sites
// actually use ('-', '_', '.') and lowercases each token. A segment with no
// separator yields itself, so the whole-segment match falls out of the same code.
func splitURLTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
}

// hostOf returns rawURL's lowercased host, or "" when it cannot be parsed. It is
// the host side of postingURL's promise to read only the URL string.
func hostOf(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// applyPhrases are the whole phrases a page uses to offer an application. They are
// PHRASES, never tokens, the same discipline the domain's closure phrases keep: a
// bare "bewerben" or "apply" token has a second reading on half the careers web
// ("Bewerbungsprozess", "apply filters"), while a phrase this specific has none.
//
// They are deliberately DISJOINT from postingVocabularyGroups: a phrase counted by
// both marks would turn the tier's agreement requirement into a disjunction for
// that phrase, which is exactly the shape ADR-0044's measurement rejects. That is
// why "ihre bewerbung" / "deine bewerbung" sit in the vocabulary group below and
// not here, even though they read like an offer to apply.
var applyPhrases = []string{
	// English.
	"apply now", "apply today", "apply online", "apply for this job",
	"apply for this position", "apply for this role",
	"submit your application", "start your application", "send us your application",
	// German. Load-bearing.
	"jetzt bewerben", "jetzt online bewerben", "jetzt hier bewerben",
	"online bewerben", "hier bewerben", "bewerben sie sich", "bewirb dich",
	"zur online-bewerbung",
}

// applyAffordance reports whether the page offers an application -- the leakiest of
// the four marks (it fires on ~33% of non-postings), which is why it is weak-tier
// and never admits a page on its own.
func applyAffordance(content *crawler.Content) bool {
	return containsAny(foldedBody(content), applyPhrases)
}

// postingVocabularyGroups are the sections a job posting's body has and a hub's
// does not, grouped by what each section SAYS. A group counts ONCE however many of
// its phrases hit, so a page repeating "Aufgaben" five times is still one group --
// what distinguishes a posting is the RANGE of sections, not the frequency of any
// one word. English and German forms sit in the same group deliberately: they are
// the same section, and a bilingual page must not score two.
var postingVocabularyGroups = [][]string{
	// What the role does.
	{"responsibilities", "your role", "what you will do", "what you'll do",
		"your tasks", "ihre aufgaben", "deine aufgaben", "aufgabengebiet",
		"das erwartet dich", "tätigkeiten"},
	// What the role requires.
	{"requirements", "qualifications", "your profile", "what you bring",
		"who you are", "ihr profil", "dein profil", "anforderungen",
		"qualifikationen", "das bringst du mit", "voraussetzungen"},
	// What the employer offers.
	{"we offer", "what we offer", "benefits", "perks", "wir bieten",
		"das bieten wir", "unser angebot", "deine vorteile", "ihre vorteile"},
	// The terms of employment.
	{"full-time", "part-time", "employment type", "permanent position",
		"vollzeit", "teilzeit", "festanstellung", "unbefristet", "befristet",
		"arbeitszeit", "eintrittsdatum", "berufserfahrung"},
	// How to apply, and who to ask.
	{"application process", "how to apply", "your application",
		"bewerbungsprozess", "ihre bewerbung", "deine bewerbung", "ansprechpartner"},
}

// postingVocabularyGroupsRequired is how many DISTINCT groups above must appear
// before the page reads as posting-dense. Two, from the ADR-0044 replay: one group
// fires on hub cards and careers landing copy, and dropping to one was the
// max-recall variant that cost ~70% more calls for ~0 recall.
const postingVocabularyGroupsRequired = 2

// postingVocabulary reports whether the page's text carries at least
// postingVocabularyGroupsRequired distinct posting sections.
func postingVocabulary(content *crawler.Content) bool {
	body := foldedBody(content)
	groups := 0
	for _, group := range postingVocabularyGroups {
		if containsAny(body, group) {
			groups++
			if groups >= postingVocabularyGroupsRequired {
				return true
			}
		}
	}
	return false
}

// foldedBody returns content's main text case-folded and whitespace-collapsed, so
// a phrase split across a line break still matches. The parser already collapses
// whitespace; this repeats it so a captured or hand-written Content is read the
// same way.
//
// It lowercases here rather than leaving it to containsAny even though containsAny
// would fold it anyway: the two marks call containsAny up to six times per page,
// and each call would otherwise copy the whole body again. Folding once keeps the
// rung's cost flat on a gate that runs on every walked page. Idempotent, so the
// second fold inside containsAny is a no-op.
func foldedBody(content *crawler.Content) string {
	return strings.ToLower(strings.Join(strings.Fields(content.MainContent), " "))
}
