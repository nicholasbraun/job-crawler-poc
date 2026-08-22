// The Extract Gate's Posting Score (ADR-0049): a learned, graded estimate that a
// page is one Job Listing, computed from the Score Signals the page carries. It is
// pure -- a URL and parsed page content in, a number out; no model, no network, no
// database -- so the gate keeps that property.
//
// NOTHING CONSULTS IT YET. The Learned Veto rung that spends the score is #301, so a
// fitted table still moves no gate verdict: posting_score_weights_gen.go now carries
// what "llmbench train-scorer" fitted over the committed Extract Gold Set, and the only
// things that read it are this file and its tests.
//
// The learned half lives in the gate's OWN package rather than a sub-package, and
// that is forced rather than chosen: the Score Signals reuse the gate's unexported
// detectors, so a sub-package would import pagegate while pagegate imports it for
// Score -- an import cycle. The rejected ways out (moving ADR-0044's argued word
// lists into a third leaf package; duplicating the detectors) are recorded in
// ADR-0049.
//
// The signals are read as GRADATIONS rather than as the thresholded marks Positive
// Evidence collapses them into, and that is the entire point of the rung: every page
// this score will judge has already cleared those thresholds, so within that band the
// marks are near-constant and only the degrees still separate anything. ADR-0016 made
// the same move once already, folding the Gate's job-link count in continuously
// instead of reading it at one cut.
//
// (Detached from the package clause on purpose: pagegate.go carries the package doc
// comment, and a package must have only one.)

package pagegate

