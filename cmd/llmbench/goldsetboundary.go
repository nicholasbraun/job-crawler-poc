// This file is the Extract Gold Set's BOUNDARY drawings (ADR-0043 #263, ADR-0049
// #304): the pairs of gate configs whose disagreement DEFINES each Boundary
// Stratum, the streaming replay that computes that disagreement over a capture
// frame, the shared draw both verbs run, and the human confirmation sheet the
// strata's labels are owed. It replays the real Extract Gate and nothing else --
// no network, no model, no crawler behaviour.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// boundaryCensusWeight is every Boundary Stratum row's weight. It is exactly 1
// because the stratum is a CENSUS: every page the drawing's half of the
// disagreement holds is taken, so each row's inclusion probability is 1 and its
// inverse is 1. There is no accept-share correction to apply -- a census describes
// no stream, so there is no population to weight toward -- and the per-drawing
// weight balance therefore holds by construction.
const boundaryCensusWeight = 1.0

// boundaryBaselineConfig is TODAY'S blanket accept: the Extract Gate as it behaved
// before ADR-0044's Positive Evidence rung. RequirePositiveEvidence is set FALSE
// explicitly rather than left at its default so this keeps meaning "the previous
// behaviour" after #264 flips that default on.
//
// LearnedVeto is pinned FALSE for the same reason (ADR-0049): the baseline is the
// pre-ADR-0044 gate, which had no rung 9 at all, and a default flip that quietly armed
// one here would make AuditFalseDrops attribute veto-caused drops to the baseline's
// reject rungs.
func boundaryBaselineConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = false
	cfg.LearnedVeto = false
	return cfg
}

// boundaryCandidateConfig is the tiered Positive Evidence rule as the gate ships it
// TODAY (ADR-0044, internal/pagegate/positive_evidence.go). TRUE is set explicitly
// for the same reason the baseline sets FALSE.
//
// It is no longer the rule that DREW this stratum. The draw was taken against #258's
// rule, and #257 widened that rule to the false-drops the stratum measured, so the
// committed rows are a HISTORICAL disagreement set: 29 of them extract under the rule
// this function now returns. The stratum was kept rather than re-drawn -- re-drawing
// discards the recovered postings, which are the evidence, and owes 188 fresh human
// confirmations -- and TestCommittedBoundaryRecoveryLedger is the ledger of what
// changed. A fresh DRAW (goldset-sample-boundary) still uses this function and still
// gets today's boundary, which is why it reads the shipping rule rather than a pinned
// copy of #258's.
//
// The rule lives HERE, in code, rather than in a -gate-config flag: a flag would let
// a later run silently redefine the boundary, and the drawn rows would then claim a
// boundary nobody could re-derive.
//
// LearnedVeto is pinned FALSE, and that pin is load-bearing beyond the draw (ADR-0049).
// This function is also how the trainer and two guards name "the pages the Positive
// Evidence rung accepts": rung 8's accept population is by definition PRE-veto, since
// the veto only ever prunes it. Left at its default, the day EXTRACT_LEARNED_VETO's
// default flips, scorerSamples would fit its operating point against the veto's own
// survivors, TestCommittedThresholdLosesNoDetailRowThePositiveEvidenceRungAccepts would
// become a tautology, and the subtractive-only guard's OFF arm would be a second copy
// of its ON arm.
func boundaryCandidateConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = true
	cfg.LearnedVeto = false
	return cfg
}

// vetoBaselineConfig is the Extract Gate exactly as it SHIPS today: every reject
// rung, the Positive Evidence rung on, and the Learned Veto off. It is the veto
// depth's denominator -- "of the pages today's gate extracts" -- and both switches
// are pinned explicitly for the reason the ADR-0044 pair pins them: the day
// EXTRACT_LEARNED_VETO's default flips, a baseline left at the default would
// silently become the POST-veto gate, and the depth would collapse to zero while
// still reporting itself as a measurement.
//
// It returns the same struct boundaryCandidateConfig returns today, and it is
// deliberately NOT aliased to it. The two are separate provenance claims that are
// allowed to diverge: boundaryCandidateConfig is frozen as "the rule ADR-0043's
// committed rows were drawn against", while this one must track whatever production
// ships. Add a rung 10 behind a third switch and the first has to pin it off while
// this one must not.
func vetoBaselineConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = true
	cfg.LearnedVeto = false
	return cfg
}

// vetoCandidateConfig is what EXTRACT_LEARNED_VETO=true runs, and what
// `score-capture -gate-config <file holding {"LearnedVeto": true}>` scores: the same
// ladder with rung 9 enforcing at the compiled-in VetoThreshold. The flag takes a
// PATH, never inline JSON: loadGateConfig reads the file. Both switches are pinned
// explicitly, for the reason vetoBaselineConfig states -- this config must keep
// meaning "both rungs on" whatever either default later says.
func vetoCandidateConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = true
	cfg.LearnedVeto = true
	return cfg
}

