// This file is the train-scorer verb's pure substrate (ADR-0049, #300): the committed
// Extract Gold Set reduced to what a fit reads, the logistic fit itself, the Score
// Vocabulary selection, the host-grouped cross-validation, and the veto curve
// restricted to the pages the Positive Evidence rung accepts. It reads and computes;
// it writes nothing, and it touches no network and no model.
//
// Everything here is deterministic by construction, because the artifact it feeds must
// regenerate byte for byte on a machine that is not the one that committed it: rows are
// sorted before the first sum, signal names are indexed through a sorted slice rather
// than ranged over as a map, folds come from a fixed-seed hash, the accumulation is
// single-threaded, and every weight is rounded to a fixed precision BEFORE any discrete
// decision reads it.
package main

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/cespare/xxhash/v2"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

const (
	// fitIterations and fitLearnRate drive full-batch gradient descent. Full batch
	// rather than stochastic descent removes the example-shuffling seed entirely,
	// which is one fewer thing that has to be reproducible, and the cost is trivial:
	// 442 rows carrying a few hundred Score Signals each.
	fitIterations = 500
	fitLearnRate  = 0.5
	// fitL2 shrinks the weights and never the intercept. A word seen in a handful of
	// rows would otherwise be free to take an unbounded weight, which is exactly the
	// per-employer boilerplate ADR-0049 refuses to ship.
	fitL2 = 1e-4
	// hashedBits is the probe's 2^14, used ONLY for the untruncated reference the
	// vocabulary-size curve is compared against. Nothing hashed is ever emitted.
	hashedBits = 14
	// hashedSignalPrefix names a hashed bucket. It cannot collide with a Score Signal
	// name because a bucket's suffix is digits and the three real prefixes are
	// "sig:", "title:" and "body:".
	hashedSignalPrefix = "hash:"
)

// structuralPrefix, titlePrefix and bodyPrefix mirror the three namespaces Signals
// emits. They are duplicated here as literals rather than exported from pagegate
// because they are a WIRE format between the two -- the fitted table's keys -- and a
// test asserts the trainer and the gate agree about them.
const (
	structuralPrefix = "sig:"
	titlePrefix      = "title:"
	bodyPrefix       = "body:"
)

// scorerSample is one Extract Gold Set row reduced to what the fit reads.
type scorerSample struct {
	URL string
	// Host is the row's hostname: the cross-validation grouping key, because the set
	// holds far more rows than hosts.
	Host  string
	Label bench.ExtractLabel
	// Signals are pagegate.Signals' output for the row, computed ONCE. The trainer
	// never derives a Score Signal of its own: sharing that function with the gate is
	// what makes the shipped rule and the measured rule the same rule (ADR-0049).
	Signals []string
	// Accepted marks the rows the Learned Veto will actually judge: the pages rung 8
	// admits, minus the ATS exemption, which returns at rung 2 and is never scored.
	Accepted bool
}

// sampleCensus is what reading the committed Extract Gold Set found, in the
// vocabulary ADR-0049's own table uses. Every count here is over the file as
// committed, so the report can state the population it restricted itself to instead
// of asserting one.
type sampleCensus struct {
	Rows int `json:"rows"`
	// Skipped counts rows carrying no valid label; Ambiguous counts the rows a review
	// could not settle. Both are excluded from the fit exactly as bench.ScoreExtract
	// excludes them, so the trainer and the scorecard describe one population.
	Skipped   int `json:"skipped_unlabelled"`
	Ambiguous int `json:"ambiguous_set_aside"`
	// Accepts is every page the Positive Evidence rung admits, ambiguous rows
	// INCLUDED -- ADR-0049's table counts 180, of which three carry no scorable label.
	Accepts int `json:"rung8_accepts"`
	// ATSExempt counts rows of any label whose URL is a recognized ATS posting. Those
	// accept at rung 2, before a veto could ever run, and ADR-0049's claim that the
	// whole extract bill is rung 8's rests on this being zero.
	ATSExempt int `json:"ats_exempt_accepts"`
}

