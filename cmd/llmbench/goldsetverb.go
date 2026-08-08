package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// runGoldSetSample builds the Extract Gold Set from the extract-decision tap's
// local capture: it streams the capture, dedupes it by URL, stratifies every page
// by the ADR-0042 lone-posting predicate, draws samplePlan's quota from each
// (stratum, verdict) cell in deterministic hash order, attaches each row's
// cap-derived sampling weight, and writes the substrate plus its review sheet.
// Labels and provenance are left EMPTY -- proposing a label is a separate,
// deliberately human-reviewable step (goldset-worksheet / goldset-apply).
// Returns the process exit code: 2 on a usage or wiring error, 1 on IO, 0 on
// success.
func runGoldSetSample(args []string) int {
	fs := flag.NewFlagSet("goldset-sample", flag.ExitOnError)
	capture := fs.String("capture", "", "extract-capture JSONL written by the EXTRACT_CAPTURE_PATH tap (required; gitignored, never committed)")
	dir := fs.String("dir", defaultGoldSetDir, "directory the Extract Gold Set is written to")
	seed := fs.String("seed", defaultSeed, "seed for the deterministic within-cell selection; changing it is a deliberate resample")
	acceptShare := fs.Float64("accept-share", -1, "the live extract stream's accept share; -1 reconstructs it from the capture's per-verdict caps (ADR-0043)")
	_ = fs.Parse(args)

	if *capture == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-sample -capture <capture.jsonl> [-dir d] [-seed s] [-accept-share f]")
		return 2
	}

	scan, err := scanCapture(*capture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: %v\n", err)
		return 1
	}
	sel, err := applyPlan(scan, samplePlan, *seed, *acceptShare)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: %v\n", err)
		return 2
	}
	rows, err := readSelected(*capture, sel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: create %q: %v\n", *dir, err)
		return 1
	}
	substrate, sheet := goldSetPaths(*dir)
	if err := writeGoldSet(substrate, rows); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: %v\n", err)
		return 1
	}
	if err := os.WriteFile(sheet, renderSheet(rows), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: write %q: %v\n", sheet, err)
		return 1
	}

	printSampleSummary(os.Stdout, scan, sel, rows, substrate)
	return 0
}

// runGoldSetSampleRandom draws the ADR-0043 RANDOM stratum (#262) and APPENDS it to
// the existing Extract Gold Set: it streams the capture, narrows it to the faithful
// frame at or after -since, drops the URLs the substrate already carries, collapses
// every candidate's sampling cell to the verdict, draws randomSamplePlan's quotas in
// deterministic hash order, attaches each row's weight, and rewrites the substrate
// and both review sheets. Existing rows -- their labels, provenance and expected
// extractions -- pass through untouched: unlike goldset-sample this verb is
// APPEND-ONLY, because the labels it extends cost a review pass to produce.
//
// -accept-share is REQUIRED and never reconstructed. liveAcceptShare splits sessions
// on a one-hour ts gap, and three of the capture's six process frames are contiguous
// within that, so the estimator would silently merge them and produce a wrong number.
// The reconstruction is still printed beside the supplied value as a cross-check.
// Returns the process exit code: 2 on a usage or validation error, 1 on IO, 0 on
// success.
func runGoldSetSampleRandom(args []string) int {
	fs := flag.NewFlagSet("goldset-sample-random", flag.ExitOnError)
	capture := fs.String("capture", "", "extract-capture JSONL written by the EXTRACT_CAPTURE_PATH tap (required; gitignored, never committed)")
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set the random stratum is appended to")
	seed := fs.String("seed", defaultRandomSeed, "seed for the deterministic within-cell selection; changing it is a deliberate resample")
	since := fs.String("since", "", "RFC3339 cutoff: only capture records at or after it are sampled (required; excludes windows parsed by a superseded parser)")
	acceptShare := fs.Float64("accept-share", -1, "the live extract stream's MEASURED accept share, in (0,1) (required; this verb never reconstructs it)")
	_ = fs.Parse(args)

	if *capture == "" || *since == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-sample-random -capture <capture.jsonl> -since <RFC3339> -accept-share <p> [-dir d] [-seed s]")
		return 2
	}
	cutoff, err := time.Parse(time.RFC3339, *since)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: -since %q: %v\n", *since, err)
		return 2
	}
	if *acceptShare <= 0 || *acceptShare >= 1 {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: -accept-share must be in (0,1), got %g; the capture's session split merges process frames, so this verb refuses to reconstruct it\n", *acceptShare)
		return 2
	}

	scan, err := scanCapture(*capture)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v\n", err)
		return 1
	}
	framed, outOfFrame := frameSince(scan, cutoff)

	// The substrate MUST already exist: the random stratum extends the #254 file, it
	// never creates one. A missing file here is a wrong -dir, and silently starting a
	// new substrate would strand every existing label.
	substrate, sheetPath := goldSetPaths(*dir)
	existing, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v (the random stratum extends an existing gold set; run goldset-sample first)\n", err)
		return 1
	}
	committed := map[string]struct{}{}
	for _, row := range existing {
		committed[row.URL] = struct{}{}
	}
	framed, alreadyCommitted := excludingURLs(framed, committed)

	sel, err := applyPlan(asStratum(framed, stratumRandom), randomSamplePlan, *seed, *acceptShare)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v\n", err)
		return 2
	}
	drawn, err := readSelected(*capture, sel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v\n", err)
		return 1
	}
	if err := validateDrawnRandomRows(drawn, committed); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v\n", err)
		return 2
	}

	merged := append(append([]goldRow{}, existing...), drawn...)
	if err := writeGoldSet(substrate, merged); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v\n", err)
		return 1
	}
	if err := os.WriteFile(sheetPath, renderSheet(merged), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: write %q: %v\n", sheetPath, err)
		return 1
	}
	if err := os.WriteFile(expectedSheetPath(*dir), renderExpectedSheet(merged), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: write %q: %v\n", expectedSheetPath(*dir), err)
		return 1
	}

	printRandomSampleSummary(os.Stdout, scan, framed, sel, drawn, merged, outOfFrame, alreadyCommitted, substrate)
	return 0
}