// boundaryDesign is one boundary census's complete definition: the verb that draws
// it, the stratum its rows are stamped with, the two gate configs whose disagreement
// IS the boundary, and which half of that disagreement the census takes.
//
// Pair and stratum live in ONE value on purpose. A committed row states which pair
// drew it through its stratum and through nothing else, so a row's provenance is read
// off the artifact rather than remembered; binding the two here makes that true by
// construction rather than by two call sites agreeing to agree.
type boundaryDesign struct {
	// Verb is the llmbench verb that draws it, used to prefix its diagnostics.
	Verb string
	// Stratum stamps every row this design draws. No two designs may share one
	// (TestEachBoundaryDesignStampsItsOwnStratum).
	Stratum goldStratum
	// Baseline and Candidate are the pair. They are funcs in THIS FILE rather than a
	// -gate-config flag for the reason boundaryCandidateConfig states: a flag would let
	// a later run silently redefine a boundary that already-committed rows claim, and
	// those rows would then assert a boundary nobody can re-derive.
	Baseline  func() crawler.LLMGateConfig
	Candidate func() crawler.LLMGateConfig
	// ExtractorAcceptsOnly restricts the census to the disagreement's accept half --
	// the pages the LIVE extractor said were one Job Listing. ADR-0043's boundary takes
	// that half because the extractor's accept is its evidence that a dropped page was
	// a real one. The Learned Veto's boundary takes BOTH halves and must: ADR-0049
	// forbids grading this rung against the extractor's verdict at all (precision 0.454
	// against human labels, with its errors concentrated on exactly the pages that
	// decide an operating point), so filtering the drop set by that verdict would
	// import that bias before a human ever reads a row.
	ExtractorAcceptsOnly bool
	// Rule is the one-line statement of what defines this cut, printed in the summary
	// so a run says its own operating point rather than leaving it to be remembered.
	Rule string
	// Floor is a pre-registered condition on the depth, or 0 for a design that has
	// none. It is REPORTED and never moves an exit code, on train-scorer's precedent:
	// merging the tooling is not gated on it, flipping a rung's default is.
	Floor float64
	// Reversal is the diagnostic printed when the candidate config ADDS a page the
	// baseline skips. Each design states its own claim in its own ADR's words.
	Reversal string
}

// positiveEvidenceBoundary is ADR-0043's Boundary Stratum (#263): today's blanket
// accept against the tiered Positive Evidence rule, accept half only.
var positiveEvidenceBoundary = boundaryDesign{
	Verb:                 "goldset-sample-boundary",
	Stratum:              stratumBoundary,
	Baseline:             boundaryBaselineConfig,
	Candidate:            boundaryCandidateConfig,
	ExtractorAcceptsOnly: true,
	Rule:                 "the tiered Positive Evidence rule (ADR-0044, internal/pagegate/positive_evidence.go)",
	Reversal: "the rung is supposed to be purely ADDITIVE, so this stratum's one-sided " +
		"definition no longer describes the boundary",
}

// learnedVetoBoundary is ADR-0049's rollout drawing (#304): the shipping gate against
// the same gate with rung 9 armed, BOTH halves of the disagreement.
var learnedVetoBoundary = boundaryDesign{
	Verb:                 "goldset-sample-veto-boundary",
	Stratum:              stratumVetoBoundary,
	Baseline:             vetoBaselineConfig,
	Candidate:            vetoCandidateConfig,
	ExtractorAcceptsOnly: false, // see boundaryDesign.ExtractorAcceptsOnly, and ADR-0049
	Rule: fmt.Sprintf("Posting Score < %.6f (pagegate.VetoThreshold, ADR-0049) -- compiled in "+
		"beside the weights, and it moves with every refit", pagegate.VetoThreshold),
	Floor: vetoFloor, // ADR-0049's pre-registered 10%, the same constant the trainer reports against
	Reversal: "the Learned Veto is SUBTRACTIVE ONLY (ADR-0049): it can withhold a call, never " +
		"add one, so a page it admits means the rung is not the rule this drawing assumes",
}