import (
	"math"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// Every Score Signal is a PRESENCE indicator: a name the page either carries or does
// not. The uniformity is deliberate -- ADR-0044 measured that what distinguishes a
// posting is the RANGE of sections, not the frequency of any one word, and the same
// reading is applied to every signal here. So the fitted table reads as "what each
// thing is worth" and Score is a plain sum of weights.
//
// A graded count is therefore written as a THERMOMETER: one "_ge_k" indicator per
// level, with every level up to the page's value firing. That keeps every signal
// binary while preserving the count's ORDER -- a one-hot per exact value discards it,
// and with a few hundred labelled rows the ordering is information the fit can use.
// It degrades to a linear-in-count term when the fitted weights come out equal, while
// still permitting a non-monotone shape, which body length needs.
//
// Three prefixes, one namespace each. A vocabulary word is letters only (see
// appendWords), so it can never collide with a structural name.
const (
	structuralSignalPrefix = "sig:"
	titleWordPrefix        = "title:"
	bodyWordPrefix         = "body:"
)

// vocabularyGroupGrades name one level per posting section postingVocabularyGroups
// holds, so the count enters across its FULL range rather than as ADR-0044's boolean
// at postingVocabularyGroupsStrong. Its length must equal len(postingVocabularyGroups)
// -- TestVocabularyGroupGradesCoverEveryVocabularyGroup holds that -- and adding a
// sixth posting section is a retrain rather than an edit, because a level's name is a
// key in the fitted table.
var vocabularyGroupGrades = []string{
	structuralSignalPrefix + "vocab_groups_ge_1",
	structuralSignalPrefix + "vocab_groups_ge_2",
	structuralSignalPrefix + "vocab_groups_ge_3",
	structuralSignalPrefix + "vocab_groups_ge_4",
	structuralSignalPrefix + "vocab_groups_ge_5",
}

// jobLinkGrades span the distinct same-host Job Listing link counts that can REACH
// this score: rung 7 rejects at cfg.ExtractJobLinkSaturationCount (5 under
// DefaultLLMGateConfig), so a page arriving here carries 0-4. A higher count
// saturates at the last level, exactly as ADR-0016's min(count/K, 1) saturates.
//
// The ceiling is a package constant rather than the config's K on purpose. Signals
// must stay a pure, config-independent function of the page -- the same isolation
// ADR-0019 demands of the extract path -- and the trainer reads the identical signal
// off a Gold Set row where no gate config is in play.
var jobLinkGrades = []string{
	structuralSignalPrefix + "job_links_ge_1",
	structuralSignalPrefix + "job_links_ge_2",
	structuralSignalPrefix + "job_links_ge_3",
	structuralSignalPrefix + "job_links_ge_4",
}

// bodyLengthGrades bucket the page's body length on a DOUBLING scale, because what
// separates a posting from a hub is an order of magnitude of body, not a hundred
// characters. The edges are drawn from the 457 committed Extract Gold Set rows, where
// `detail` bodies sit at p10 1.8 KB / median 3.9 KB / p90 8.2 KB while everything else
// spreads both shorter (p10 278 B) and longer (p90 10.3 KB). Above 32 KB there are 3
// rows in all, one of them `detail` -- too few to earn a level.
//
// Measured in BYTES of the folded body BEFORE the scan cap, so a page far above the
// cap is still distinguishable from one just under it.
var bodyLengthGrades = []struct {
	minBytes int
	name     string
}{
	{256, structuralSignalPrefix + "body_bytes_ge_256"},
	{512, structuralSignalPrefix + "body_bytes_ge_512"},
	{1024, structuralSignalPrefix + "body_bytes_ge_1024"},
	{2048, structuralSignalPrefix + "body_bytes_ge_2048"},
	{4096, structuralSignalPrefix + "body_bytes_ge_4096"},
	{8192, structuralSignalPrefix + "body_bytes_ge_8192"},
	{16384, structuralSignalPrefix + "body_bytes_ge_16384"},
}

// lonePostingSignal marks a page whose structured data publishes exactly one posting,
// read through crawler.LonePosting (ADR-0042) -- the codebase's one structured-posting
// read -- so the score, the Extract Gate's rungs and the Posting Body cannot drift
// into three answers about one page.
const lonePostingSignal = structuralSignalPrefix + "lone_posting"

// TWO SIGNALS ARE DELIBERATELY ABSENT, and this is where that is on the record.
//
// The ATS Embed: rung 5 rejects every page carrying one, so at the position this
// score is spent the signal is constant-false and a weight on it could only fit noise.
//
// A posting-shaped URL: ADR-0049 enumerates the Score Signals and postingURL is not
// among them. Inside the rung-8 accept set it is a strong-tier ADMITTING mark rather
// than something that separates those accepts (the probe's "URL alone 0.614" was
// measured over all scorable rows, not over the accepts). It is one line plus a
// retrain if the within-accepts curve disappoints.

// scoreTextMaxBytes bounds the text the Score Vocabulary scan reads. The gate runs on
// every walked page and this score sees every page Positive Evidence admits, while the
// largest body observed in the capture is ~757 KB -- an unbounded tokenize-and-sort
// over that is a per-page cost nobody signed up for. 64 KiB is above all but ONE of
// the 457 committed Extract Gold Set rows (the largest is 77,292 bytes) and cuts the
// worst captured page to under a tenth of its length.
//
// The cap applies identically when training and when serving, because both go through
// Signals -- which is what keeps the truncation from becoming a difference between the
// rule that was measured and the rule that ships. A word straddling the cut is
// truncated, which is deterministic and cannot recur across pages often enough to earn
// a weight.
const scoreTextMaxBytes = 64 << 10

// scoreMinWordRunes is the shortest token that can become a Score Vocabulary word. A
// one-letter token carries no lexical signal and would multiply the candidate set with
// initials and the letters of "(m/w/d)", whose meaning ADR-0044 already reads as a
// SHAPE in the Title rather than as three letters. Counted in RUNES, not bytes: "ä" is
// two bytes.
//
// There is deliberately no hand-written list of common words to exclude. Which words
// are worth weight is what the fit decides; curating that by hand would put an argued
// rule back inside the one rule in this package that is meant to be learned.
const scoreMinWordRunes = 2

// Signals returns the Score Signals a page carries (ADR-0049): the names of its graded
// Structural Signals, and of the Score Vocabulary candidates in its Title and body.
//
// It is EXPORTED because the trainer (llmbench train-scorer) must call the same
// implementation the gate calls. A trainer that tokenized German slightly differently
// from the gate would make the shipped rule silently not the rule that was measured;
// one shared implementation makes that unrepresentable.
//
// Pure: URL and parsed content in, names out; no model, no network, no database. u is
// read ONLY as the base for same-host link counting -- the URL contributes no WORDS, so
// the Score Vocabulary cannot learn the handful of hosts the Gold Set happens to sample.
//
// It reuses the gate's own detectors -- vocabularyGroupCount, foldedBody, foldedTitle,
// countJobPostingLinks, crawler.LonePosting -- rather than reimplementing them, so the
// score and the reject rungs can never give two answers about one page. That reuse is
// the whole reason the scorer lives in this package.
//
// Body text is the page's Flattened Text (foldedBody, ADR-0046), exactly as the
// Positive Evidence marks read it, so the Structural Rendering switch cannot move what
// the score sees. Presence is binary, never a count: a word repeated five times is one
// signal. Title words and body words occupy separate namespaces, so the same word in a
// Title is a different signal from the same word in the body.
//
// It emits every CANDIDATE signal the page carries, not only the ones the fitted table
// weighs: the trainer needs the full candidate set to choose the Score Vocabulary from,
// and Score simply finds no weight for the rest. That is what keeps training and
// serving one code path.
//
// The order is deterministic -- structural signals in table order, then Title words
// sorted, then body words sorted, with no duplicates -- because Score sums in that
// order and a float sum in a randomized order is not reproducible.
//
// content must be non-nil, the same contract hasPositiveEvidence holds; every call site
// parses the page first.
func Signals(u crawler.URL, content *crawler.Content) []string {
	body := foldedBody(content)
	sigs := make([]string, 0, 256)
	sigs = appendGrades(sigs, vocabularyGroupGrades, vocabularyGroupCount(body))
	sigs = appendGrades(sigs, jobLinkGrades, countJobPostingLinks(u, content))
	for _, grade := range bodyLengthGrades {
		if len(body) >= grade.minBytes {
			sigs = append(sigs, grade.name)
		}
	}
	if _, ok := crawler.LonePosting(content); ok {
		sigs = append(sigs, lonePostingSignal)
	}
	sigs = appendWords(sigs, titleWordPrefix, foldedTitle(content))
	sigs = appendWords(sigs, bodyWordPrefix, body)
	return sigs
}

// Score returns the page's Posting Score (ADR-0049): the Extract Gate's learned, graded
// estimate that the page is one Job Listing, in [0,1]. Pure, on the same contract as
// Signals.
//
// The functional form is fixed HERE and the training run supplies only the numbers: the
// weights of the Score Signals the page carries, summed, plus a fitted intercept,
// squashed by the logistic. A signal with no entry in the table contributes nothing,
// which is what lets the Score Vocabulary be a truncated list rather than a required one.
//
// [0,1] rather than the raw sum: the squash is monotone, so the veto is the same rule
// either way, but a bounded range is what lets VetoThreshold be read as a number, lets a
// fixed-bucket histogram record the live distribution, and lets a false-drop log line
// say something interpretable. VetoThreshold is a value in this same space, so the
// trainer must apply this same squash when it chooses the cut.
//
// It goes through Signals rather than a fused fast path because a second path is a
// second implementation, which is exactly what this design exists to prevent; summing in
// Signals' order is what makes the result reproducible.
//
// NOTHING CALLS IT YET -- the Learned Veto rung is #301.
func Score(u crawler.URL, content *crawler.Content) float64 {
	z := scoreIntercept
	for _, signal := range Signals(u, content) {
		z += scoreWeights[signal]
	}
	return 1 / (1 + math.Exp(-z))
}

// appendGrades appends the first min(n, len(grades)) level names: the thermometer
// encoding, saturating at the table's last level so a count beyond the table is read as
// "at least this much" rather than dropped.
func appendGrades(dst, grades []string, n int) []string {
	return append(dst, grades[:min(n, len(grades))]...)
}

// appendWords appends prefix+word for every distinct Score Vocabulary candidate in
// text, in sorted order. text is ALREADY folded and lowercased (foldedBody /
// foldedTitle) -- do not re-fold it here: reading the same folded strings the Positive
// Evidence marks read is half of the no-drift property.
//
// The split predicate is unicode.IsLetter, never an ASCII range, because roughly half
// the signal is German: "Qualitätssicherung" and "Führungskräfte" must tokenize whole,
// and an ASCII tokenizer would shatter them at every umlaut. Digits and punctuation are
// separators, so no page number or id can enter the Score Vocabulary.
//
// The map and the sort are per-page trivia at these sizes: over the committed Extract
// Gold Set a page yields a median of 212 distinct words and at most ~2,250.
func appendWords(dst []string, prefix, text string) []string {
	seen := map[string]struct{}{}
	for _, token := range strings.FieldsFunc(truncateAtRune(text, scoreTextMaxBytes), func(r rune) bool {
		return !unicode.IsLetter(r)
	}) {
		if utf8.RuneCountInString(token) < scoreMinWordRunes {
			continue
		}
		seen[token] = struct{}{}
	}
	words := make([]string, 0, len(seen))
	for word := range seen {
		words = append(words, word)
	}
	slices.Sort(words)
	for _, word := range words {
		dst = append(dst, prefix+word)
	}
	return dst
}

// truncateAtRune cuts s to at most maxBytes, backing up to a rune boundary so the cut
// can never split a multi-byte character into invalid UTF-8.
func truncateAtRune(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}
