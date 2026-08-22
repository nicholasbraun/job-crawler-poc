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
	"regexp"
	"strings"
	"unicode"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// hasPositiveEvidence reports whether a page that has cleared every reject rung
// carries Positive Evidence of being a single posting (ADR-0044). Strong evidence
// admits alone: a posting-shaped URL, a lone structured-data posting, a Title
// announcing one vacancy, or four of the five posting sections. The weak marks --
// two posting sections, an apply affordance, a role designation in the Title --
// admit only in agreement.
//
// The TIER SHAPE is the measurement (see the file comment) and does not change; what
// #257 changed is which marks exist, each one taken from a page the Boundary Stratum
// recorded this rung dropping.
//
// content must be non-nil, the same contract the content reject rungs already
// hold (atsEmbed dereferences it too); every ShouldExtract call site parses the
// page first. It reads content.Title as well as the body: both live call sites (the
// walk's URL processor and the refetch lane) hand it a freshly parsed Content, so
// the title is populated on both.
//
// fold carries the page's folded body, unfolded until a mark below asks for it, so the
// Learned Veto rung reads the same copy this rung read rather than making a second one
// (ADR-0049). It must be a fold of the same content.
func hasPositiveEvidence(u crawler.URL, content *crawler.Content, fold *bodyFold) bool {
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
	// The Title says a specific role is WANTED. Strong, not paired: the pages this
	// recovers include one whose body carries no posting section at all
	// (seedcamp.com/views/visiting-analyst), so pairing it with even one vocabulary
	// group loses it.
	if titleAnnouncesVacancy(content) {
		return true
	}
	// From here down every mark reads the BODY, so fold it once and hand the folded
	// string to each of them. The marks below run on the majority of the pages this
	// rung sheds -- everything that reaches rung 8 without a posting-shaped URL -- and
	// the largest body observed in the capture is ~757 KB, so a fold per mark would be
	// three full copies of it per page on the gate's hottest path. The fold sits BELOW
	// the URL, structured-data and Title marks on purpose: a page they admit never
	// pays for it at all -- which is why the fold is asked for HERE and not at the top,
	// even though the caller already holds it.
	body := fold.folded()
	// Counted once and reused: the strong threshold and the weak tier read the same
	// number, and re-deriving it would walk all five groups a second time.
	groups := vocabularyGroupCount(body)
	// Enough of a posting's sections that the page is not mentioning a posting, it is
	// being one.
	if groups >= postingVocabularyGroupsStrong {
		return true
	}
	// Weak marks, in agreement only (ADR-0044). Neither admits alone: an apply
	// affordance alone fires on 33% of non-postings, and it is the entire remaining
	// bill in the OR form the tiered rule dominates. Dense posting vocabulary is the
	// anchor; either corroborator completes the pair.
	return groups >= postingVocabularyGroupsRequired &&
		(applyAffordance(body) || titleDesignatesOneRole(content))
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
	// "role" is the same kind of job word as "position" and "opening" -- /open-roles/
	// <id> is a board section like /openings/<id>. Like every word here it only ever
	// fires on a NON-TERMINAL segment, so a bare /open-roles index still does not admit.
	"role", "roles",
	// German. Load-bearing, not decorative: German career sites publish postings at
	// compound paths (/jobs-karriere/<slug>, /stellenangebote_unternehmen/<firm>/<slug>)
	// and job-word hosts (karriere.<company>.de/<slug>) the shared predicate misses.
	"karriere", "stelle", "stellen", "stellenangebot", "stellenangebote",
	"stellenanzeige", "stellenanzeigen", "stellenausschreibung",
	"stellenausschreibungen", "stellenmarkt", "jobangebot", "jobangebote",
	"bewerbung", "bewerben",
	// German apprenticeships. An Ausbildung listing IS a Job Listing, and German
	// employers publish it at its own section (/ausbildungsberuf/<slug>,
	// /ausbildungen/<slug>) rather than under /stellenangebote.
	//
	// Measured and REJECTED alongside these: "praktikum" / "praktika" -- 0 additional
	// postings and 2 extra leaks on the gold set. No evidence, so they stay out.
	"ausbildung", "ausbildungen", "ausbildungsberuf", "ausbildungsberufe",
	"ausbildungsplatz", "ausbildungsplaetze",
}

// postingURL reports whether u's shape marks it a single posting for the Positive
// Evidence rung: a job word as a token of a path segment that is followed by a
// further segment (so a compound segment such as "jobs-karriere" or
// "stellenangebote_unternehmen" counts, and a bare index segment does not), a job
// word in the host with any path at all, or -- failing both -- a FINAL segment that
// reads as a role slug, which isRoleSlug judges under much stricter rules because a
// terminal segment is where hub slugs live.
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
	// Nothing above the last segment named a section, so the last segment is all
	// there is: read it as a role slug, under the much stricter rules below.
	return isRoleSlug(segs[len(segs)-1])
}