// scorerSamples reads the committed Extract Gold Set and reduces every SCORABLE row
// to a sample, sorted by URL. The sort is load-bearing: the fit sums over rows, and a
// float sum taken in a different order is a different number in its last bits.
//
// The URL therefore has to be a TOTAL key, and this is where that is enforced: a second
// row for a URL already in the file is refused outright. It would otherwise be a
// determinism hazard nothing else catches -- two rows comparing equal leave their order
// to the sort's internals, and a Go toolchain upgrade could then move a weight and turn
// the regenerability guard red on one machine only -- and it would quietly halve a
// row's out-of-fold reading too, since crossValidate keys its scores by URL. A
// duplicate is also a Gold Set defect in its own right: two labels for one page.
//
// The Positive Evidence rung is replayed through boundaryCandidateConfig, which sets
// RequirePositiveEvidence EXPLICITLY for the same reason the boundary draw does: the
// population the Learned Veto judges must read the same before and after a default
// flip.
func scorerSamples(path string) ([]scorerSample, sampleCensus, error) {
	rows, err := readGoldSet(path)
	if err != nil {
		return nil, sampleCensus{}, err
	}
	cfg := boundaryCandidateConfig()
	census := sampleCensus{Rows: len(rows)}
	samples := make([]scorerSample, 0, len(rows))
	seen := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if _, dup := seen[row.URL]; dup {
			return nil, sampleCensus{}, fmt.Errorf("gold set %q: %q appears more than once; one page carries one label, and a repeated URL would leave the fit's row order to the sort's internals", path, row.URL)
		}
		seen[row.URL] = struct{}{}
		u, err := crawler.NewURL(row.URL)
		if err != nil {
			return nil, sampleCensus{}, fmt.Errorf("gold set %q: url %q: %w", path, row.URL, err)
		}
		exempt := catalog.Classify(u) == catalog.RoleJobListing
		if exempt {
			census.ATSExempt++
		}
		extract, rung := pagegate.ExtractDecision(u, &row.Content, cfg)
		accepted := extract && rung == pagegate.RungNone && !exempt
		if accepted {
			census.Accepts++
		}
		switch {
		case !row.Label.Valid():
			census.Skipped++
			continue
		case !row.Label.Scored():
			census.Ambiguous++
			continue
		}
		samples = append(samples, scorerSample{
			URL:      row.URL,
			Host:     u.Hostname,
			Label:    row.Label,
			Signals:  pagegate.Signals(u, &row.Content),
			Accepted: accepted,
		})
	}
	// Stable, so the order is a function of the file even if the URL key ever stops
	// being total. The duplicate check above is what makes it total today; the stable
	// sort is what keeps a lapse from becoming a silent, architecture-dependent weight.
	slices.SortStableFunc(samples, func(a, b scorerSample) int { return strings.Compare(a.URL, b.URL) })
	return samples, census, nil
}

// hostWords returns the words of one host, tokenized EXACTLY as the Score Vocabulary
// tokenizes page text -- by handing the host to pagegate.Signals as a page body and
// reading the words back out. Re-implementing the split here would let the exclusion
// and the thing it excludes from disagree about what a word is, which is the whole
// failure a shared Signals() exists to prevent.
func hostWords(host string) []string {
	words := []string{}
	for _, sig := range pagegate.Signals(crawler.URL{}, &crawler.Content{MainContent: host}) {
		if word, ok := strings.CutPrefix(sig, bodyPrefix); ok {
			words = append(words, word)
		}
	}
	return words
}