// validateDrawnRandomRows refuses a draw that could corrupt the substrate: a row
// already committed (one page cannot carry two drawings' incompatible weights), a
// duplicate within the draw, a row outside the random stratum, a non-positive
// weight, or a row that arrived carrying a label. Nothing is written until it
// passes, so a bad draw leaves the committed file exactly as it was.
func validateDrawnRandomRows(drawn []goldRow, committed map[string]struct{}) error {
	seen := map[string]struct{}{}
	for _, row := range drawn {
		if _, dup := committed[row.URL]; dup {
			return fmt.Errorf("drew %q, which the substrate already carries", row.URL)
		}
		if _, dup := seen[row.URL]; dup {
			return fmt.Errorf("drew %q twice", row.URL)
		}
		seen[row.URL] = struct{}{}
		if row.Stratum != stratumRandom {
			return fmt.Errorf("drew %q in stratum %q, want %q", row.URL, row.Stratum, stratumRandom)
		}
		if row.Weight <= 0 {
			return fmt.Errorf("drew %q with weight %g, want > 0", row.URL, row.Weight)
		}
		if row.Label != "" {
			return fmt.Errorf("drew %q carrying label %q; a fresh draw is unlabelled", row.URL, row.Label)
		}
	}
	return nil
}

// printRandomSampleSummary writes the account an operator needs to trust the random
// drawing: what the capture held, what each narrowing step dropped and why, the
// accept share the weights rest on ALONGSIDE the estimator's reconstruction (which
// is a cross-check and does not govern), the realized per-cell design, and the two
// row counts -- the drawing's own, whose weights must sum to it, and the union's.
func printRandomSampleSummary(w io.Writer, scan, framed captureScan, sel selection, drawn, merged []goldRow, outOfFrame, alreadyCommitted int, substrate string) {
	fmt.Fprintln(w, "extract gold set random stratum")
	fmt.Fprintf(w, "  capture lines       %d\n", scan.Lines)
	fmt.Fprintf(w, "  duplicate lines     %d (deduped by url, latest ts wins)\n", scan.Duplicates)
	fmt.Fprintf(w, "  dropped oversized   %d (raw line > %d bytes)\n", scan.Oversized, maxCandidateBytes)
	fmt.Fprintf(w, "  dropped bad url     %d\n", scan.BadURL)
	fmt.Fprintf(w, "  dropped out-of-frame %d (superseded parser, or an unparseable ts)\n", outOfFrame)
	fmt.Fprintf(w, "  dropped committed   %d (already in the substrate from an earlier drawing)\n", alreadyCommitted)
	fmt.Fprintf(w, "  candidate frame     %d\n", len(framed.Candidates))
	fmt.Fprintf(w, "  accept share        %.4f (supplied; governs the weights)\n", sel.AcceptShare)
	if estimated, ok := liveAcceptShare(framed.Timeline); ok {
		fmt.Fprintf(w, "  accept share        %.4f (liveAcceptShare's reconstruction -- CROSS-CHECK ONLY, does not govern:\n", estimated)
		fmt.Fprintf(w, "                      its %v session split merges contiguous process frames of this capture)\n", sessionGap)
	} else {
		fmt.Fprintln(w, "  accept share        (liveAcceptShare could not reconstruct one from this frame)")
	}
	for _, c := range sel.Cells {
		fmt.Fprintf(w, "  cell %-8s verdict=%-5v population %5d  sampled %3d  weight %.5f\n",
			c.Key.Stratum, c.Key.Verdict, c.Population, c.Sampled, c.Weight)
	}
	fmt.Fprintf(w, "  drawn rows          %d\n", len(drawn))
	fmt.Fprintf(w, "  drawn weight sum    %.4f (must equal the drawn rows)\n", weightSum(drawn))
	fmt.Fprintf(w, "  substrate rows      %d (%d existing + %d drawn)\n", len(merged), len(merged)-len(drawn), len(drawn))
	if info, err := os.Stat(substrate); err == nil {
		fmt.Fprintf(w, "  wrote               %s (%d bytes)\n", substrate, info.Size())
	}
}