// roleSlugPostingNouns name ONE posting, which is what licenses reading a job word
// inside the FINAL segment -- the role slug itself ("/stellenangebot_<role>",
// "/ausbildung-in-gifhorn-zum-anlagenmechaniker"). A PLURAL section word may not:
// "careers-at-sedus", "career-open-positions" and "offene-stellen-schulamt-neuruppin"
// are hub slugs of exactly the same shape, and admitting them is the
// /karriere-bei-bitsea failure mode ADR-0016 already names. Running this rule over
// the full extractPostingWords list was measured: +2 postings for +9 leaks. The
// singular restriction is the whole predicate; relaxing it deletes the predicate.
var roleSlugPostingNouns = []string{
	"stellenangebot", "stellenanzeige", "stellenausschreibung", "jobangebot",
	"vacancy", "ausbildung",
}

// roleSlugMinTokens is how many delimiter-separated tokens the final segment must
// carry before a posting noun inside it reads as a role slug rather than a section
// name. A role slug is long; "ausbildung-bei-uns" and "stellenangebot-nicht-dabei"
// are not. The gold set cannot settle this floor -- 3, 4 and 5 score identically on
// it, because the two rows it recovers carry 6 and 9 tokens -- so it is a judgement,
// taken on the shape of the German section slugs it has to exclude.
const roleSlugMinTokens = 4