// hostTokens is every word of every host in the FIT POPULATION -- the scorable rows,
// which is what scorerSamples returns. It is the leakage exclusion: 442 rows sit on
// 357 hosts, so a blind fit will learn one sampled employer's name and
// cross-validation will reward it (ADR-0049). The words it removes are real costs --
// "jobs", "career", "karriere", "berlin" are all host tokens somewhere in the set --
// and that is the price of the guard.
//
// The fit population, and not every row of the file, is the RIGHT scope rather than a
// convenient one: the hazard is a fit learning a host it was trained on and being
// rewarded for it on that host's other rows, and a host carried only by unlabelled or
// `ambiguous` rows is in neither half -- scorerSamples drops those rows before they
// reach a fit or a fold, so nothing of that host is learnable or rewardable. Eleven of
// the committed set's hosts are in exactly that position today. Settling one of their
// labels moves its rows into the fit and its words into this ban list in the same
// regeneration, which is why the scope needs no separate upkeep. What it does mean is
// that the artifact and ADR-0049 must claim the fit population, not the whole file.
func hostTokens(samples []scorerSample) map[string]struct{} {
	tokens := map[string]struct{}{}
	for _, s := range samples {
		for _, word := range hostWords(s.Host) {
			tokens[word] = struct{}{}
		}
	}
	return tokens
}

// hostCount is how many distinct hosts the samples sit on. It is the number that
// makes the leakage guard necessary rather than decorative, so the artifact states it.
func hostCount(samples []scorerSample) int {
	hosts := map[string]struct{}{}
	for _, s := range samples {
		hosts[s.Host] = struct{}{}
	}
	return len(hosts)
}

// scorerModel is a fitted Posting Score in exactly the form the artifact ships it:
// weights keyed by Score Signal name, plus the intercept. Both are already rounded to
// weightPrecision, so the model a decision is taken against is the model that ships.
type scorerModel struct {
	Weights   map[string]float64
	Intercept float64
}

// score applies the model to one page's Score Signals. It duplicates pagegate.Score's
// ARITHMETIC -- not its Score Signals, which are shared -- so it must sum in the order
// Signals returns, starting from the intercept, exactly as Score does. The end-to-end
// agreement is held by TestCommittedThresholdLosesNoDetailRowThePositiveEvidenceRungAccepts,
// which re-derives the operating point through pagegate.Score and the compiled table.
func (m scorerModel) score(signals []string) float64 {
	z := m.Intercept
	for _, signal := range signals {
		z += m.Weights[signal]
	}
	return logistic(z)
}

// vocabularyWords is the model's Score Vocabulary: its title: and body: entries, sorted.
func (m scorerModel) vocabularyWords() []string {
	words := []string{}
	for name := range m.Weights {
		if isVocabularySignal(name) {
			words = append(words, name)
		}
	}
	slices.Sort(words)
	return words
}

// logistic squashes the weighted sum into [0,1], the space VetoThreshold is expressed
// in. It is the same form pagegate.Score applies; the trainer must apply it too,
// because the cut it chooses is a value in that space.
func logistic(z float64) float64 { return 1 / (1 + math.Exp(-z)) }

// isVocabularySignal reports whether name is a Score Vocabulary entry rather than one
// of the enumerated Structural Score Signals. Only the vocabulary is ever truncated:
// the ~17 structural names are argued and few, and always carry a weight if the fit
// gives them one.
func isVocabularySignal(name string) bool {
	return strings.HasPrefix(name, titlePrefix) || strings.HasPrefix(name, bodyPrefix)
}

// vocabularyWordOf strips a Score Vocabulary entry's namespace prefix, so the same
// word is excluded in the Title and in the body alike.
func vocabularyWordOf(name string) (string, bool) {
	if word, ok := strings.CutPrefix(name, titlePrefix); ok {
		return word, true
	}
	return strings.CutPrefix(name, bodyPrefix)
}

// fitOptions are the two knobs the artifact depends on. They are flag-backed because
// a human sets the Score Vocabulary size from the curve the run reports; letting the
// run choose it would make the artifact depend on the cross-validation and stop it
// being a hermetic function of (Gold Set, trainer code, flag defaults).
type fitOptions struct {
	// Vocabulary is how many title:/body: entries the fit keeps; 0 keeps every
	// admissible candidate.
	Vocabulary int
	// MinDF is the smallest document frequency a candidate word may have. A word seen
	// in four of 442 rows cannot be estimated, and every slot it takes is one the
	// size curve has to pay for.
	MinDF int
}

