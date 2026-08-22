// This file is the train-scorer verb (ADR-0049, #300): the only thing that writes
// pagegate's Posting Score weights. It fits them over the committed Extract Gold Set,
// emits the generated Go source the gate compiles in, and reports what a Learned Veto
// built on them would do to the pages the Positive Evidence rung accepts today. No
// network, no model, no Docker.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	// defaultWeightsPath is where pagegate compiles the table in from, relative to the
	// repo root -- the same base every other llmbench default path is relative to.
	defaultWeightsPath = "internal/pagegate/posting_score_weights_gen.go"
	// defaultScoreVocabulary is how many Score Vocabulary words the committed artifact
	// holds. It is a flag DEFAULT a human set from the size curve, never a value the
	// run picks: a run that chose its own size would make the artifact depend on the
	// cross-validation, and it would stop being a hermetic function of (Gold Set,
	// trainer code, flag defaults). Changing it is a visible diff in two places.
	defaultScoreVocabulary = 500
	// defaultMinDocFrequency is the smallest document frequency a candidate word may
	// carry. Below it a word cannot be estimated from a few hundred rows, and the slot
	// it takes is one the size curve has to pay for.
	defaultMinDocFrequency = 5
	// defaultFolds and defaultFoldSeed fix the host-grouped cross-validation. The seed
	// is the run's ONLY source of randomness, and it is committed so the out-of-fold
	// numbers in ADR-0049's amendment can be re-derived.
	defaultFolds    = 5
	defaultFoldSeed = "posting-score-v1"
	// vetoFloor is ADR-0049's pre-registered condition: the Learned Veto is turned on
	// only if it vetoes at least this share of rung-8 accepts while losing none of
	// their detail rows. It is REPORTED and never moves an exit code -- merging the
	// trainer is not gated on it, flipping the rung's default is.
	vetoFloor = 0.10
	// hashedGapAlarm is the precision gap at which ADR-0049 says the explicit,
	// readable word list is revisited in favour of the probe's hashed form.
	hashedGapAlarm = 0.02
	// sizeCurveDepth is the one operating point every curve is compared at: a cut of
	// 10% of the rung-8 accepts, which is the depth the pre-registered condition names.
	// One number, comparable across sizes and against the hashed reference, tied to
	// the decision the run exists to inform.
	sizeCurveDepth = 0.10
	// topWeightsPerSign is how many of the heaviest entries each way the report lists.
	// ADR-0049 asks for a human read of the fitted table; fifty lines is what a person
	// actually reads.
	topWeightsPerSign = 25
)

// vocabularySizes is the ladder the precision-versus-size curve is reported over. The
// trailing 0 is the untruncated explicit list, which is what separates the cost of
// TRUNCATION from the cost of being explicit at all -- the hashed reference measures
// the second.
var vocabularySizes = []int{50, 100, 200, 300, 500, 1000, 0}

// trainWeight is one fitted entry as the report lists it. ADR-0049's answer to a rule
// that cannot explain itself is that a human reads the table; this is the read.
type trainWeight struct {
	Signal string  `json:"signal"`
	Weight float64 `json:"weight"`
}

// trainPopulation is the population the fit ran over and the sub-population every
// reported figure is restricted to.
type trainPopulation struct {
	sampleCensus
	// Scorable and Detail are the fit population: every row with a valid,
	// non-ambiguous label.
	Scorable int `json:"scorable"`
	Detail   int `json:"detail"`
	Hosts    int `json:"hosts"`
}