// readSelected re-streams the capture and materializes the selected rows in full,
// keyed by the exact line the dedupe chose so a re-extracted URL yields the same
// record the stratification saw. Only selected lines are decoded whole, so peak
// memory stays proportional to the sample rather than to the 87 MB capture.
func readSelected(path string, sel selection) ([]goldRow, error) {
	byLine := map[int]candidate{}
	for _, c := range sel.Chosen {
		byLine[c.Line] = c
	}
	weightByCell := map[cellKey]float64{}
	for _, c := range sel.Cells {
		weightByCell[c.Key] = c.Weight
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open capture %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	rows := []goldRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), captureScanBuffer)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		cand, ok := byLine[lineNo]
		if !ok {
			continue
		}
		var row goldRow
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("capture line %d: %w", lineNo, err)
		}
		row.Stratum = cand.Stratum
		row.Weight = weightByCell[cellKey{cand.Stratum, cand.Verdict}]
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read capture %q: %w", path, err)
	}
	if len(rows) != len(sel.Chosen) {
		return nil, fmt.Errorf("materialized %d rows, selected %d", len(rows), len(sel.Chosen))
	}
	return rows, nil
}

// printSampleSummary writes the sampling account an operator needs to trust the
// file: what was read, what was dropped and why, the realized per-cell design, and
// the accept share the weights rest on. It is the number set the README records.
func printSampleSummary(w io.Writer, scan captureScan, sel selection, rows []goldRow, substrate string) {
	fmt.Fprintln(w, "extract gold set sample")
	fmt.Fprintf(w, "  capture lines     %d\n", scan.Lines)
	fmt.Fprintf(w, "  unique urls       %d\n", len(scan.Candidates))
	fmt.Fprintf(w, "  duplicate lines   %d (deduped by url, latest ts wins)\n", scan.Duplicates)
	fmt.Fprintf(w, "  dropped oversized %d (raw line > %d bytes)\n", scan.Oversized, maxCandidateBytes)
	fmt.Fprintf(w, "  dropped bad url   %d\n", scan.BadURL)
	source := "supplied"
	if sel.AcceptShareEstimated {
		source = "reconstructed from the per-verdict caps"
	}
	fmt.Fprintf(w, "  accept share      %.4f (%s)\n", sel.AcceptShare, source)
	for _, c := range sel.Cells {
		fmt.Fprintf(w, "  cell %-18s verdict=%-5v population %5d  sampled %3d  weight %.4f\n",
			c.Key.Stratum, c.Key.Verdict, c.Population, c.Sampled, c.Weight)
	}
	fmt.Fprintf(w, "  rows              %d\n", len(rows))
	fmt.Fprintf(w, "  weight sum        %.4f (must equal rows)\n", weightSum(rows))
	if info, err := os.Stat(substrate); err == nil {
		fmt.Fprintf(w, "  wrote             %s (%d bytes)\n", substrate, info.Size())
	}
}

