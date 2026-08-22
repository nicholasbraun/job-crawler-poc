// This file is llmbench's goldset-refit verb (ADR-0049, #304): the whole of step 4 of
// the repository README's "Turning the Learned Veto on" in one pass -- apply, counts,
// regenerate, verify -- and the verdict on whether what it built still earns the flip.
//
// It exists because the sequence was five manual acts and the last of them, reading the
// pre-registered condition out of the trainer's report, is the one a human skips. That
// is the only one that says whether the artifact just built still deserves to ship.
//
// THE BINDING FACT: this process compiled the PREVIOUS weights in. It writes new ones,
// and pagegate.Score and pagegate.VetoThreshold in this binary stay the old artifact's
// until something rebuilds. That is why the trainer runs IN-PROCESS -- its own fitted
// model is the new artifact and its report is the new artifact's measurement -- and why
// verification SHELLS OUT to `go test`, which recompiles and is therefore the only step
// that can see a hand-picked fixture cross the new VetoThreshold.
//
// Nothing here edits a label. It passes no provenance-stamping flag to goldset-apply,
// never touches extractGoldSetFalseDrops and never writes a label field: a Blind
// Confirmation is goldset-ui's act and a human's (ADR-0048).
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// refitConfig is one refit pass's whole input, so the sequence is separable from flag
// parsing and testable without a toolchain in the loop.
type refitConfig struct {
	Dir, Weights, Counts string
	Verify               bool
	// Suite runs the guard suite. It is a field so a test can drive the sequence
	// without spawning `go test` inside `go test`; the verb wires goTestSuite.
	Suite func(w io.Writer) error
	// Out receives this verb's own transcript. The two verbs it composes --
	// goldset-apply and the trainer -- print to the process streams as they always do,
	// which is what a human wants here: one continuous transcript.
	Out io.Writer
}