// trainVocabulary is what admission kept and what the fit did with it.
type trainVocabulary struct {
	candidateCensus
	// DistinctHostWords is the size of the ban list itself, in WORDS. candidateCensus
	// counts the signal names it removed, which is the larger number because a banned
	// word is removed from the Title and body namespaces separately; the leakage guard
	// logs this one. Both are printed so neither can be read as the other.
	DistinctHostWords int `json:"distinct_host_words"`
	// Kept is the Score Vocabulary the artifact ships; Weighted is every entry in it,
	// structural signals included.
	Kept     int `json:"kept_words"`
	Weighted int `json:"weighted_signals"`
	// Intercept and LogLoss are the fit's own read on itself: the score of a page
	// carrying no weighted signal, and the mean negative log-likelihood the descent
	// converged to.
	Intercept  float64 `json:"intercept"`
	LogLoss    float64 `json:"mean_log_loss"`
	Iterations int     `json:"iterations"`
	LearnRate  float64 `json:"learn_rate"`
	L2         float64 `json:"l2"`
}

// trainVetoBlock is the curve that decides whether the Learned Veto is ever switched
// on: what a cut at each depth does to the rows rung 8 accepts today. Nobody has ever
// computed it, and a figure over all scorable rows is a DIFFERENT measurement that
// must not be substituted for it -- which is why no such figure exists in this report.
type trainVetoBlock struct {
	Accepts acceptCensus `json:"accepts"`
	// InSample reads the shipped weights over the rows they were fitted on. It is
	// in-sample by necessity: TestExtractGoldSetFalseDropGuard, the merge gate
	// ADR-0049 pins the threshold to, is in-sample over the same rows.
	InSample []vetoPoint `json:"in_sample"`
	// OutOfFold is the honest generalization read, printed beside it.
	OutOfFold  []vetoPoint `json:"out_of_fold"`
	Folds      int         `json:"folds"`
	HostGroups int         `json:"host_groups"`
	FoldSizes  []int       `json:"fold_sizes"`
	// Unscored is how many rows no fold could hold out, and it must be zero for
	// OutOfFold and SizeCurve to mean anything: an unscored row reads as a Posting
	// Score of 0.0000, the lowest possible, and is counted among the first rows every
	// cut vetoes. printTrainAlarms says so in red rather than letting a fabricated
	// ladder read as a measurement.
	Unscored int `json:"out_of_fold_unscored"`
}

// trainSizePoint is one point of the precision-versus-size curve, measured out of
// fold at sizeCurveDepth. Survivors and DetailLost print beside every rate because at
// n=177 a 0.02 precision difference is about three rows.
type trainSizePoint struct {
	// N is the Score Vocabulary size; 0 is untruncated.
	N int `json:"n"`
	// Form is "explicit" for a readable word list and "hashed" for the probe's
	// 2^14-bucket reference.
	Form       string  `json:"form"`
	Precision  float64 `json:"precision"`
	Survivors  int     `json:"survivors"`
	DetailLost int     `json:"detail_lost"`
}

// trainReport is everything the run measured.
type trainReport struct {
	Source        string          `json:"source"`
	Population    trainPopulation `json:"population"`
	Vocabulary    trainVocabulary `json:"vocabulary"`
	WithinAccepts trainVetoBlock  `json:"within_accepts"`
	// Chosen is the operating point the artifact ships: the deepest cut that loses
	// none of the detail rows the Positive Evidence rung accepts.
	Chosen    vetoPoint `json:"chosen_operating_point"`
	Threshold float64   `json:"veto_threshold"`
	// FloorMet records ADR-0049's pre-registered condition. Descriptive; it moves no
	// exit code.
	FloorMet   bool             `json:"floor_met"`
	SizeCurve  []trainSizePoint `json:"size_curve"`
	HashedGap  float64          `json:"hashed_gap"`
	TopWeights []trainWeight    `json:"top_weights"`
}