// runGoldSetWorksheet renders the labeler's view of the committed substrate to
// -out: per row, the page's own title, two windows into its main content, and its
// outbound-link counts. Every structured-data-derived field -- the stratum
// included -- is withheld, as is the live extractor's verdict, and rows are emitted
// in hash-shuffled order, so a label cannot be inferred from the very signals the
// gold set exists to test the mechanism against (ADR-0043). The worksheet is a
// working artifact and is never committed.
//
// -stratum narrows the sheet to one sampling cell, and -n takes only the first N
// rows AFTER the shuffle: together they cut a deterministic, quotable subset -- the
// 20 rows a human spot-checks the random stratum on, rather than the whole 4 MB
// substrate. Exit: 2 on usage, 1 on IO, 0 otherwise.
func runGoldSetWorksheet(args []string) int {
	fs := flag.NewFlagSet("goldset-worksheet", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set")
	out := fs.String("out", "", "path to write the worksheet JSONL to (required; a working artifact, never committed)")
	seed := fs.String("seed", defaultSeed, "seed for the presentation shuffle")
	stratum := fs.String("stratum", "", "restrict the sheet to one stratum; empty renders every row")
	n := fs.Int("n", 0, "render only the first N rows after the shuffle (a deterministic subset); 0 renders all")
	_ = fs.Parse(args)

	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-worksheet -out <worksheet.jsonl> [-dir d] [-seed s] [-stratum s] [-n k]")
		return 2
	}
	if *stratum != "" && !goldStratum(*stratum).Valid() {
		fmt.Fprintf(os.Stderr, "llmbench goldset-worksheet: -stratum %q is not a known stratum\n", *stratum)
		return 2
	}

	substrate, _ := goldSetPaths(*dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-worksheet: %v\n", err)
		return 1
	}

	sheets := make([]worksheetRow, 0, len(rows))
	for _, row := range rows {
		if *stratum != "" && row.Stratum != goldStratum(*stratum) {
			continue
		}
		sheets = append(sheets, worksheetFor(row))
	}
	sort.Slice(sheets, func(i, j int) bool {
		return seededHash(*seed, sheets[i].URL) < seededHash(*seed, sheets[j].URL)
	})
	if *n > 0 && *n < len(sheets) {
		sheets = sheets[:*n]
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-worksheet: create %q: %v\n", *out, err)
		return 1
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, s := range sheets {
		if err := enc.Encode(s); err != nil {
			_ = f.Close()
			fmt.Fprintf(os.Stderr, "llmbench goldset-worksheet: encode %q: %v\n", s.URL, err)
			return 1
		}
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-worksheet: close %q: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(os.Stdout, "wrote %s (%d rows)\n", *out, len(sheets))
	return 0
}

// runGoldSetApply folds labels, expected extractions and their provenance into the
// committed Extract Gold Set. It reads both review sheets (and, with -proposals, a
// proposer's id/label/note file), merges them into the substrate by row, stamps the
// provenance the flags authorize, and rewrites the substrate and both sheets
// canonically so the three can never drift.
//
// The expected extractions live in the same verb as the labels because they ARE
// labels -- the field-fidelity ground truth the #256 guard rests on -- so they get
// the same total-validation-then-write discipline.
//
// Nothing is written unless every row validates, so a stale sheet fails loudly
// rather than half-applying. An existing proposer or confirmer is NEVER
// overwritten and its timestamp never re-stamped: provenance is an append-only
// record of who actually saw the row. Exit: 2 on usage or a validation failure, 1
// on IO, 0 otherwise.
func runGoldSetApply(args []string) int {
	fs := flag.NewFlagSet("goldset-apply", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set")
	proposals := fs.String("proposals", "", "optional TSV of id<TAB>label<TAB>note to merge before stamping (a proposer's output)")
	proposedBy := fs.String("proposed-by", "", "name to stamp as the proposer on labelled rows that have none; prefix an automated proposer with \"llm:\"")
	confirmedBy := fs.String("confirmed-by", "", "HUMAN name to stamp as the confirmer on labelled rows that have none; an \"llm:\" name is rejected")
	confirmStratum := fs.String("confirm-stratum", "", "restrict -confirmed-by to one stratum; empty confirms every labelled row")
	expectedProposedBy := fs.String("expected-proposed-by", "", "name to stamp as the proposer on expected extractions that have none; prefix an automated reading with \"script:\" or \"llm:\"")
	expectedConfirmedBy := fs.String("expected-confirmed-by", "", "HUMAN name to stamp as the confirmer on expected extractions that have none; an \"llm:\" or \"script:\" name is rejected")
	now := fs.String("now", "", "RFC3339 UTC timestamp to stamp; empty uses the current time")
	_ = fs.Parse(args)

	stamp := time.Now().UTC().Format(time.RFC3339)
	if *now != "" {
		t, err := time.Parse(time.RFC3339, *now)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-apply: -now %q: %v\n", *now, err)
			return 2
		}
		stamp = t.UTC().Format(time.RFC3339)
	}
	if strings.HasPrefix(*confirmedBy, "llm:") {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: -confirmed-by %q is not a human; confirmation cannot be automated (ADR-0043)\n", *confirmedBy)
		return 2
	}
	if machineName(*expectedConfirmedBy) {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: -expected-confirmed-by %q is not a human; confirmation cannot be automated (ADR-0043)\n", *expectedConfirmedBy)
		return 2
	}
	if *confirmStratum != "" && !goldStratum(*confirmStratum).Valid() {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: -confirm-stratum %q is not a known stratum\n", *confirmStratum)
		return 2
	}

	substrate, sheetPath := goldSetPaths(*dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 1
	}
	sheetData, err := os.ReadFile(sheetPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: read %q: %v\n", sheetPath, err)
		return 1
	}
	sheet, err := parseSheet(sheetData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 2
	}
	var proposed []sheetRow
	if *proposals != "" {
		data, err := os.ReadFile(*proposals)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-apply: read %q: %v\n", *proposals, err)
			return 1
		}
		proposed, err = parseProposals(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
			return 2
		}
	}

	// A missing expected.tsv is the bootstrap path, before propose-expected.sh has
	// ever run, so it reads as an empty sheet rather than an error.
	expectedPath := expectedSheetPath(*dir)
	var expected []expectedSheetRow
	if expectedData, err := os.ReadFile(expectedPath); err == nil {
		expected, err = parseExpectedSheet(expectedData)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
			return 2
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: read %q: %v\n", expectedPath, err)
		return 1
	}

	merged, err := applyLabels(rows, sheet, proposed, *proposedBy, *confirmedBy, goldStratum(*confirmStratum), stamp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 2
	}
	merged, err = applyExpected(merged, expected, *expectedProposedBy, *expectedConfirmedBy, stamp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 2
	}

	if err := writeGoldSet(substrate, merged); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 1
	}
	if err := os.WriteFile(sheetPath, renderSheet(merged), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: write %q: %v\n", sheetPath, err)
		return 1
	}
	if err := os.WriteFile(expectedPath, renderExpectedSheet(merged), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: write %q: %v\n", expectedPath, err)
		return 1
	}

	printApplySummary(os.Stdout, merged)
	return 0
}