// boundaryOutcome is one design's config pair replayed over a capture frame: the
// whole answer, read two ways -- as a DEPTH (a label-free number) and as a DROP SET
// (a census to confirm). Both come from one replay so the number an operator decided
// on and the set they then drew can never be two different computations (ADR-0049).
type boundaryOutcome struct {
	// Frame is how many candidates were replayed.
	Frame int
	// BaselineAccepts is how many of them the design's BASELINE extracts: the pages
	// the pair agrees on plus the pages it disagrees on. It is the depth's
	// denominator. The capture tap sits downstream of the Extract Gate, so on a fresh
	// window it should sit within a few rows of Frame; a large gap means a reject rung
	// moved since the window and the frame is stale.
	BaselineAccepts int
	// DroppedAccepted and DroppedAbstained are the disagreement -- baseline extracts,
	// candidate skips -- split by the LIVE extractor's own verdict. The split is
	// REPORTED for both designs and ACTED ON only by a design that says it takes one
	// half.
	DroppedAccepted  []candidate
	DroppedAbstained []candidate
	// Reversed collects any page the CANDIDATE extracts that the baseline skips: the
	// design's own subtractive claim, returned rather than asserted so a rule that
	// stops being subtractive is REPORTED instead of silently halving the boundary.
	Reversed []string
}

// Drop is the size of the whole disagreement, both verdict halves.
func (o boundaryOutcome) Drop() int { return len(o.DroppedAccepted) + len(o.DroppedAbstained) }

// Depth is the share of the baseline's accepts the candidate withholds -- for
// learnedVetoBoundary, the **Veto Depth**. It is 0 when the baseline accepts
// nothing, which the caller refuses before reading it: a depth with no denominator
// is a wrong frame, not a zero.
func (o boundaryOutcome) Depth() float64 {
	if o.BaselineAccepts == 0 {
		return 0
	}
	return float64(o.Drop()) / float64(o.BaselineAccepts)
}

// Census is the drop set the design actually draws: both verdict halves, or the
// accept half alone. The result never aliases the outcome's own slices.
func (o boundaryOutcome) Census(d boundaryDesign) []candidate {
	out := make([]candidate, 0, o.Drop())
	out = append(out, o.DroppedAccepted...)
	if d.ExtractorAcceptsOnly {
		return out
	}
	return append(out, o.DroppedAbstained...)
}

// replayBoundary re-streams the capture and replays the design's BOTH gate configs
// over each candidate's captured Content, counting what the baseline extracts and
// partitioning the pages the two disagree on by the live extractor's own verdict. It
// decodes one record at a time and keeps only the candidate handle, so peak memory
// stays proportional to the disagreement set rather than to the whole capture.
func replayBoundary(path string, scan captureScan, d boundaryDesign) (boundaryOutcome, error) {
	byLine := map[int]candidate{}
	for _, c := range scan.Candidates {
		byLine[c.Line] = c
	}

	f, err := os.Open(path)
	if err != nil {
		return boundaryOutcome{}, fmt.Errorf("open capture %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	baselineCfg, candidateCfg := d.Baseline(), d.Candidate()
	out := boundaryOutcome{
		Frame:            len(scan.Candidates),
		DroppedAccepted:  []candidate{},
		DroppedAbstained: []candidate{},
		Reversed:         []string{},
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), captureScanBuffer)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		cand, ok := byLine[lineNo]
		if !ok {
			continue
		}
		var rec goldRow
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return boundaryOutcome{}, fmt.Errorf("capture line %d: %w", lineNo, err)
		}
		u, err := crawler.NewURL(rec.URL)
		if err != nil {
			return boundaryOutcome{}, fmt.Errorf("capture line %d: url %q: %w", lineNo, rec.URL, err)
		}
		before := pagegate.ShouldExtract(u, &rec.Content, baselineCfg)
		after := pagegate.ShouldExtract(u, &rec.Content, candidateCfg)
		if before {
			out.BaselineAccepts++
		}
		switch {
		case before == after:
			continue
		case !before && after:
			out.Reversed = append(out.Reversed, rec.URL)
		case cand.Verdict:
			out.DroppedAccepted = append(out.DroppedAccepted, cand)
		default:
			out.DroppedAbstained = append(out.DroppedAbstained, cand)
		}
	}
	if err := sc.Err(); err != nil {
		return boundaryOutcome{}, fmt.Errorf("read capture %q: %w", path, err)
	}
	return out, nil
}