// isRoleSlug reports whether a URL's FINAL path segment is a role slug: long enough
// to be naming a role, and carrying a posting noun that names exactly one posting.
// It is the only place this rung reads a job word in a terminal segment, and it is
// deliberately much narrower than segmentHasPostingWord -- a terminal segment is
// where hub slugs live.
func isRoleSlug(segment string) bool {
	tokens := splitURLTokens(stripWebExt(segment))
	if len(tokens) < roleSlugMinTokens {
		return false
	}
	for _, token := range tokens {
		for _, noun := range roleSlugPostingNouns {
			if token == noun {
				return true
			}
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
// They are deliberately DISJOINT from postingVocabularyGroups, and disjoint under
// SUBSTRING matching, not merely as sets of strings -- both lists are read with
// strings.Contains, so a vocabulary phrase that is a substring of an apply phrase is
// counted by both marks off one run of text, turning the tier's agreement
// requirement into a disjunction for that phrase. That is exactly the shape
// ADR-0044's measurement rejects, and it is why "ihre bewerbung" / "deine bewerbung"
// sit in the vocabulary group below and not here even though they read like an offer
// to apply, and why "your application" is not in that group.
// TestApplyPhrasesAndVocabularyGroupsAreDisjoint holds the invariant.
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
// and never admits a page on its own. body is the page's ALREADY-FOLDED main text
// (foldedBody); it is passed in rather than folded here so the rung folds once.
func applyAffordance(body string) bool {
	return containsAny(body, applyPhrases)
}

// vacancyTitlePhrases are the whole phrases a Title uses to announce that ONE role
// is wanted. The trailing space is load-bearing: without it "hiring an" matches
// "hiring analysts", and a newsroom announcing that it is hiring analysts is not a
// posting.
//
// Measured and REJECTED: "wir suchen" and "we are looking for a" -- 0 additional
// postings on the gold set, and "Wir suchen Verstärkung" is a careers-landing title.
var vacancyTitlePhrases = []string{"hiring a ", "hiring an "}

// vacancyTitleWord is the German half of the same idiom: "<Rolle> gesucht". It is
// matched as a WHOLE WORD, never a substring, and that is not fussiness --
// "Stellengesuche" are positions sought BY JOB SEEKERS (a distinction this package
// already keeps in its index terminals) and "meistgesuchte" is a superlative. Both
// would match a substring test.
const vacancyTitleWord = "gesucht"

// titleAnnouncesVacancy reports whether content's Title says a specific role is
// wanted. It is STRONG evidence: a title is a page's own claim about what the page
// is, and "we are hiring a <role>" / "<Rolle> gesucht" is a claim only a posting
// makes. The English and German forms sit together for the same reason applyPhrases
// and the vocabulary groups keep both -- they are one idiom in two languages.
func titleAnnouncesVacancy(content *crawler.Content) bool {
	title := foldedTitle(content)
	if containsAny(title, vacancyTitlePhrases) {
		return true
	}
	for _, token := range strings.FieldsFunc(title, func(r rune) bool {
		return !unicode.IsLetter(r)
	}) {
		if token == vacancyTitleWord {
			return true
		}
	}
	return false
}

// roleDesignation is the German equal-treatment marker a posting's role name carries
// ("(m/w/d)", "(w/m/d)", "(m/f/d)", "(m/w/x)"). In a TITLE it means the title names
// ONE ROLE.
//
// It is WEAK on purpose. Alone it also fires on a posting header followed by a rail
// of other postings (jobportal.hs-hannover.de/Praktikum/<role>) and on a title with
// no role body under it at all (qvls.de/de/postdoctoral-researcher-m-f-d) -- two of
// the shapes the blind re-read took OUT of `detail`. As STRONG it was measured: +6
// postings, +4 leaks including exactly those three rows.
var roleDesignation = regexp.MustCompile(`\(\s*[mwfd]\s*[/|:]\s*[mwfdx]\s*([/|:]\s*[mwfdx]\s*)?\)`)

// titleDesignatesOneRole reports whether content's Title carries a role designation.
// It corroborates dense posting vocabulary; it never admits a page by itself, and it
// is deliberately NOT paired with the apply affordance instead -- that pairing
// re-admits the rail-of-postings pages, which carry an apply action and exactly one
// posting section.
func titleDesignatesOneRole(content *crawler.Content) bool {
	return roleDesignation.MatchString(foldedTitle(content))
}

// foldedTitle returns content's Title case-folded and whitespace-collapsed. The
// collapse is not cosmetic: titles in the wild carry newlines and runs of spaces,
// which would otherwise break a phrase match down the middle.
func foldedTitle(content *crawler.Content) string {
	return strings.ToLower(strings.Join(strings.Fields(content.Title), " "))
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
	//
	// "your application" is deliberately ABSENT: it is a substring of applyPhrases'
	// "submit/start/send us your application", so an apply button alone would have
	// scored this group AND the apply affordance -- the disjunction the tier shape
	// exists to prevent (see applyPhrases). "how to apply" and the German forms cover
	// the section heading itself, which is what this group is for.
	{"application process", "how to apply",
		"bewerbungsprozess", "ihre bewerbung", "deine bewerbung", "ansprechpartner"},
}

// postingVocabularyGroupsRequired is how many DISTINCT groups above must appear
// before the page reads as posting-dense. Two, from the ADR-0044 replay: one group
// fires on hub cards and careers landing copy, and dropping to one was the
// max-recall variant that cost ~70% more calls for ~0 recall.
const postingVocabularyGroupsRequired = 2

// postingVocabularyGroupsStrong is the group count at which posting vocabulary stops
// being a weak mark and stands alone: a page carrying four of the five sections a
// posting has is not mentioning a posting, it is being one.
//
// FOUR, from the gold set. THREE was measured and REJECTED: it re-admits
// massinc.org/about-us/career-opportunities and ifes.uni-hannover.de/eev/hiwi, two of
// the standing-call and rail-of-postings rows a blind re-read had just taken out of
// `detail`. A threshold that re-admits the rows a human just rejected is moving the
// wrong way. FIVE recovers nothing.
const postingVocabularyGroupsStrong = 4

// vocabularyGroupCount counts the DISTINCT posting sections body carries. body is
// the page's ALREADY-FOLDED main text (foldedBody). It has no early exit -- both
// thresholds read the count, not a verdict -- and hasPositiveEvidence holds the
// result in a local so the five groups are walked once per page.
func vocabularyGroupCount(body string) int {
	groups := 0
	for _, group := range postingVocabularyGroups {
		if containsAny(body, group) {
			groups++
		}
	}
	return groups
}

// foldedBody returns the page's Flattened Text (ADR-0046), case-folded, so a phrase
// split across a line break — or across a heading or list item once the parser renders
// structure — still matches. Deriving it here rather than reading the field raw is what
// keeps every mark scoring exactly the text it scores today, whether the parser is
// rendering structure or not.
//
// It lowercases here rather than leaving it to containsAny even though containsAny
// would fold it anyway: the body marks call containsAny up to six times per page,
// and each call would otherwise copy the whole body again. hasPositiveEvidence
// calls this ONCE and passes the result to every mark that reads the body, which is
// what keeps the rung's cost flat on a gate that runs on every walked page. Do not
// re-fold inside a mark. Idempotent, so the second fold inside containsAny is a
// no-op.
func foldedBody(content *crawler.Content) string {
	return strings.ToLower(crawler.FlattenedText(content.MainContent))
}

// bodyFold is one page's foldedBody, computed AT MOST ONCE and shared by every rung
// that reads it. Two rungs do: Positive Evidence (rung 8) and, when it is armed, the
// Learned Veto's Score Signals (rung 9, ADR-0049). Folding per rung would mean a
// second full copy of a body observed at up to ~757 KB in the capture, on a gate that
// runs on every walked page with fifty workers behind it.
//
// It is LAZY on purpose, not as an optimisation detail. hasPositiveEvidence's URL,
// structured-data and Title marks admit a page without reading the body at all, and
// that early exit is load-bearing: a page they admit must keep paying nothing for the
// fold, whatever rung 9's switch says. The zero value is unusable; call newBodyFold.
type bodyFold struct {
	content *crawler.Content
	text    string
	done    bool
}

// newBodyFold prepares a page's fold without performing it.
func newBodyFold(content *crawler.Content) *bodyFold { return &bodyFold{content: content} }

// folded returns the page's foldedBody, folding on first use.
func (b *bodyFold) folded() string {
	if !b.done {
		b.text, b.done = foldedBody(b.content), true
	}
	return b.text
}