// runTrainScorer fits the Posting Score over the committed Extract Gold Set, writes
// the generated weights table pagegate compiles in, and reports what a Learned Veto
// built on it would do to the pages the Positive Evidence rung accepts today
// (ADR-0049). No network, no model.
//
// It is the ONLY thing that may write posting_score_weights_gen.go, and it is in Go
// because it calls the same pagegate.Signals the gate calls -- a trainer that
// tokenized German slightly differently from the gate would ship a rule that is not
// the rule that was measured, silently.
//
// Exit: 2 on a wiring error (nothing written), 1 if the artifact it was about to write
// would lose a detail row the Positive Evidence rung accepts (also nothing written), 0
// otherwise. The pre-registered 10% condition is REPORTED and never moves the exit
// code: merging this is not gated on it, flipping the rung's default is.
func runTrainScorer(args []string) int {
	fs := flag.NewFlagSet("train-scorer", flag.ExitOnError)
	in := fs.String("in", filepath.Join(defaultGoldSetDir, goldSetFile), "committed Extract Gold Set JSONL to fit over; read only, never written")
	out := fs.String("out", defaultWeightsPath, "generated Go source to write pagegate's weights table to")
	vocab := fs.Int("vocab", defaultScoreVocabulary, "Score Vocabulary words the artifact holds; 0 keeps every admissible candidate. Set from the size curve this verb reports, never by the run itself")
	minDF := fs.Int("min-df", defaultMinDocFrequency, "smallest document frequency a candidate word may carry")
	folds := fs.Int("folds", defaultFolds, "host-grouped cross-validation folds")
	seed := fs.String("seed", defaultFoldSeed, "fixed seed the fold assignment hashes with; the run's only source of randomness")
	report := fs.Bool("report", true, "print the measurement report; -report=false fits and writes only, which is what the regenerability guard uses so it costs one fit rather than fifty")
	jsonOut := fs.Bool("json", false, "emit the report as indented JSON instead of the human-readable form; the exit code is unchanged")
	_ = fs.Parse(args)

	// Every knob is validated before anything is read or written, so a mistyped flag
	// can never leave a half-considered artifact behind.
	switch {
	case *in == "" || *out == "":
		fmt.Fprintln(os.Stderr, "usage: llmbench train-scorer [-in goldset.jsonl] [-out weights.go] [-vocab n] [-min-df n] [-folds n] [-seed s] [-report=false] [-json]")
		return 2
	case *vocab < 0:
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: -vocab must be >= 0 (0 keeps every candidate), got %d\n", *vocab)
		return 2
	case *minDF < 1:
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: -min-df must be >= 1, got %d\n", *minDF)
		return 2
	case *folds < 2:
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: -folds must be >= 2, got %d\n", *folds)
		return 2
	case *seed == "":
		fmt.Fprintln(os.Stderr, "llmbench train-scorer: -seed must be set; an empty seed is not a reproducible fold assignment")
		return 2
	}

	samples, census, err := scorerSamples(*in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: %v\n", err)
		return 2
	}
	if len(samples) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: no scorable rows in %s\n", *in)
		return 2
	}

	opts := fitOptions{Vocabulary: *vocab, MinDF: *minDF}
	exclude := hostTokens(samples)
	// Admission runs here as well as inside the fit, and the second pass is worth its
	// cost twice over: it is what lets a min-df floor that admits nothing be refused
	// before a degenerate table is written, and it is the census the report prints so
	// the price of the leakage guard is on the record rather than assumed.
	admissible, candidates := admissibleSignals(samples, exclude, opts.MinDF)
	if len(admissible) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: no admissible Score Signal survived -min-df %d\n", opts.MinDF)
		return 2
	}
	model := fitScorer(samples, exclude, opts)

	accepts := scoredAccepts(samples, func(s scorerSample) float64 { return model.score(s.Signals) })
	threshold, ok := zeroLossThreshold(accepts)
	if !ok {
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: the Positive Evidence rung accepts no detail row in %s; the population was computed wrong\n", *in)
		return 2
	}
	chosen := measureVeto(accepts, vetoPoint{Threshold: threshold})
	// The cut was chosen by the constraint rather than requested at a depth, so its
	// depth IS what it realized.
	chosen.Depth = chosen.VetoShare
	// Unreachable while zeroLossThreshold is what chose the cut, and kept anyway: the
	// one thing this verb may never do is emit an artifact that drops a posting the
	// Positive Evidence rung accepts, and a guard that only fires when something else
	// broke is exactly the guard worth having.
	if chosen.DetailLost > 0 {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"llmbench train-scorer: the chosen cut loses %d detail row(s) the Positive Evidence rung accepts; nothing written",
			chosen.DetailLost)))
		return 1
	}

	artifact := weightsArtifact{Model: model, Threshold: threshold, ScorableRows: len(samples), Hosts: hostCount(samples)}
	src, err := renderWeightsSource(artifact)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: %v\n", err)
		return 2
	}
	// Staged and renamed rather than truncated and rewritten: a half-written weights
	// table is a compile error at best and a silently truncated Score Vocabulary at
	// worst, which is precisely the hazard ADR-0049 refuses to accept.
	if err := atomicWrite(fileWrite{Path: *out, Perm: 0o644, Write: writeBytes(src)}); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench train-scorer: %v\n", err)
		return 2
	}
	if !*report {
		return 0
	}

	r := buildTrainReport(*in, samples, census, candidates, model, accepts, chosen, threshold, exclude, opts, *seed, *folds)
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(r); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench train-scorer: encode json: %v\n", err)
			return 2
		}
	} else {
		printTrainReport(os.Stdout, r)
	}
	printTrainAlarms(r)
	return 0
}