// withStratum rewrites each candidate's stratum to s and orders them by URL. It is
// where censusSelection stamps a drawing's rows, and the only place a boundary row's
// stratum is decided -- which is what makes the stratum a reliable statement about
// which config pair drew the row.
func withStratum(cands []candidate, s goldStratum) []candidate {
	out := make([]candidate, 0, len(cands))
	for _, c := range cands {
		c.Stratum = s
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// censusSelection builds the drawing's selection by hand: a census has no quota to
// apply and no accept share to weight by. It emits ONE CELL PER VERDICT HALF the
// design takes, both at boundaryCensusWeight -- readSelected keys each row's weight
// on {stratum, verdict}, so a design that draws the abstain half without a cell for
// it would write those rows at weight 0 and fail the substrate's weight guard.
func censusSelection(d boundaryDesign, census []candidate) selection {
	byVerdict := map[bool]int{}
	for _, c := range census {
		byVerdict[c.Verdict]++
	}
	verdicts := []bool{true}
	if !d.ExtractorAcceptsOnly {
		verdicts = append(verdicts, false)
	}
	sel := selection{Chosen: withStratum(census, d.Stratum)}
	for _, v := range verdicts {
		sel.Cells = append(sel.Cells, cellResult{
			Key:        cellKey{d.Stratum, v},
			Population: byVerdict[v],
			Sampled:    byVerdict[v],
			Weight:     boundaryCensusWeight,
		})
	}
	return sel
}

// boundaryDrawArgs is one boundary run's inputs, parsed and validated.
type boundaryDrawArgs struct {
	Design  boundaryDesign
	Capture string
	Dir     string
	Since   time.Time
	// Draw appends the census to the Extract Gold Set. False reports the depth and
	// writes nothing -- and never even opens Dir.
	Draw bool
}

// runBoundaryDrawing is the body BOTH boundary verbs run: it scans the capture,
// narrows it to the faithful frame at or after Since, replays the design's pair over
// every candidate in that frame, and either reports the reading or appends the drop
// set to the Extract Gold Set. Like goldset-sample-random the draw is APPEND-ONLY:
// existing labels, provenance and expected extractions pass through untouched.
//
// The exclusion of already-committed URLs happens AFTER the replay, on the census
// alone. A page cannot carry two drawings' weights, so it must not be DRAWN twice --
// but excluding it before the replay would make the depth a number about how much of
// this frame an earlier drawing happened to sample, and the depth is ADR-0049's
// pre-registered go/no-go over the frame as a whole.
//
// Returns the process exit code: 2 on a usage or validation error, 1 on IO, 0
// otherwise. Nothing is written unless every check passes.
func runBoundaryDrawing(a boundaryDrawArgs, w io.Writer) int {
	d := a.Design
	scan, err := scanCapture(a.Capture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench %s: %v\n", d.Verb, err)
		return 1
	}
	framed, outOfFrame := frameSince(scan, a.Since)

	outcome, err := replayBoundary(a.Capture, framed, d)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench %s: %v\n", d.Verb, err)
		return 1
	}
	if len(outcome.Reversed) > 0 {
		fmt.Fprintf(os.Stderr, "llmbench %s: %d pages are extracted by the candidate rule and skipped by the baseline (e.g. %s).\n",
			d.Verb, len(outcome.Reversed), outcome.Reversed[0])
		fmt.Fprintf(os.Stderr, "  %s. Nothing was written.\n", d.Reversal)
		return 2
	}
	if outcome.BaselineAccepts == 0 {
		fmt.Fprintf(os.Stderr, "llmbench %s: the baseline rule extracts nothing in this frame, so the depth has no denominator. Check -capture and -since.\n", d.Verb)
		return 2
	}

	summary := boundarySummary{
		Design:     d,
		Scan:       scan,
		Outcome:    outcome,
		OutOfFrame: outOfFrame,
	}
	// Report mode stops here, before the substrate is even opened. It is the
	// non-destructive default for the veto's drawing because the depth is ADR-0049's
	// pre-registered go/no-go: writing the rows it implies before the operator has read
	// the number would change the fitted weights and owe a human confirmation per row,
	// on a rollout the number may yet say no to. An EMPTY drop set is not an error
	// here -- "the candidate would withhold nothing" is a measurement.
	if !a.Draw {
		printBoundarySummary(w, summary)
		return 0
	}

	// The substrate MUST already exist, for the same reason the random drawing
	// requires one: this stratum extends the committed file, and silently starting a
	// new one would strand every existing label.
	substrate, _ := goldSetPaths(a.Dir)
	existing, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench %s: %v (this stratum extends an existing gold set; run goldset-sample first)\n", d.Verb, err)
		return 1
	}
	committed := map[string]struct{}{}
	for _, row := range existing {
		committed[row.URL] = struct{}{}
	}
	census := outcome.Census(d)
	drawable, alreadyCommitted := withoutURLs(census, committed)
	if len(drawable) == 0 {
		if len(census) == 0 {
			fmt.Fprintf(os.Stderr, "llmbench %s: the two configs disagree on no page this drawing takes; there is no boundary to draw\n", d.Verb)
		} else {
			fmt.Fprintf(os.Stderr, "llmbench %s: every page on the boundary is already in the substrate; nothing left to draw\n", d.Verb)
		}
		return 2
	}

	drawn, err := readSelected(a.Capture, censusSelection(d, drawable))
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench %s: %v\n", d.Verb, err)
		return 1
	}
	if err := validateDrawnBoundaryRows(d, drawn, committed); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench %s: %v\n", d.Verb, err)
		return 2
	}

	merged := append(append([]goldRow{}, existing...), drawn...)
	if err := writeGoldSetFiles(a.Dir, merged); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench %s: %v\n", d.Verb, err)
		return 1
	}

	summary.Drawn, summary.Merged = drawn, merged
	summary.AlreadyCommitted, summary.Substrate, summary.Drew = alreadyCommitted, substrate, true
	printBoundarySummary(w, summary)
	return 0
}