// machineName reports whether a confirmer name announces an automated author. A
// confirmation is only meaningful from a person (ADR-0043), and the expected
// extractions are proposed by a script, so both prefixes are refused here.
func machineName(name string) bool {
	return strings.HasPrefix(name, "llm:") || strings.HasPrefix(name, "script:")
}

// parseProposals parses a proposer's output: id<TAB>label<TAB>note, one row per
// line, blank and '#' lines skipped. The key is the row ID rather than the URL so
// a proposer never has to retype a long captured URL -- a mistyped URL would
// silently attach a label to the wrong page, or to none.
func parseProposals(data []byte) ([]sheetRow, error) {
	rows := []sheetRow{}
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 || len(fields) > 3 {
			return nil, fmt.Errorf("proposals line %d: got %d columns, want id<TAB>label[<TAB>note]", i+1, len(fields))
		}
		label := bench.ExtractLabel(strings.TrimSpace(fields[1]))
		if !label.Valid() {
			return nil, fmt.Errorf("proposals line %d: unknown label %q", i+1, label)
		}
		note := ""
		if len(fields) == 3 {
			note = flattenField(fields[2])
		}
		rows = append(rows, sheetRow{ID: strings.TrimSpace(fields[0]), Label: label, Note: note})
	}
	return rows, nil
}