// buildTrainReport measures everything the run reports. It runs AFTER the artifact is
// written, which is how the report is kept incapable of moving a byte of it.
func buildTrainReport(
	source string,
	samples []scorerSample,
	census sampleCensus,
	candidates candidateCensus,
	model scorerModel,
	accepts []scoredAccept,
	chosen vetoPoint,
	threshold float64,
	exclude map[string]struct{},
	opts fitOptions,
	seed string,
	folds int,
) trainReport {
	detail := 0
	for _, s := range samples {
		if s.Label.Positive() {
			detail++
		}
	}

	// Fold membership is a function of (host, seed, folds) alone, so a size that scores
	// every sample here scores every sample at every other point on the size curve too.
	// One coverage reading therefore covers the whole report.
	outOfFold, unscored := crossValidate(samples, exclude, opts, seed, folds)
	heldAccepts := scoredAccepts(samples, func(s scorerSample) float64 { return outOfFold[s.URL] })

	r := trainReport{
		Source: source,
		Population: trainPopulation{
			sampleCensus: census,
			Scorable:     len(samples),
			Detail:       detail,
			Hosts:        hostCount(samples),
		},
		Vocabulary: trainVocabulary{
			candidateCensus:   candidates,
			DistinctHostWords: len(exclude),
			Kept:              len(model.vocabularyWords()),
			Weighted:          len(model.Weights),
			Intercept:         model.Intercept,
			LogLoss:           meanLogLoss(samples, model),
			Iterations:        fitIterations,
			LearnRate:         fitLearnRate,
			L2:                fitL2,
		},
		WithinAccepts: trainVetoBlock{
			Accepts:    censusOf(accepts),
			InSample:   vetoCurve(accepts, vetoDepths),
			OutOfFold:  vetoCurve(heldAccepts, vetoDepths),
			Folds:      folds,
			HostGroups: hostCount(samples),
			FoldSizes:  foldSizes(samples, seed, folds),
			Unscored:   unscored,
		},
		Chosen:    chosen,
		Threshold: threshold,
		FloorMet:  chosen.VetoShare >= vetoFloor && chosen.DetailLost == 0,
	}
	r.SizeCurve, r.HashedGap = sizeCurve(samples, exclude, opts, seed, folds)
	r.TopWeights = topWeights(model)
	return r
}