// validateDrawnBoundaryRows refuses a draw that could corrupt the substrate: a row
// already committed (one page cannot carry two drawings' incompatible weights), a
// duplicate within the draw, a row outside the design's own stratum, a weight that
// is not the census weight, or a row that arrived carrying a label. Nothing is
// written until it passes, so a bad draw leaves the committed file exactly as it was.
//
// The live verdict is checked only for a design that says it takes the accept half.
// The Learned Veto's drawing takes both halves and MUST NOT be given this check back:
// ADR-0049 forbids grading that rung against the extractor's own verdict, so a drop
// set filtered by it would import that bias before a human ever reads a row.
func validateDrawnBoundaryRows(d boundaryDesign, drawn []goldRow, committed map[string]struct{}) error {
	seen := map[string]struct{}{}
	for _, row := range drawn {
		if _, dup := committed[row.URL]; dup {
			return fmt.Errorf("drew %q, which the substrate already carries", row.URL)
		}
		if _, dup := seen[row.URL]; dup {
			return fmt.Errorf("drew %q twice", row.URL)
		}
		seen[row.URL] = struct{}{}
		if row.Stratum != d.Stratum {
			return fmt.Errorf("drew %q in stratum %q, want %q", row.URL, row.Stratum, d.Stratum)
		}
		if row.Weight != boundaryCensusWeight {
			return fmt.Errorf("drew %q with weight %g, want the census weight %g", row.URL, row.Weight, boundaryCensusWeight)
		}
		if d.ExtractorAcceptsOnly && !row.Verdict {
			return fmt.Errorf("drew %q, whose live verdict was abstain; this drawing takes the accept half of the disagreement", row.URL)
		}
		if row.Label != "" {
			return fmt.Errorf("drew %q carrying label %q; a fresh draw is unlabelled", row.URL, row.Label)
		}
	}
	return nil
}

// boundarySummary is everything the account of one boundary run reads from. The
// fields below Outcome are filled only by a run that actually drew.
type boundarySummary struct {
	Design boundaryDesign
	// Scan is the capture BEFORE framing, so the report can account for what each
	// narrowing step dropped. The framed count is Outcome.Frame -- one number, read off
	// the replay that actually used it.
	Scan       captureScan
	Outcome    boundaryOutcome
	OutOfFrame int

	Drawn, Merged    []goldRow
	AlreadyCommitted int
	Substrate        string
	Drew             bool
}

// printBoundarySummary writes the account an operator needs to trust a boundary run:
// what the capture held, what each narrowing step dropped, the rule the cut was taken
// at, the depth and its denominator, BOTH halves of the disagreement, and the
// reversed count that proves the candidate subtractive. A run that drew adds the two
// row counts; a run that only reported says so and names the flag that would draw.
//
// There is deliberately NO accept share: it does not apply to a census, and printing
// one would invite somebody to weight this drawing.
func printBoundarySummary(w io.Writer, s boundarySummary) {
	o, d := s.Outcome, s.Design
	half := "both halves"
	if d.ExtractorAcceptsOnly {
		half = "the accept half"
	}
	fmt.Fprintf(w, "extract gold set %s stratum\n", d.Stratum)
	fmt.Fprintf(w, "  capture lines        %d\n", s.Scan.Lines)
	fmt.Fprintf(w, "  duplicate lines      %d (deduped by url, latest ts wins)\n", s.Scan.Duplicates)
	fmt.Fprintf(w, "  dropped oversized    %d (raw line > %d bytes)\n", s.Scan.Oversized, maxCandidateBytes)
	fmt.Fprintf(w, "  dropped bad url      %d\n", s.Scan.BadURL)
	fmt.Fprintf(w, "  dropped out-of-frame %d (superseded parser, or an unparseable ts)\n", s.OutOfFrame)
	fmt.Fprintf(w, "  candidate frame      %d\n", o.Frame)
	fmt.Fprintf(w, "  rule                 %s\n", d.Rule)
	fmt.Fprintf(w, "  gate extracts        %d of %d framed (the depth's denominator: what the baseline rule extracts)\n", o.BaselineAccepts, o.Frame)
	fmt.Fprintf(w, "  drop set             %d (live accept %d / live abstain %d; this drawing takes %s)\n",
		o.Drop(), len(o.DroppedAccepted), len(o.DroppedAbstained), half)
	fmt.Fprintf(w, "  depth                %.4f%s\n", o.Depth(), floorVerdict(d, o))
	if d.Floor > 0 {
		// The pre-registered condition has a second half -- losing none of the rung's
		// detail rows -- which is in-sample and cannot be read off an unlabelled frame.
		// Saying so here is what keeps a MET verdict from being read as the whole
		// condition.
		fmt.Fprintf(w, "                       this is the depth half of the condition only; the zero-detail-loss half is\n")
		fmt.Fprintf(w, "                       in-sample and is re-checked after the refit (step 4 of the rollout)\n")
	}
	fmt.Fprintf(w, "  reversed             %d (pages the CANDIDATE adds; this pair's candidate can only ever subtract, so this must be 0)\n", len(o.Reversed))
	if !s.Drew {
		fmt.Fprintf(w, "  wrote                nothing. Re-run with -draw to append the %d-row census.\n", len(o.Census(d)))
		return
	}
	fmt.Fprintf(w, "  dropped committed    %d (boundary pages the substrate already carries)\n", s.AlreadyCommitted)
	fmt.Fprintf(w, "  drawn rows           %d\n", len(s.Drawn))
	fmt.Fprintf(w, "  drawn weight sum     %.4f (census: every weight is exactly %g)\n", weightSum(s.Drawn), boundaryCensusWeight)
	fmt.Fprintf(w, "  substrate rows       %d (%d existing + %d drawn)\n", len(s.Merged), len(s.Merged)-len(s.Drawn), len(s.Drawn))
	if info, err := os.Stat(s.Substrate); err == nil {
		fmt.Fprintf(w, "  wrote                %s (%d bytes)\n", s.Substrate, info.Size())
	}
}