// runGoldSetRefit is step 4 of the Learned Veto's rollout as one pass (ADR-0049): it
// applies the confirmed rows through goldset-apply, recomputes and rewrites the counts
// the record determines, re-runs the trainer `go generate ./internal/pagegate` runs,
// then runs the repository's suite over the regenerated tree.
//
// Exit: 2 on a wiring error or a refused rewrite, 1 when the result is not shippable,
// 0 otherwise.
func runGoldSetRefit(args []string) int {
	fs := flag.NewFlagSet("goldset-refit", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set; the record this pass applies, counts and refits over")
	weights := fs.String("weights", defaultWeightsPath, "generated Go source pagegate compiles its Posting Score weights in from; rewritten by the same trainer `go generate ./internal/pagegate` runs (train-scorer calls this path -out)")
	counts := fs.String("counts", defaultCountsPath, "Go source carrying the counts the record determines; every one is recomputed and rewritten in place, and lands in your diff -- which is why they are hard-coded rather than computed at test time")
	verify := fs.Bool("verify", true, "run `go test -count=1 ./...` over the regenerated tree -- the only step that sees the new weights COMPILED, and it needs Docker for the repository suite. -verify=false skips it, and the pass then cannot call the result shippable")
	_ = fs.Parse(args)

	return goldSetRefit(refitConfig{
		Dir:     *dir,
		Weights: *weights,
		Counts:  *counts,
		Verify:  *verify,
		Suite:   goTestSuite,
		Out:     os.Stdout,
	})
}

// goTestSuite runs the repository's whole suite. It is `go test -count=1 ./...` and NOT
// a hand-picked package list, for the reason the runbook gives: a refit can move a
// fixture across VetoThreshold in any package that pins one -- internal/pagegate,
// internal/processor/url_processor and cmd/llmbench today -- and a list is a list that
// goes stale. It needs a Docker daemon, because the repository suite spins Postgres and
// Redis up through testcontainers; -count=1 because a verification a cache can satisfy
// is not one. Not -race: CI owns that, and it doubles the cost for nothing this verb
// asks.
func goTestSuite(w io.Writer) error {
	cmd := exec.Command("go", "test", "-count=1", "./...")
	cmd.Stdout, cmd.Stderr = w, w
	return cmd.Run()
}

// goldSetRefit is the sequence. It stops in exactly four places, and every one of them
// says what it did and did not write:
//
//	phase 0  a wiring error, before anything is read or written  -> 2
//	phase 1  goldset-apply refused; the record was not written   -> 2
//	phase 2  a ratchet would move the way a signature vanishes   -> 2
//	phase 3  the trainer refused; the tree is now inconsistent   -> 1 or 2
//
// Past those it always finishes, and the exit code is the verdict on the result rather
// than a sign the pass aborted.
func goldSetRefit(cfg refitConfig) int {
	fmt.Fprintln(cfg.Out, "goldset-refit -- apply, counts, regenerate, verify over the Extract Gold Set (ADR-0049)")

	// Phase 0. Everything checkable is checked before the first write, on train-scorer's
	// own discipline: this pass writes three things, and a mistyped flag must not be
	// able to leave two of them moved and the third behind.
	counts, code := refitPreflight(cfg)
	if code != 0 {
		return code
	}

	// Phase 1. goldset-apply is the record's only writer, and it runs with -dir and
	// nothing else: a canonicalizing re-merge that folds the human's own labels.tsv
	// edits in and stamps no provenance. It therefore cannot invent a proposer or sign
	// a confirmation.
	fmt.Fprintf(cfg.Out, "\n1/4 apply       goldset-apply -dir %s   (no provenance stamped: a confirmation is goldset-ui's act)\n", cfg.Dir)
	if code := runGoldSetApply([]string{"-dir", cfg.Dir}); code != 0 {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"llmbench goldset-refit: goldset-apply exited %d; it validates in full before it writes, so the record is untouched and nothing else here has run", code)))
		return 2
	}

	// Phase 2. The counts are read off the APPLIED record, which is the state the tests
	// will read them off too.
	snapshot, err := snapshotRecord(cfg.Dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-refit: %v\n", err)
		return 2
	}
	changes := countChanges(snapshot, counts.Lits)
	fmt.Fprintf(cfg.Out, "\n2/4 counts      %s\n", cfg.Counts)
	if code := writeRefitCounts(cfg, counts, changes); code != 0 {
		return code
	}

	// Phase 3. The trainer runs in-process at the flag defaults with only the two paths
	// overridden, so the artifact stays a pure function of (Gold Set, trainer code, flag
	// defaults) -- a refit that chose its own -vocab would emit a file the regenerability
	// guard rejects on the next build.
	fmt.Fprintf(cfg.Out, "\n3/4 regenerate  %s\n", cfg.Weights)
	opts := defaultTrainOptions()
	opts.In, _ = goldSetPaths(cfg.Dir)
	opts.Out, opts.Report = cfg.Weights, true
	report, code := trainScorer(opts)
	if code != 0 {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"llmbench goldset-refit: the trainer refused and wrote NOTHING, so the tree is now inconsistent: the counts in %s moved and %s did not. Fix what it named and re-run this verb; do not commit the counts on their own.",
			cfg.Counts, cfg.Weights)))
		return code
	}
	printTrainReport(cfg.Out, report)
	printTrainAlarms(report)

	// Phase 4. Only a fresh compile can see the weights just written.
	var suiteErr error
	if cfg.Verify {
		fmt.Fprintln(cfg.Out, "\n4/4 verify      go test -count=1 ./...   (needs Docker; the only step that sees the new weights COMPILED)")
		suiteErr = cfg.Suite(cfg.Out)
	} else {
		fmt.Fprintln(cfg.Out, "\n4/4 verify      SKIPPED (-verify=false)")
	}

	outcome := refitVerdict(report, cfg.Verify, suiteErr)
	printRefitVerdict(cfg.Out, report, outcome)
	return outcome.Code
}

// countsFile is the counts source as phase 0 read it: the bytes, and the integer
// literal every derived count is bound to, located in them. The two travel together
// because a splice is only valid against the bytes its offsets were taken from.
type countsFile struct {
	Src  []byte
	Lits map[string]countLiteral
}