// sizeCurve measures out-of-fold precision over the surviving accepts at
// sizeCurveDepth for each Score Vocabulary size, and against the probe's untruncated
// hashed form. The gap is what ADR-0049 asks for: what an explicit, readable list
// costs against a hashed bag nobody can read.
func sizeCurve(samples []scorerSample, exclude map[string]struct{}, opts fitOptions, seed string, folds int) ([]trainSizePoint, float64) {
	at := func(rows []scorerSample, ex map[string]struct{}, size int) vetoPoint {
		scores, _ := crossValidate(rows, ex, fitOptions{Vocabulary: size, MinDF: opts.MinDF}, seed, folds)
		return vetoAt(scoredAccepts(rows, func(s scorerSample) float64 { return scores[s.URL] }), sizeCurveDepth)
	}

	// The run's OWN size is measured directly rather than looked up in the ladder. An
	// earlier form read the gap off whichever ladder rung happened to equal -vocab and
	// left it at 0.0 when none did, so `-vocab 250` printed a fabricated "+0.0000" and
	// the pre-registered HASHED GAP alarm could not fire -- a manufactured zero on the
	// one number that governs a documented design reversal.
	chosen := at(samples, exclude, opts.Vocabulary)

	sizes := curveSizes(opts.Vocabulary)
	curve := make([]trainSizePoint, 0, len(sizes)+1)
	for _, size := range sizes {
		p := chosen
		if size != opts.Vocabulary {
			p = at(samples, exclude, size)
		}
		curve = append(curve, trainSizePoint{N: size, Form: "explicit", Precision: p.Precision, Survivors: p.Survivors, DetailLost: p.DetailLost})
	}
	// The hashed arm is the probe's 2^14 bag as the probe ran it, and it is a MORE
	// generous reference than the explicit ladder rather than a like-for-like one: a
	// hashed bucket is not a `title:`/`body:` name, so admissibleSignals reads it as a
	// structural signal and it escapes both the min-df floor and the truncation. The arm
	// therefore fits over every non-host-word candidate, the too-rare ones included,
	// folded into 16,384 buckets. That is why a zero gap reads conservatively: the
	// explicit list is matching a reference that saw strictly more. Only the leakage
	// exclusion is shared, and it must be (see hashedSample).
	h := at(hashedSamples(samples, exclude), nil, 0)
	curve = append(curve, trainSizePoint{N: 0, Form: "hashed", Precision: h.Precision, Survivors: h.Survivors, DetailLost: h.DetailLost})

	return curve, h.Precision - chosen.Precision
}

// curveSizes is the reported ladder with the run's chosen -vocab guaranteed on it, so
// the gap the report prints is a point a reader can also see measured. The trailing 0
// stays last: it is "untruncated", not a size, so a chosen size larger than every rung
// still sorts ahead of it.
func curveSizes(chosen int) []int {
	if slices.Contains(vocabularySizes, chosen) {
		return vocabularySizes
	}
	at := len(vocabularySizes) - 1
	for i, n := range vocabularySizes {
		if n != 0 && n > chosen {
			at = i
			break
		}
	}
	return slices.Insert(slices.Clone(vocabularySizes), at, chosen)
}

// topWeights is the heaviest entries each way. It is the fit's only explanation of
// itself, and the reason ADR-0049 ships an explicit list at all: a reader checks that
// it weighs the words a posting uses, not the words one sampled employer uses.
func topWeights(m scorerModel) []trainWeight {
	all := make([]trainWeight, 0, len(m.Weights))
	for signal, weight := range m.Weights {
		all = append(all, trainWeight{Signal: signal, Weight: weight})
	}
	slices.SortFunc(all, func(a, b trainWeight) int {
		if c := cmpFloat(b.Weight, a.Weight); c != 0 {
			return c
		}
		return strings.Compare(a.Signal, b.Signal)
	})
	head := min(topWeightsPerSign, len(all))
	tail := max(len(all)-topWeightsPerSign, head)
	return append(append([]trainWeight{}, all[:head]...), all[tail:]...)
}