// floorVerdict renders a design's pre-registered condition on the depth, or "" for a
// design that has none. A NOT-MET verdict prints in red and does NOT move the exit
// code: train-scorer's precedent for vetoFloor -- merging the tooling is not gated on
// the condition, flipping the rung's default is.
func floorVerdict(d boundaryDesign, o boundaryOutcome) string {
	if d.Floor <= 0 {
		return ""
	}
	if o.Depth() < d.Floor {
		return red(fmt.Sprintf("   FLOOR NOT MET (>= %.4f of the baseline's accepts, ADR-0049): "+
			"the honest outcome is an amendment to ADR-0049 and the rung stays off", d.Floor))
	}
	return fmt.Sprintf("   FLOOR MET (>= %.4f of the baseline's accepts, ADR-0049)", d.Floor)
}

// runGoldSetSampleBoundary draws the ADR-0043 BOUNDARY Stratum (#263) and APPENDS
// it to the existing Extract Gold Set: it replays today's blanket accept and the
// tiered Positive Evidence rule over the faithful frame at or after -since and takes
// every page the two disagree on that the LIVE extractor accepted.
//
// It takes NO -gate-config and NO -seed, for the reason boundaryCandidateConfig
// states, and the drawing is a CENSUS of the disagreement's accept half, so there is
// no within-cell order to seed and every weight is exactly 1.
//
// It also has no -draw: this stratum is drawn and FROZEN (ADR-0043's consequence on
// re-drawing, TestCommittedBoundaryRecoveryLedger), so a report-only mode over it
// would measure a boundary nobody is going to take. Returns the process exit code:
// 2 on a usage or validation error, 1 on IO, 0 on success.
func runGoldSetSampleBoundary(args []string) int {
	fs := flag.NewFlagSet("goldset-sample-boundary", flag.ExitOnError)
	capture := fs.String("capture", "", "extract-capture JSONL written by the EXTRACT_CAPTURE_PATH tap (required; gitignored, never committed)")
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set the boundary stratum is appended to")
	since := fs.String("since", "", "RFC3339 cutoff: only capture records at or after it are drawn from (required; excludes windows parsed by a superseded parser)")
	_ = fs.Parse(args)

	if *capture == "" || *since == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-sample-boundary -capture <capture.jsonl> -since <RFC3339> [-dir d]")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-boundary: -since %q: %v\n", *since, err)
		return 2
	}
	return runBoundaryDrawing(boundaryDrawArgs{
		Design:  positiveEvidenceBoundary,
		Capture: *capture,
		Dir:     *dir,
		Since:   cutoff,
		Draw:    true,
	}, os.Stdout)
}