// refitPreflight is phase 0: everything this pass can refuse before it has written
// anything. It returns the counts source, because locating every count IS one of the
// checks -- a count the verb could not find is a Gold Set change the diff would not
// show, which is the one thing the hard-coded counts exist to prevent.
func refitPreflight(cfg refitConfig) (countsFile, int) {
	if cfg.Verify {
		if cfg.Suite == nil {
			fmt.Fprintln(os.Stderr, "llmbench goldset-refit: verification was asked for with no suite to run it")
			return countsFile{}, 2
		}
		if _, err := exec.LookPath("go"); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-refit: no `go` on PATH, so the regenerated tree could not be verified: %v\n", err)
			return countsFile{}, 2
		}
	}
	substrate, _ := goldSetPaths(cfg.Dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-refit: %v\n", err)
		return countsFile{}, 2
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench goldset-refit: %s holds no rows; there is no record to refit over\n", substrate)
		return countsFile{}, 2
	}
	if dir := filepath.Dir(cfg.Weights); dir != "" {
		if _, err := os.Stat(dir); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-refit: -weights %q: %v\n", cfg.Weights, err)
			return countsFile{}, 2
		}
	}
	src, lits, err := readCountLiterals(cfg.Counts, derivedCountNames())
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-refit: %v\n", err)
		return countsFile{}, 2
	}
	return countsFile{Src: src, Lits: lits}, 0
}

// writeRefitCounts prints every count the record determines and rewrites the ones that
// moved. Printing all of them, changed or not, is the point: automating the edit is
// fine, making it invisible is not.
//
// It refuses the whole pass -- before the counts file or the weights are written -- when
// a change would move a ratchet the way that means a human signature vanished
// (ADR-0048).
func writeRefitCounts(cfg refitConfig, counts countsFile, changes []countChange) int {
	regressions := []countChange{}
	rewrites := 0
	for _, c := range changes {
		switch {
		case c.regression():
			regressions = append(regressions, c)
			fmt.Fprintf(cfg.Out, "  %30s  %d -> %d   REFUSED: %s\n", c.Name, c.Old, c.New, c.Why)
		case c.moved():
			rewrites++
			note := "ACKNOWLEDGE: a pinned census moved"
			if c.Direction != countPinned {
				note = "confirmations landed"
			}
			fmt.Fprintf(cfg.Out, "  %30s  %d -> %d   %s (%s)\n", c.Name, c.Old, c.New, note, c.Why)
		default:
			fmt.Fprintf(cfg.Out, "  %30s  %d\n", c.Name, c.Old)
		}
	}

	if len(regressions) > 0 {
		names := make([]string, 0, len(regressions))
		for _, c := range regressions {
			names = append(names, c.Name)
		}
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf(
			"llmbench goldset-refit: %v would move the way that means a human signature VANISHED, and a ratchet may only be moved that way by the person who withdraws the confirmation (ADR-0048). "+
				"Nothing was written here: neither %s nor %s. goldset-apply already ran, and it is idempotent and stamped nothing. "+
				"Either a signature was lost -- find it in the diff -- or rows were appended to the stratum, and that rise belongs in the drawing's own commit.",
			names, cfg.Counts, cfg.Weights)))
		return 2
	}

	if rewrites > 0 {
		out, err := rewriteCounts(counts.Src, counts.Lits, changes)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-refit: %v\n", err)
			return 2
		}
		if err := atomicWrite(fileWrite{Path: cfg.Counts, Perm: 0o644, Write: writeBytes(out)}); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-refit: %v\n", err)
			return 2
		}
	}
	// Nothing is written when nothing moved: an untouched file is the honest record of
	// a record that did not change.
	fmt.Fprintf(cfg.Out, "  %d counts checked, %d rewritten. They are in your diff -- commit them with the record.\n", len(changes), rewrites)
	return 0
}

// refitOutcome is the pass's answer: whether the artifact it just built earns the flip,
// and the reasons it does not.
type refitOutcome struct {
	Shippable bool
	Verified  bool
	Reasons   []string
	Code      int
}