// printTrainReport writes the human-readable measurement. The within-accepts curve
// comes FIRST because it is the report's whole point: it is the only population the
// Learned Veto will judge, and it is where the entire extract bill is spent.
func printTrainReport(w io.Writer, r trainReport) {
	fmt.Fprintln(w, "train-scorer -- fitting the Posting Score over the committed Extract Gold Set (offline: no network, no model)")
	fmt.Fprintf(w, "  source            %s\n", r.Source)

	p := r.Population
	fmt.Fprintln(w, "\npopulation")
	fmt.Fprintf(w, "  rows              %d (skipped unlabelled %d, ambiguous set aside %d)\n", p.Rows, p.Skipped, p.Ambiguous)
	fmt.Fprintf(w, "  scorable          %d   detail %d   hosts %d\n", p.Scorable, p.Detail, p.Hosts)
	a := r.WithinAccepts.Accepts
	fmt.Fprintf(w, "  rung-8 accepts    %d   scorable %d   detail %d   precision %.4f\n", p.Accepts, a.Scorable, a.Detail, a.Precision)
	fmt.Fprintf(w, "  ats-exempt        %d     (rung 2 returns before the veto; ADR-0049)\n", p.ATSExempt)

	v := r.Vocabulary
	fmt.Fprintln(w, "\nscore vocabulary")
	fmt.Fprintf(w, "  candidate entries %d   dropped as a host word %d (%d distinct host words)   dropped below min-df %d\n",
		v.Candidates, v.HostWords, v.DistinctHostWords, v.BelowMinDF)
	fmt.Fprintf(w, "  kept              %d words + %d structural signals = %d weighted entries\n", v.Kept, v.Structural, v.Weighted)
	fmt.Fprintf(w, "  fit               intercept %.6f   mean log-loss %.4f   (full-batch, %d iterations, lr %.2f, l2 %g)\n",
		v.Intercept, v.LogLoss, v.Iterations, v.LearnRate, v.L2)

	b := r.WithinAccepts
	fmt.Fprintln(w, "\nthe Learned Veto restricted to the rows Positive Evidence accepts")
	fmt.Fprintf(w, "  n=%d scorable accepts, %d detail, precision %.4f with no veto\n", a.Scorable, a.Detail, a.Precision)
	fmt.Fprintln(w, "  in-sample (the shipped weights -- in-sample BY NECESSITY: TestExtractGoldSetFalseDropGuard,")
	fmt.Fprintln(w, "  the merge gate ADR-0049 pins the threshold to, reads the same rows the same way)")
	printVetoLadder(w, b.InSample)
	fmt.Fprintf(w, "  out of fold (host-grouped, %d folds over %d host groups, fold sizes %v -- the honest generalization read)\n",
		b.Folds, b.HostGroups, b.FoldSizes)
	printVetoLadder(w, b.OutOfFold)

	c := r.Chosen
	fmt.Fprintf(w, "\nchosen operating point (the deepest cut that loses NONE of the %d detail rows)\n", a.Detail)
	fmt.Fprintf(w, "  VetoThreshold     %.6f\n", r.Threshold)
	fmt.Fprintf(w, "  vetoed            %d of %d (%.2f%%)   detail lost %d\n", c.Vetoed, a.Scorable, 100*c.VetoShare, c.DetailLost)
	fmt.Fprintf(w, "  leaks removed     %d hub-index + %d residue = %d\n", c.HubIndexRemoved, c.ResidueRemoved, c.HubIndexRemoved+c.ResidueRemoved)
	fmt.Fprintf(w, "  precision         %.4f -> %.4f\n", a.Precision, c.Precision)
	fmt.Fprintf(w, "  pre-registered condition (>=%.0f%% of rung-8 accepts, zero detail lost):  %s\n", 100*vetoFloor, metOrNot(r.FloorMet))

	fmt.Fprintf(w, "\nScore Vocabulary size (out-of-fold precision over surviving accepts at a %.0f%% veto depth)\n", 100*sizeCurveDepth)
	fmt.Fprintf(w, "  %8s  %10s  %10s  %11s\n", "n", "precision", "survivors", "detail-lost")
	for _, s := range r.SizeCurve {
		fmt.Fprintf(w, "  %8s  %10.4f  %10d  %11d\n", sizeLabel(s), s.Precision, s.Survivors, s.DetailLost)
	}
	fmt.Fprintf(w, "  gap to the hashed form   %+.4f", r.HashedGap)
	if r.HashedGap > hashedGapAlarm {
		fmt.Fprintf(w, "   [above %.2f: ADR-0049 says revisit the explicit list]", hashedGapAlarm)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "\nheaviest Score Signals (read these: they are the fit's only explanation of itself)")
	for _, tw := range r.TopWeights {
		fmt.Fprintf(w, "  %+.6f  %s\n", tw.Weight, tw.Signal)
	}
}