// candidateCensus is what the admission step removed and why, so the cost of the
// leakage guard is on the record rather than assumed.
//
// Every count here is over signal NAMES, not over distinct words: `title:apply` and
// `body:apply` are two entries, because the two namespaces are two signals the fit
// weighs separately. A reader comparing these numbers with the leakage guard's own log
// line, which counts distinct host WORDS, will find two different figures for what
// sounds like one thing -- so the report prints both side by side.
type candidateCensus struct {
	// Candidates is every distinct Score Vocabulary name the fit population carries.
	Candidates int `json:"candidates"`
	// HostWords is how many of those NAMES carry a word of some host in the fit
	// population.
	HostWords int `json:"dropped_host_words"`
	// BelowMinDF is how many of the survivors are too rare to estimate.
	BelowMinDF int `json:"dropped_below_min_df"`
	// Structural and Words are what remains, split by namespace.
	Structural int `json:"structural_signals"`
	Words      int `json:"admissible_words"`
}

// admissibleSignals is every Score Signal the fit may weigh, sorted, plus the census
// of what admission removed. Structural signals are never excluded and never counted
// against the vocabulary size: truncation is the Score Vocabulary's problem alone.
func admissibleSignals(samples []scorerSample, exclude map[string]struct{}, minDF int) ([]string, candidateCensus) {
	structural := map[string]struct{}{}
	frequency := map[string]int{}
	for _, s := range samples {
		for _, sig := range s.Signals {
			if !isVocabularySignal(sig) {
				structural[sig] = struct{}{}
				continue
			}
			frequency[sig]++
		}
	}

	census := candidateCensus{Candidates: len(frequency), Structural: len(structural)}
	names := make([]string, 0, len(structural)+len(frequency))
	for name := range structural {
		names = append(names, name)
	}
	for name, df := range frequency {
		word, _ := vocabularyWordOf(name)
		if _, banned := exclude[word]; banned {
			census.HostWords++
			continue
		}
		if df < minDF {
			census.BelowMinDF++
			continue
		}
		names = append(names, name)
		census.Words++
	}
	slices.Sort(names)
	return names, census
}

// fitScorer fits the Posting Score over samples, admitting no word in exclude.
//
// Two stages: a fit over every admissible candidate, then a refit over the structural
// signals plus the heaviest opts.Vocabulary words. The refit is what makes the shipped
// weights the weights OF the shipped Score Vocabulary rather than leftovers of a wider
// fit that also saw the words that were dropped.
func fitScorer(samples []scorerSample, exclude map[string]struct{}, opts fitOptions) scorerModel {
	// The row order is canonicalized HERE rather than trusted from the caller: the
	// gradient sums over rows, a float sum taken in a different order differs in its
	// last bits, and the artifact has to regenerate byte for byte from whatever slice
	// a caller happens to hand over. Stable, for the reason scorerSamples' sort is:
	// scorerSamples refuses a duplicate URL, so the key is total, and a stable sort is
	// what keeps that from being the only thing standing between a lapse and a weight
	// that moves with the toolchain.
	ordered := make([]scorerSample, len(samples))
	copy(ordered, samples)
	slices.SortStableFunc(ordered, func(a, b scorerSample) int { return strings.Compare(a.URL, b.URL) })
	samples = ordered

	admissible, _ := admissibleSignals(samples, exclude, opts.MinDF)
	m := fitOver(samples, admissible)
	if opts.Vocabulary > 0 {
		if kept := selectVocabulary(admissible, m, opts.Vocabulary); len(kept) < len(admissible) {
			m = fitOver(samples, kept)
		}
	}
	return dropZeroWeights(m)
}