// runGoldSetSampleVetoBoundary is ADR-0049's rollout reading (#304), and the same
// drawing against the Learned Veto's own pair: the shipping gate with rung 9 off
// versus on. By default it only REPORTS the **veto depth** over the capture frame --
// of the pages today's gate extracts, the share the veto would withhold -- which
// needs no labels and is the pre-registered go/no-go for turning the rung on.
//
// -draw appends the pages below the cut as the veto-boundary stratum, BOTH halves of
// the disagreement, because ADR-0049 forbids filtering them by the extractor's own
// verdict. Returns the process exit code: 2 on a usage or validation error, 1 on IO,
// 0 on success.
func runGoldSetSampleVetoBoundary(args []string) int {
	fs := flag.NewFlagSet("goldset-sample-veto-boundary", flag.ExitOnError)
	capture := fs.String("capture", "", "extract-capture JSONL written by the EXTRACT_CAPTURE_PATH tap (required; gitignored, never committed)")
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set the veto-boundary stratum is appended to (read only under -draw)")
	since := fs.String("since", "", "RFC3339 cutoff: only capture records at or after it are drawn from (required; excludes windows parsed by a superseded parser)")
	// The default is deliberately NON-destructive, where the two sibling drawing verbs
	// always write: the depth is ADR-0049's pre-registered go/no-go for the whole
	// rollout and must be read before the set it implies is committed, since those rows
	// change the fitted weights and each one owes a human confirmation.
	draw := fs.Bool("draw", false, "append the drop set to the Extract Gold Set as the veto-boundary stratum; the default reports the veto depth and writes nothing")
	_ = fs.Parse(args)

	if *capture == "" || *since == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-sample-veto-boundary -capture <capture.jsonl> -since <RFC3339> [-dir d] [-draw]")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-veto-boundary: -since %q: %v\n", *since, err)
		return 2
	}
	return runBoundaryDrawing(boundaryDrawArgs{
		Design:  learnedVetoBoundary,
		Capture: *capture,
		Dir:     *dir,
		Since:   cutoff,
		Draw:    *draw,
	}, os.Stdout)
}

// boundaryRowsOf projects the replayed capture rows of the Boundary Stratum onto
// what ScoreExtractBoundary folds, carrying each row's confirmation state.
func boundaryRowsOf(rows []captureRow) []bench.BoundaryRow {
	out := []bench.BoundaryRow{}
	for _, row := range rows {
		if row.Stratum != stratumBoundary {
			continue
		}
		out = append(out, bench.BoundaryRow{ExtractVerdictRow: row.ExtractVerdictRow, Confirmed: row.Confirmed})
	}
	return out
}

const (
	// confirmHeadRunes and confirmMidRunes are the two page-text windows the
	// confirmation sheet shows. They are wider than the labeler's worksheet: the
	// confirmer is settling a label somebody else proposed, and a posting's role body
	// often starts below the first screen of a German page.
	confirmHeadRunes = 2000
	confirmMidRunes  = 1200
	// confirmRowsPerFile is the default chunk size. The confirmation is a human pass
	// over ~190 rows; chunking it is what makes a PARTIAL pass legitimate and visible
	// rather than an abandoned one.
	confirmRowsPerFile = 20
)

// confirmChunk is one rendered chunk of the confirmation sheet: its file name, its
// Markdown body, and the row ids it covers, so the caller can write the matching
// id file the human edits down to what they actually read.
type confirmChunk struct {
	Name string
	Body []byte
	IDs  []string
}