// applyLabels is the pure merge behind goldset-apply: it validates the sheet
// against the substrate, folds in the sheet's and the proposals' labels and notes,
// and stamps the provenance the caller authorized. It returns a new slice and
// mutates nothing, so a validation failure leaves the caller's rows -- and
// therefore the files on disk -- untouched.
//
// Validation is total and fails on the first fault: a sheet row whose URL or ID is
// not in the substrate, a duplicate sheet row, a sheet stratum or verdict that
// disagrees with the substrate (the signature of a sheet edited against an older
// sample), or a proposal for an unknown row.
func applyLabels(rows []goldRow, sheet, proposed []sheetRow, proposedBy, confirmedBy string, confirmStratum goldStratum, stamp string) ([]goldRow, error) {
	byID := map[string]int{}
	for i, row := range rows {
		byID[rowID(row.URL)] = i
	}

	merged := make([]goldRow, len(rows))
	copy(merged, rows)

	seen := map[string]struct{}{}
	for _, s := range sheet {
		i, ok := byID[s.ID]
		if !ok || merged[i].URL != s.URL {
			return nil, fmt.Errorf("sheet row %q (%s) is not in the gold set: regenerate the sheet from the substrate", s.URL, s.ID)
		}
		if _, dup := seen[s.ID]; dup {
			return nil, fmt.Errorf("sheet row %q appears twice", s.URL)
		}
		seen[s.ID] = struct{}{}
		if s.Stratum != merged[i].Stratum {
			return nil, fmt.Errorf("sheet row %q: stratum %q disagrees with the substrate (%q)", s.URL, s.Stratum, merged[i].Stratum)
		}
		if s.Verdict != merged[i].Verdict {
			return nil, fmt.Errorf("sheet row %q: verdict %v disagrees with the substrate (%v)", s.URL, s.Verdict, merged[i].Verdict)
		}
		// A confirmed label that changes retracts its confirmation, for the same
		// reason applyExpected retracts one whose values changed: the human confirmed
		// the old label, not this one.
		if merged[i].LabelProvenance.ConfirmedBy != "" && merged[i].Label != s.Label {
			merged[i].LabelProvenance.ConfirmedBy, merged[i].LabelProvenance.ConfirmedAt = "", ""
		}
		merged[i].Label = s.Label
		if s.Note != "" {
			merged[i].LabelProvenance.Note = s.Note
		}
		if s.ProposedBy != "" {
			merged[i].LabelProvenance.ProposedBy = s.ProposedBy
		}
		// Stamp the time alongside the name, as the -confirmed-by flag path does: a
		// confirmer with no timestamp is an incomplete provenance record, and the
		// well-formedness guard rejects one. A sheet is the only way to confirm a
		// SUBSET of rows, so this path has to produce the same shape.
		if s.ConfirmedBy != "" && merged[i].LabelProvenance.ConfirmedBy == "" {
			merged[i].LabelProvenance.ConfirmedBy = s.ConfirmedBy
			merged[i].LabelProvenance.ConfirmedAt = stamp
		}
	}

	proposedSeen := map[string]struct{}{}
	for _, p := range proposed {
		i, ok := byID[p.ID]
		if !ok {
			return nil, fmt.Errorf("proposal for unknown row id %q", p.ID)
		}
		if _, dup := proposedSeen[p.ID]; dup {
			return nil, fmt.Errorf("proposal for row id %q appears twice", p.ID)
		}
		proposedSeen[p.ID] = struct{}{}
		merged[i].Label = p.Label
		if p.Note != "" {
			merged[i].LabelProvenance.Note = p.Note
		}
	}

	for i := range merged {
		if !merged[i].Label.Valid() {
			continue
		}
		prov := &merged[i].LabelProvenance
		if proposedBy != "" && prov.ProposedBy == "" {
			prov.ProposedBy, prov.ProposedAt = proposedBy, stamp
		}
		if confirmedBy == "" || prov.ConfirmedBy != "" {
			continue
		}
		if confirmStratum != "" && merged[i].Stratum != confirmStratum {
			continue
		}
		prov.ConfirmedBy, prov.ConfirmedAt = confirmedBy, stamp
	}

	return merged, nil
}