// printVetoLadder writes one depth ladder. Counts print beside every rate because at
// this population size a 0.02 precision difference is about three rows, and a reader
// must be able to see that.
func printVetoLadder(w io.Writer, points []vetoPoint) {
	fmt.Fprintf(w, "    %6s  %9s  %6s  %7s  %11s  %9s  %7s  %9s  %9s\n",
		"depth", "threshold", "vetoed", "share", "detail-lost", "hub-index", "residue", "survivors", "precision")
	for _, p := range points {
		fmt.Fprintf(w, "    %5.0f%%  %9.6f  %6d  %6.2f%%  %11d  %9d  %7d  %9d  %9.4f\n",
			100*p.Depth, p.Threshold, p.Vetoed, 100*p.VetoShare, p.DetailLost, p.HubIndexRemoved, p.ResidueRemoved, p.Survivors, p.Precision)
	}
}

// printTrainAlarms writes the findings a reader must not scroll past to stderr in red.
// None moves the exit code: the first two are findings about the Gold Set rather than
// failures of the run (ADR-0049 pre-commits to recording an unmet floor as the honest
// outcome), and the third is about the -folds this run was given, which cannot touch
// the artifact -- the fit does not cross-validate.
func printTrainAlarms(r trainReport) {
	if !r.FloorMet {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"FLOOR NOT MET  the cut vetoes %.2f%% of rung-8 accepts, below the pre-registered %.0f%%: %d rows do not support a Learned Veto at this size (ADR-0049)",
			100*r.Chosen.VetoShare, 100*vetoFloor, r.Population.Scorable)))
	}
	if r.HashedGap > hashedGapAlarm {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"HASHED GAP     %+.4f precision above the explicit list, past the %.2f ADR-0049 names as the point where the readable list is revisited",
			r.HashedGap, hashedGapAlarm)))
	}
	if b := r.WithinAccepts; b.Unscored > 0 {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"NO OUT-OF-FOLD SCORE  %d of %d rows sit in a fold with no training half (%d folds over %d host groups), so nothing measured them: they read as 0.0000, the lowest possible Posting Score, and are counted among the first rows every cut vetoes. The out-of-fold ladder and the size curve above are not measurements. The artifact is unaffected -- the fit does not cross-validate.",
			b.Unscored, r.Population.Scorable, b.Folds, b.HostGroups)))
	}
}

// sizeLabel renders a curve point's size column: a count, "all" for the untruncated
// explicit list, or the hashed reference's bucket count.
func sizeLabel(s trainSizePoint) string {
	if s.Form == "hashed" {
		return fmt.Sprintf("2^%d", hashedBits)
	}
	if s.N == 0 {
		return "all"
	}
	return fmt.Sprintf("%d", s.N)
}

// metOrNot renders the pre-registered condition's answer.
func metOrNot(met bool) string {
	if met {
		return "MET"
	}
	return "NOT MET"
}