// selectVocabulary keeps the heaviest opts.Vocabulary Score Vocabulary entries and
// every structural signal. The ranking reads the ROUNDED magnitude first and the name
// second, so a tie at 1e-6 is broken by the name rather than by the last bits of a
// float that a different architecture may compute differently.
func selectVocabulary(admissible []string, m scorerModel, size int) []string {
	kept := []string{}
	words := []string{}
	for _, name := range admissible {
		if isVocabularySignal(name) {
			words = append(words, name)
			continue
		}
		kept = append(kept, name)
	}
	slices.SortFunc(words, func(a, b string) int {
		if c := cmpFloat(math.Abs(m.Weights[b]), math.Abs(m.Weights[a])); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	kept = append(kept, words[:min(size, len(words))]...)
	slices.Sort(kept)
	return kept
}

// cmpFloat orders two already-rounded weights. NaN cannot arise here -- the fit's
// only transcendental is the logistic, which is bounded -- so a plain comparison is
// the whole ordering.
func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// fitOver fits over exactly the named signals and returns the ROUNDED model. Names
// must be sorted: they become the column order of the gradient, and a different order
// is a different float sum.
func fitOver(samples []scorerSample, names []string) scorerModel {
	index := make(map[string]int32, len(names))
	for i, name := range names {
		index[name] = int32(i)
	}
	rows := make([][]int32, len(samples))
	positive := make([]bool, len(samples))
	for i, s := range samples {
		row := make([]int32, 0, len(s.Signals))
		for _, sig := range s.Signals {
			if j, ok := index[sig]; ok {
				row = append(row, j)
			}
		}
		rows[i] = row
		positive[i] = s.Label.Positive()
	}

	w, intercept := fitLogistic(rows, positive, len(names))
	weights := make(map[string]float64, len(names))
	for i, name := range names {
		weights[name] = roundWeight(w[i])
	}
	return scorerModel{Weights: weights, Intercept: roundWeight(intercept)}
}

// fitLogistic runs full-batch gradient descent over rows encoded as indices into a
// signal table of width n, and returns the raw weights and intercept.
//
// Every Score Signal is a presence indicator, so the two hot loops are pure ADDITION
// and carry no product at all. The only products are the L2 term and the learning-rate
// step, and each is wrapped in an explicit float64 conversion: Go permits a
// floating-point multiply and add to be fused into one rounding, arm64 takes that
// where amd64 typically does not, and an artifact that differs by architecture cannot
// be guarded by byte-for-byte regeneration.
func fitLogistic(rows [][]int32, positive []bool, n int) ([]float64, float64) {
	w := make([]float64, n)
	intercept := 0.0
	if len(rows) == 0 {
		return w, intercept
	}
	rowCount := float64(len(rows))
	grad := make([]float64, n)
	for range fitIterations {
		clear(grad)
		gradIntercept := 0.0
		for i, row := range rows {
			z := intercept
			for _, j := range row {
				z += w[j]
			}
			residual := logistic(z)
			if positive[i] {
				residual -= 1
			}
			gradIntercept += residual
			for _, j := range row {
				grad[j] += residual
			}
		}
		intercept -= float64(fitLearnRate * (gradIntercept / rowCount))
		for j := range w {
			g := grad[j]/rowCount + float64(fitL2*w[j])
			w[j] -= float64(fitLearnRate * g)
		}
	}
	return w, intercept
}

// meanLogLoss is the fit's convergence read: the mean negative log-likelihood of the
// rounded model over the rows it was fitted on. It moves no decision -- it is printed
// so a run that failed to converge is visible rather than silently shipped.
func meanLogLoss(samples []scorerSample, m scorerModel) float64 {
	if len(samples) == 0 {
		return 0
	}
	total := 0.0
	for _, s := range samples {
		p := m.score(s.Signals)
		// Clamp away from the asymptotes: a saturated row would otherwise report an
		// infinite loss and hide every other row's contribution.
		p = math.Min(math.Max(p, 1e-12), 1-1e-12)
		if s.Label.Positive() {
			total -= math.Log(p)
			continue
		}
		total -= math.Log(1 - p)
	}
	return total / float64(len(samples))
}

// dropZeroWeights removes the entries that rounded to nothing. They contribute
// nothing to a verdict and they bloat a table whose whole purpose is to be read.
func dropZeroWeights(m scorerModel) scorerModel {
	kept := make(map[string]float64, len(m.Weights))
	for name, w := range m.Weights {
		if w == 0 {
			continue
		}
		kept[name] = w
	}
	return scorerModel{Weights: kept, Intercept: m.Intercept}
}

// hashedSample folds a sample's Score Vocabulary names into 1<<hashedBits buckets and
// leaves its structural signals explicit. It exists ONLY to measure what a readable
// list costs against the probe's untruncated hashed form (ADR-0049); nothing hashed is
// ever emitted.
//
// A bucket name carries neither the "title:" nor the "body:" prefix, so downstream it
// reads as a structural signal: the min-df floor and the vocabulary truncation both
// skip it. The hashed arm is therefore fitted over EVERY non-host-word candidate, the
// too-rare ones included, and is a strictly more generous reference than the explicit
// ladder rather than the same candidates in a different representation. That is a
// property of the reference, not a defect in it -- sizeCurve says what it means for
// reading the gap.
//
// The leakage exclusion is applied BEFORE the hash, and has to be: a hashed bucket no
// longer carries the word that named it, so an arm that hashed first would quietly buy
// its advantage from the host boilerplate the explicit arm is forbidden to learn.
func hashedSample(s scorerSample, exclude map[string]struct{}) scorerSample {
	seen := map[string]struct{}{}
	hashed := make([]string, 0, len(s.Signals))
	for _, sig := range s.Signals {
		name := sig
		if word, ok := vocabularyWordOf(sig); ok {
			if _, banned := exclude[word]; banned {
				continue
			}
			name = hashedSignalPrefix + strconv.FormatUint(xxhash.Sum64String(sig)%(1<<hashedBits), 10)
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		hashed = append(hashed, name)
	}
	s.Signals = hashed
	return s
}

// hashedSamples folds every sample. xxhash is the repo's existing hash (ADR-0027) and
// is byte-identical on every platform, so the reference arm is as reproducible as the
// arm it judges.
func hashedSamples(samples []scorerSample, exclude map[string]struct{}) []scorerSample {
	out := make([]scorerSample, 0, len(samples))
	for _, s := range samples {
		out = append(out, hashedSample(s, exclude))
	}
	return out
}

// foldOf assigns a HOST to a cross-validation fold by a fixed-seed hash. Grouping by
// host rather than by row is the point: the committed set holds 457 rows across far
// fewer hosts, and a host split across folds lets boilerplate learned on one of its
// pages be rewarded on another (ADR-0049).
//
// seededHash is the goldset tooling's own sha256 key, so the assignment is stable on
// every machine and there is no other source of randomness in the run.
func foldOf(seed, host string, folds int) int {
	n, err := strconv.ParseUint(seededHash(seed, host)[:8], 16, 64)
	if err != nil {
		// Unreachable: seededHash returns hex. Falling back to fold 0 keeps the
		// function total rather than making every caller handle an impossible error.
		return 0
	}
	return int(n % uint64(folds))
}

// crossValidate returns each sample's OUT-OF-FOLD Posting Score, keyed by URL. The
// Score Vocabulary is reselected inside every fold's training half: selecting once
// over the whole set and then cross-validating measures a model that has already seen
// the held-out rows, which is the classic way to make a curve flatter itself.
//
// exclude is the host-token set of the whole FIT POPULATION, held-out hosts included.
// That is deliberate and conservative: the exclusion only ever REMOVES information from
// the training half, and it is the same exclusion the shipped artifact carries.
//
// It also returns how many samples it could NOT score. A fold whose TRAINING half is
// empty -- every host in the population having hashed to that one fold -- is skipped,
// and its held rows then appear in no fold's output at all. Every caller reads the
// result as scores[s.URL], where a missing key yields 0.0: a legitimate Posting Score
// and the lowest one possible, so an unscored row sorts to the bottom of the
// out-of-fold accept ladder and is counted among the first things any cut vetoes. A
// ladder built that way looks measured and is not, so the count is returned rather than
// left for a reader to notice.
func crossValidate(samples []scorerSample, exclude map[string]struct{}, opts fitOptions, seed string, folds int) (map[string]float64, int) {
	scores := make(map[string]float64, len(samples))
	for fold := range folds {
		train := []scorerSample{}
		held := []scorerSample{}
		for _, s := range samples {
			if foldOf(seed, s.Host, folds) == fold {
				held = append(held, s)
				continue
			}
			train = append(train, s)
		}
		if len(held) == 0 || len(train) == 0 {
			continue
		}
		m := fitScorer(train, exclude, opts)
		for _, s := range held {
			scores[s.URL] = m.score(s.Signals)
		}
	}
	unscored := 0
	for _, s := range samples {
		if _, ok := scores[s.URL]; !ok {
			unscored++
		}
	}
	return scores, unscored
}

// foldSizes reports how many samples each fold holds, so a report can show that the
// host grouping did not collapse the split into one fold.
func foldSizes(samples []scorerSample, seed string, folds int) []int {
	sizes := make([]int, folds)
	for _, s := range samples {
		sizes[foldOf(seed, s.Host, folds)]++
	}
	return sizes
}

// scoredAccept is one row of the population the Learned Veto judges: a page the
// Positive Evidence rung accepts today, carrying a scorable label and a Posting Score.
type scoredAccept struct {
	URL   string
	Label bench.ExtractLabel
	Score float64
}

// scoredAccepts projects the accepted samples onto the population every reported
// figure is restricted to, scoring each with score. Ambiguous rows never reach here --
// scorerSamples already set them aside -- so the count is the 177 scorable accepts
// bench.ScoreExtract would fold, not the 180 pages rung 8 admits.
//
// The result is sorted by score and then by URL, so a tie at the cut is broken by name
// rather than by map order.
func scoredAccepts(samples []scorerSample, score func(scorerSample) float64) []scoredAccept {
	accepts := []scoredAccept{}
	for _, s := range samples {
		if !s.Accepted {
			continue
		}
		accepts = append(accepts, scoredAccept{URL: s.URL, Label: s.Label, Score: score(s)})
	}
	slices.SortFunc(accepts, func(a, b scoredAccept) int {
		if c := cmpFloat(a.Score, b.Score); c != 0 {
			return c
		}
		return strings.Compare(a.URL, b.URL)
	})
	return accepts
}

// vetoPoint is one depth of the Learned Veto's curve, restricted to the rows the
// Positive Evidence rung accepts -- the population it will actually judge, and where
// 100% of the extract bill is spent (ADR-0049).
type vetoPoint struct {
	// Depth is the share of the accept set the cut was ASKED for; Vetoed is what ties
	// realized, which is why the two are reported side by side rather than derived
	// from one another.
	Depth           float64 `json:"depth"`
	Threshold       float64 `json:"threshold"`
	Vetoed          int     `json:"vetoed"`
	VetoShare       float64 `json:"veto_share"`
	DetailLost      int     `json:"detail_lost"`
	HubIndexRemoved int     `json:"hub_index_removed"`
	ResidueRemoved  int     `json:"residue_removed"`
	Survivors       int     `json:"survivors"`
	Precision       float64 `json:"precision"`
}

// vetoDepths is the ladder the within-accepts curve is reported at. It brackets
// ADR-0049's pre-registered 10% floor on both sides, so the reader can see what the
// condition costs one step deeper and one step shallower.
var vetoDepths = []float64{0.05, 0.10, 0.15, 0.20, 0.25, 0.30, 0.40, 0.50}

// vetoCurve measures a cut at each depth over the accept set. accepts must be sorted
// by score ascending, which scoredAccepts guarantees.
func vetoCurve(accepts []scoredAccept, depths []float64) []vetoPoint {
	points := make([]vetoPoint, 0, len(depths))
	for _, depth := range depths {
		points = append(points, vetoAt(accepts, depth))
	}
	return points
}

// vetoAt measures ONE cut. The rung is "score < threshold rejects" (#301), so the
// threshold is the score of the shallowest row the cut LETS THROUGH, and a run of
// equal scores at the boundary survives together rather than being split arbitrarily.
func vetoAt(accepts []scoredAccept, depth float64) vetoPoint {
	point := vetoPoint{Depth: depth}
	if len(accepts) == 0 {
		return point
	}
	k := min(int(math.Round(depth*float64(len(accepts)))), len(accepts)-1)
	point.Threshold = accepts[k].Score
	return measureVeto(accepts, point)
}

// measureVeto counts what point.Threshold removes and what survives it, split by
// label: ADR-0049 reports hub-index and residue removals apart, because they are the
// two kinds of leak the rung exists to shed.
func measureVeto(accepts []scoredAccept, point vetoPoint) vetoPoint {
	survivingDetail := 0
	for _, a := range accepts {
		if a.Score >= point.Threshold {
			point.Survivors++
			if a.Label.Positive() {
				survivingDetail++
			}
			continue
		}
		point.Vetoed++
		switch a.Label {
		case bench.ExtractDetail:
			point.DetailLost++
		case bench.ExtractHubIndex:
			point.HubIndexRemoved++
		case bench.ExtractResidue:
			point.ResidueRemoved++
		}
	}
	if len(accepts) > 0 {
		point.VetoShare = float64(point.Vetoed) / float64(len(accepts))
	}
	if point.Survivors > 0 {
		point.Precision = float64(survivingDetail) / float64(point.Survivors)
	}
	return point
}

// zeroLossThreshold is the deepest cut that loses NONE of the detail rows the accept
// set holds -- the same rows, by name. ADR-0049 pins the operating point here rather
// than at recall parity, which would permit swapping one set of postings for another
// and turn TestExtractGoldSetFalseDropGuard red for every substitution.
//
// It is FLOORED to weightPrecision, which adds a whole ulp of margin on the safe side:
// the veto rejects strictly below the threshold, so the lowest-scoring detail row
// survives with room to spare even if a different machine computes its score one bit
// lower. ok is false when the accept set holds no detail row at all, which is a wiring
// error rather than an operating point.
func zeroLossThreshold(accepts []scoredAccept) (float64, bool) {
	lowest := math.Inf(1)
	found := false
	for _, a := range accepts {
		if !a.Label.Positive() {
			continue
		}
		found = true
		lowest = math.Min(lowest, a.Score)
	}
	if !found {
		return 0, false
	}
	return floorWeight(lowest), true
}

// acceptCensus is the accept set before any veto: the population every within-accepts
// figure is restricted to, and the precision the Learned Veto has to improve on.
type acceptCensus struct {
	Scorable  int     `json:"scorable"`
	Detail    int     `json:"detail"`
	Precision float64 `json:"precision"`
}

// censusOf counts the accept set. Its precision is bench.ScoreExtract's precision
// restricted to rung 8's accepts -- 0.7175 on the committed set, the number ADR-0044
// left on the table.
func censusOf(accepts []scoredAccept) acceptCensus {
	census := acceptCensus{Scorable: len(accepts)}
	for _, a := range accepts {
		if a.Label.Positive() {
			census.Detail++
		}
	}
	if census.Scorable > 0 {
		census.Precision = float64(census.Detail) / float64(census.Scorable)
	}
	return census
}