// applyExpected is the pure merge behind goldset-apply's expected-extraction half
// (#256): it validates the expected sheet against the substrate, folds its values
// into each row's Expected block, and stamps the provenance the caller authorized.
// Like applyLabels it returns a new slice and mutates nothing, so a validation
// failure leaves the caller's rows -- and therefore the files on disk -- untouched.
//
// Validation is total and fails on the first fault: a sheet row that is not in the
// substrate, a duplicate, a label that disagrees with the substrate (the signature
// of a sheet edited against an older sample), an expectation on a row outside the
// lone-posting stratum (the Free Extraction never fires there, so it could never be
// scored), and an acceptance on a row that is not residue -- a detail row has
// nothing to excuse, and a hub-index row is the exact shape ADR-0042's predicate
// rejects structurally, so excusing one would mean the predicate itself is broken.
func applyExpected(rows []goldRow, sheet []expectedSheetRow, proposedBy, confirmedBy, stamp string) ([]goldRow, error) {
	byID := map[string]int{}
	for i, row := range rows {
		byID[rowID(row.URL)] = i
	}

	merged := make([]goldRow, len(rows))
	copy(merged, rows)

	seen := map[string]struct{}{}
	for _, s := range sheet {
		i, ok := byID[s.ID]
		if !ok || merged[i].URL != s.URL {
			return nil, fmt.Errorf("expected row %q (%s) is not in the gold set: regenerate the sheet from the substrate", s.URL, s.ID)
		}
		if _, dup := seen[s.ID]; dup {
			return nil, fmt.Errorf("expected row %q appears twice", s.URL)
		}
		seen[s.ID] = struct{}{}
		if s.Label != merged[i].Label {
			return nil, fmt.Errorf("expected row %q: label %q disagrees with the substrate (%q)", s.URL, s.Label, merged[i].Label)
		}
		if merged[i].Stratum != stratumLonePosting {
			return nil, fmt.Errorf("expected row %q is in stratum %q: a Free Extraction never fires on it, so an expectation there is unscorable", s.URL, merged[i].Stratum)
		}
		if s.FreeOK && s.Label != bench.ExtractResidue {
			return nil, fmt.Errorf("expected row %q: free_ok on a %q row; only a residue fire is excusable (ADR-0042)", s.URL, s.Label)
		}

		// Copy the block before writing: merged shares its pointers with the
		// caller's rows, so mutating in place would edit the input on a later
		// failure.
		expected := goldExpected{}
		if merged[i].Expected != nil {
			expected = *merged[i].Expected
		}
		// A confirmation is a statement about specific values, so an edit to any of
		// them retracts it: without this a re-proposal could silently change a title
		// -- or re-arm a free_ok acceptance a human had withdrawn -- and leave it
		// stamped with that human's name.
		if expected.ConfirmedBy != "" && (expected.Title != s.Title ||
			expected.Location != s.Location ||
			expected.WorkArrangement != s.WorkArrangement ||
			expected.FreeOK != s.FreeOK ||
			expected.FreeOKNote != s.FreeOKNote) {
			expected.ConfirmedBy, expected.ConfirmedAt = "", ""
		}
		expected.Title, expected.Location, expected.WorkArrangement = s.Title, s.Location, s.WorkArrangement
		expected.FreeOK, expected.FreeOKNote = s.FreeOK, s.FreeOKNote
		if s.ProposedBy != "" && expected.ProposedBy == "" {
			expected.ProposedBy, expected.ProposedAt = s.ProposedBy, stamp
		}
		if s.ConfirmedBy != "" && expected.ConfirmedBy == "" {
			expected.ConfirmedBy, expected.ConfirmedAt = s.ConfirmedBy, stamp
		}
		merged[i].Expected = &expected
	}

	// The sheet is the COMPLETE set of expectations, not a patch: it lists exactly the
	// rows a correct Free Extraction fires on. A lone-posting row missing from it no
	// longer fires -- because the mechanism was narrowed, or the page changed -- so its
	// expectation is stale and must go, or score-free reports it as an unused
	// expectation forever.
	for i := range merged {
		if merged[i].Stratum != stratumLonePosting || merged[i].Expected == nil {
			continue
		}
		if _, listed := seen[rowID(merged[i].URL)]; !listed {
			merged[i].Expected = nil
		}
	}

	if proposedBy == "" && confirmedBy == "" {
		return merged, nil
	}
	for i := range merged {
		if merged[i].Expected == nil {
			continue
		}
		expected := *merged[i].Expected
		if proposedBy != "" && expected.ProposedBy == "" {
			expected.ProposedBy, expected.ProposedAt = proposedBy, stamp
		}
		if confirmedBy != "" && expected.ConfirmedBy == "" {
			expected.ConfirmedBy, expected.ConfirmedAt = confirmedBy, stamp
		}
		merged[i].Expected = &expected
	}
	return merged, nil
}