// renderConfirmSheet renders rows as ordered Markdown chunks of at most perFile
// rows each. Ordering is by row id: stable, quotable, and independent of the
// proposed label, so the sheet's order can never group the rows by the answer.
//
// Every JSON-LD-derived field, the live extractor's verdict and the gate's own
// marks are WITHHELD -- the same discipline the labeler's worksheet keeps, for a
// sharper reason. The confirmer answers one question, "is this page one Job
// Listing?", and the number the whole stratum exists to produce is what the gate
// concluded about these very pages; showing them that conclusion would anchor the
// answer the rule is being judged against. labels.tsv still carries the verdict:
// that is the REVIEW surface, read after the label exists.
func renderConfirmSheet(rows []goldRow, perFile int) []confirmChunk {
	if perFile <= 0 {
		perFile = confirmRowsPerFile
	}
	ordered := make([]goldRow, len(rows))
	copy(ordered, rows)
	sort.Slice(ordered, func(i, j int) bool { return rowID(ordered[i].URL) < rowID(ordered[j].URL) })

	chunks := []confirmChunk{}
	total := (len(ordered) + perFile - 1) / perFile
	for i := 0; i < len(ordered); i += perFile {
		end := min(i+perFile, len(ordered))
		index := len(chunks) + 1
		chunk := confirmChunk{Name: fmt.Sprintf("confirm-%02d.md", index)}
		var b strings.Builder
		fmt.Fprintf(&b, "# Boundary Stratum confirmation %d/%d\n\n", index, total)
		// The question and the rubric come from the shared value goldset-ui also
		// renders, so the two review surfaces can never put different definitions to
		// the same human (#286).
		fmt.Fprintf(&b, "One question per row: **%s**\n\n", extractLabelQuestion)
		for _, e := range extractLabelRubric {
			fmt.Fprintf(&b, "- `%s` -- %s\n", e.Label, e.Text)
		}
		b.WriteString("\n")
		b.WriteString("The proposed label is an LLM's. Disagreeing with it is the point of this pass.\n\n")
		for _, row := range ordered[i:end] {
			c := confirmViewOf(row)
			chunk.IDs = append(chunk.IDs, c.ID)
			fmt.Fprintf(&b, "---\n\n## %s\n\n", c.ID)
			fmt.Fprintf(&b, "- url: <%s>\n", c.URL)
			fmt.Fprintf(&b, "- proposed: **%s** -- %s\n", c.Label, orNone(c.Note))
			fmt.Fprintf(&b, "- proposed by: %s\n", orNone(c.ProposedBy))
			fmt.Fprintf(&b, "- page title: %s\n", orNone(c.Title))
			fmt.Fprintf(&b, "- outbound links: %d total, %d same-host, %d job-like\n\n", c.URLsTotal, c.URLsSameHost, c.URLsJoblike)
			fmt.Fprintf(&b, "head:\n\n```\n%s\n```\n\n", orNone(c.Head))
			if c.Mid != "" {
				fmt.Fprintf(&b, "mid:\n\n```\n%s\n```\n\n", c.Mid)
			}
		}
		b.WriteString("---\n\nWhen every row above is read, write the ids you confirm to a file and run:\n\n")
		b.WriteString("```sh\n")
		fmt.Fprintf(&b, "printf '%%s\\n' %s > confirmed-%02d.txt\n", strings.Join(chunk.IDs, " "), index)
		fmt.Fprintf(&b, "go run ./cmd/llmbench goldset-apply -confirmed-by \"<your name>\" -confirm-ids confirmed-%02d.txt\n", index)
		b.WriteString("```\n\n")
		b.WriteString("A row whose label you want CHANGED does not belong in that file: edit its `label` column in\n")
		b.WriteString("`cmd/llmbench/extract-goldset/labels.tsv` first (which retracts any confirmation on it), run\n")
		b.WriteString("`goldset-apply`, then confirm it in the next pass.\n")
		chunk.Body = []byte(b.String())
		chunks = append(chunks, chunk)
	}
	return chunks
}

// confirmView is one row as the confirmer reads it: what the page itself says plus
// the label somebody proposed for it, and nothing the gate derived.
type confirmView struct {
	ID         string
	URL        string
	Label      bench.ExtractLabel
	Note       string
	ProposedBy string
	Title      string
	Head       string
	Mid        string

	URLsTotal    int
	URLsSameHost int
	URLsJoblike  int
}

// confirmViewOf builds a row's confirmation view. It reuses worksheetFor for the
// page text and link counts so the two review surfaces can never disagree about
// what a page says, and re-cuts the text windows wider.
func confirmViewOf(row goldRow) confirmView {
	ws := worksheetFor(row)
	main := []rune(flattenField(row.Content.MainContent))
	mid := ""
	if start := len(main) / 3; start > confirmHeadRunes {
		mid = string(main[start:min(start+confirmMidRunes, len(main))])
	}
	return confirmView{
		ID:           ws.ID,
		URL:          ws.URL,
		Label:        row.Label,
		Note:         row.LabelProvenance.Note,
		ProposedBy:   row.LabelProvenance.ProposedBy,
		Title:        ws.Title,
		Head:         truncateRunes(flattenField(row.Content.MainContent), confirmHeadRunes),
		Mid:          mid,
		URLsTotal:    ws.URLsTotal,
		URLsSameHost: ws.URLsSameHost,
		URLsJoblike:  ws.URLsJoblike,
	}
}

// orNone renders an empty field as an explicit marker, so a confirmer can tell a
// page that said nothing from a sheet that lost the text.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// parseConfirmIDs parses a confirmation id file: one row id per line, blank and
// '#' lines skipped. It is what makes a chunk-by-chunk human pass reviewable --
// the diff shows exactly which rows gained a confirmer.
func parseConfirmIDs(data []byte) (map[string]struct{}, error) {
	ids := map[string]struct{}{}
	for i, line := range strings.Split(string(data), "\n") {
		id := strings.TrimSpace(line)
		if id == "" || strings.HasPrefix(id, "#") {
			continue
		}
		if strings.ContainsAny(id, " \t") {
			return nil, fmt.Errorf("confirm-ids line %d: %q is not a bare row id", i+1, id)
		}
		ids[id] = struct{}{}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("confirm-ids names no row; an empty confirmation is not a confirmation")
	}
	return ids, nil
}