// refitVerdict reads ADR-0049's pre-registered condition off the trainer's own report
// and nothing else, because that report is the only place the figures are restricted to
// the population the Learned Veto judges -- the pages the Positive Evidence rung
// accepts. A figure over all scorable rows is a different measurement.
//
// The condition is REPORTED by train-scorer and ENFORCED here, and the split is the
// point: merging the trainer was never gated on the floor, flipping the rung's default
// is, and this verb is the flip's decision procedure (ADR-0049).
//
// With verified false and no reasons the code is 0 and Shippable is still false: the
// word is withheld from a result nothing compiled.
func refitVerdict(r trainReport, verified bool, suiteErr error) refitOutcome {
	o := refitOutcome{Verified: verified}
	if r.Chosen.DetailLost > 0 {
		o.Reasons = append(o.Reasons, fmt.Sprintf(
			"the chosen cut loses %d detail row(s) the Positive Evidence rung accepts; re-run the trainer, never move VetoThreshold by hand", r.Chosen.DetailLost))
	}
	if !r.FloorMet {
		o.Reasons = append(o.Reasons, fmt.Sprintf(
			"the cut vetoes %.2f%% of rung-8 accepts, below the pre-registered %.0f%%: these rows do not support a Learned Veto (ADR-0049). Record the honest outcome as an amendment; do not move the threshold.",
			100*r.Chosen.VetoShare, 100*vetoFloor))
	}
	if suiteErr != nil {
		o.Reasons = append(o.Reasons, fmt.Sprintf(
			"the guard suite is red (%v). Read it before anything else: after a Blind Confirmation pass, RESTANDING entries in extractGoldSetFalseDrops are EXPECTED -- a confirmation re-opens each exception and it must be re-argued, never inherited (ADR-0043). "+
				"A \"pick a weaker fixture\" failure in internal/pagegate, internal/processor/url_processor or cmd/llmbench wants a different page, never a moved threshold.", suiteErr))
	}
	if len(o.Reasons) > 0 {
		o.Code = 1
		return o
	}
	o.Shippable = verified
	return o
}

// printRefitVerdict writes the pass's closing read: the operating point the artifact
// ships, the pre-registered condition, and what the human does next.
func printRefitVerdict(w io.Writer, r trainReport, o refitOutcome) {
	fmt.Fprintln(w, "\nverdict")
	// The "was" figure is the COMPILED threshold, which is honest precisely because
	// this binary still carries the pre-refit artifact.
	fmt.Fprintf(w, "  VetoThreshold          %.6f   (this binary compiled %.6f in; the change takes effect at the next build)\n", r.Threshold, pagegate.VetoThreshold)
	fmt.Fprintf(w, "  rung-8 accepts vetoed  %d of %d (%.2f%%)\n", r.Chosen.Vetoed, r.WithinAccepts.Accepts.Scorable, 100*r.Chosen.VetoShare)
	fmt.Fprintf(w, "  detail rows lost       %d          (the cut is chosen to lose none, confirmed or not)\n", r.Chosen.DetailLost)
	fmt.Fprintf(w, "  pre-registered condition (>=%.0f%% of rung-8 accepts, zero detail lost):  %s\n", 100*vetoFloor, metOrNot(r.FloorMet))
	switch {
	case !o.Verified:
		fmt.Fprintln(w, "  guard suite            NOT RUN")
	case o.Code == 0:
		fmt.Fprintln(w, "  guard suite            PASS")
	default:
		fmt.Fprintln(w, "  guard suite            FAIL")
	}

	for _, reason := range o.Reasons {
		fmt.Fprintln(os.Stderr, red("NOT SHIPPABLE  "+reason))
	}
	switch {
	case o.Shippable:
		fmt.Fprintln(w, "  SHIPPABLE -- this artifact earns the flip (README, \"Turning the Learned Veto on\", step 5)")
	case o.Code == 0:
		fmt.Fprintln(w, "  CONDITION MET; guard suite NOT RUN -- run `go test -count=1 ./...` before calling this shippable")
	default:
		fmt.Fprintln(w, "  NOT SHIPPABLE -- see the reasons above")
	}
	fmt.Fprintln(w, "  note: the score-capture scorecard must be re-run from a FRESH BUILD; this process")
	fmt.Fprintln(w, "        cannot score the artifact it just wrote.")
}