// printApplySummary reports the labeling state after a merge: how many rows carry
// each label, and -- the number the acceptance criterion turns on -- how many
// lone-posting rows still have no human confirmation. The expected-extraction
// block gets the same treatment, since it is the ground truth the #256 fidelity
// guard rests on.
//
// The random stratum reports SPOT-CHECKS rather than a pending count, because
// ADR-0043 asks it to be spot-checked rather than fully confirmed: the number that
// matters there is how many rows a human actually read, not how many they have left.
func printApplySummary(w io.Writer, rows []goldRow) {
	byLabel := map[bench.ExtractLabel]int{}
	labelled, proposedOnly, pending := 0, 0, 0
	expected, acceptedFires, expectedPending := 0, 0, 0
	randomRows, randomLabelled, randomSpotChecked := 0, 0, 0
	for _, row := range rows {
		if row.Expected != nil {
			expected++
			if row.Expected.FreeOK {
				acceptedFires++
			}
			if row.Expected.ConfirmedBy == "" {
				expectedPending++
			}
		}
		if row.Stratum == stratumRandom {
			randomRows++
			if row.Label.Valid() {
				randomLabelled++
			}
			if row.LabelProvenance.ConfirmedBy != "" {
				randomSpotChecked++
			}
		}
		if !row.Label.Valid() {
			continue
		}
		labelled++
		byLabel[row.Label]++
		if row.LabelProvenance.ConfirmedBy == "" {
			proposedOnly++
			if row.Stratum == stratumLonePosting {
				pending++
			}
		}
	}
	fmt.Fprintln(w, "extract gold set labels")
	fmt.Fprintf(w, "  rows              %d\n", len(rows))
	fmt.Fprintf(w, "  labelled          %d\n", labelled)
	for _, l := range bench.AllExtractLabels {
		fmt.Fprintf(w, "  %-17s %d\n", l, byLabel[l])
	}
	fmt.Fprintf(w, "  unconfirmed       %d\n", proposedOnly)
	fmt.Fprintf(w, "  pending human     %d (lone-posting rows awaiting confirmation)\n", pending)
	fmt.Fprintf(w, "  expected          %d\n", expected)
	fmt.Fprintf(w, "  expected-accepted-fires %d (residue rows carrying an argued exception)\n", acceptedFires)
	fmt.Fprintf(w, "  expected pending human  %d (rows awaiting confirmation)\n", expectedPending)
	if randomRows > 0 {
		fmt.Fprintf(w, "  random stratum    %d (labelled %d)\n", randomRows, randomLabelled)
		fmt.Fprintf(w, "  spot-checked      %d (random rows a human confirmed)\n", randomSpotChecked)
	}
}

// weightSum is the file's total sampling weight, which must equal its row count:
// the weights are normalized inverse inclusion probabilities, so any other total
// means a cell was mis-counted. Shared by the sampler's summary and the committed
// file's guard.
func weightSum(rows []goldRow) float64 {
	total := 0.0
	for _, row := range rows {
		total += row.Weight
	}
	return total
}

// weightsBalanced reports whether rows' weights sum to their count within tol.
func weightsBalanced(rows []goldRow, tol float64) bool {
	return math.Abs(weightSum(rows)-float64(len(rows))) <= tol
}
