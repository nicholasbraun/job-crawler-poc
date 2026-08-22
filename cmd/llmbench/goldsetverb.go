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
	"path/filepath"
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
	// A fresh draw carries no expected extractions, so this verb writes the substrate
	// and the labels sheet only -- expected.tsv is created by the first goldset-apply.
	// Both are staged and renamed together, so a failure leaves neither truncated (#283).
	substrate, sheet := goldSetPaths(*dir)
	if err := atomicWriteAll(goldSetWrite(substrate, rows), sheetWrite(sheet, rows)); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample: %v\n", err)
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
	substrate, _ := goldSetPaths(*dir)
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
	if err := writeGoldSetFiles(*dir, merged); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-sample-random: %v\n", err)
		return 1
	}

	printRandomSampleSummary(os.Stdout, scan, framed, sel, drawn, merged, outOfFrame, alreadyCommitted, substrate)
	return 0
}

// runGoldSetConfirmSheet renders the Boundary Stratum's human confirmation sheet:
// ordered Markdown chunks of -per-file rows each, written under -out-dir. It is the
// artifact ADR-0043's confirmation rule is actually performed on, and like the
// labeler's worksheet it is a working artifact that is never committed -- it
// regenerates from the substrate.
//
// Exit: 2 on usage, 1 on IO, 0 otherwise.
func runGoldSetConfirmSheet(args []string) int {
	fs := flag.NewFlagSet("goldset-confirm-sheet", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set")
	outDir := fs.String("out-dir", "", "directory to write the confirmation chunks to (required; a working artifact, never committed)")
	stratum := fs.String("stratum", string(stratumBoundary), "restrict the sheet to one stratum")
	perFile := fs.Int("per-file", confirmRowsPerFile, "rows per chunk file")
	_ = fs.Parse(args)

	if *outDir == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-confirm-sheet -out-dir <dir> [-dir d] [-stratum s] [-per-file n]")
		return 2
	}
	if !goldStratum(*stratum).Valid() {
		fmt.Fprintf(os.Stderr, "llmbench goldset-confirm-sheet: -stratum %q is not a known stratum\n", *stratum)
		return 2
	}

	substrate, _ := goldSetPaths(*dir)
	rows, err := readGoldSet(substrate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-confirm-sheet: %v\n", err)
		return 1
	}
	selected := []goldRow{}
	for _, row := range rows {
		if row.Stratum == goldStratum(*stratum) {
			selected = append(selected, row)
		}
	}
	if len(selected) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench goldset-confirm-sheet: no row is in stratum %q\n", *stratum)
		return 2
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-confirm-sheet: create %q: %v\n", *outDir, err)
		return 1
	}

	chunks := renderConfirmSheet(selected, *perFile)
	for _, chunk := range chunks {
		path := filepath.Join(*outDir, chunk.Name)
		if err := os.WriteFile(path, chunk.Body, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-confirm-sheet: write %q: %v\n", path, err)
			return 1
		}
	}
	fmt.Fprintf(os.Stdout, "wrote %d chunks covering %d %s rows to %s\n", len(chunks), len(selected), *stratum, *outDir)
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
// record of who actually saw the row. The one way a confirmation leaves a row is
// RETRACTION -- a label that changes invalidates the confirmation of the old label,
// on both the sheet and the proposals path. Exit: 2 on usage or a validation
// failure, 1 on IO, 0 otherwise.
func runGoldSetApply(args []string) int {
	fs := flag.NewFlagSet("goldset-apply", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set")
	proposals := fs.String("proposals", "", "optional TSV of id<TAB>label<TAB>note to merge before stamping (a proposer's output)")
	proposedBy := fs.String("proposed-by", "", "name to stamp as the proposer on labelled rows that have none; prefix an automated proposer with \"llm:\"")
	confirmedBy := fs.String("confirmed-by", "", "HUMAN name to stamp as the confirmer on labelled rows that have none; an \"llm:\" or \"script:\" name is rejected")
	confirmStratum := fs.String("confirm-stratum", "", "restrict -confirmed-by to one stratum; empty confirms every labelled row")
	confirmIDs := fs.String("confirm-ids", "", "file of row ids (one per line) naming EXACTLY the rows -confirmed-by signs off; mutually exclusive with -confirm-stratum")
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
	if machineName(*confirmedBy) {
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
	// One unambiguous selector for a confirmation. Both together would leave it
	// unclear whether the id list widens the stratum or narrows it, and a confirmation
	// nobody can read back from the flags is not reviewable.
	if *confirmIDs != "" && *confirmStratum != "" {
		fmt.Fprintln(os.Stderr, "llmbench goldset-apply: -confirm-ids and -confirm-stratum are mutually exclusive; a confirmation names one set of rows")
		return 2
	}
	if *confirmIDs != "" && *confirmedBy == "" {
		fmt.Fprintln(os.Stderr, "llmbench goldset-apply: -confirm-ids needs -confirmed-by; a list of rows is not a confirmer")
		return 2
	}
	var confirmSet map[string]struct{}
	if *confirmIDs != "" {
		data, err := os.ReadFile(*confirmIDs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-apply: read %q: %v\n", *confirmIDs, err)
			return 1
		}
		confirmSet, err = parseConfirmIDs(data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
			return 2
		}
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

	merged, err := applyLabels(rows, sheet, proposed, *proposedBy, *confirmedBy, goldStratum(*confirmStratum), confirmSet, stamp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 2
	}
	merged, err = applyExpected(merged, expected, *expectedProposedBy, *expectedConfirmedBy, stamp)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
		return 2
	}

	if err := writeGoldSetFiles(*dir, merged); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench goldset-apply: %v\n", err)
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

// retractConfirmation withdraws a human's signature from a row whose label has just
// been replaced: the human confirmed the OLD label, and leaving their name on the new
// one is the single thing the confirmation record must never say.
//
// It deliberately leaves ProposedLabel alone. That field is a statement about the
// PROPOSER, not about the confirmation, and a value already there is true whatever
// happens to the label afterwards -- the proposer really did propose it. A retraction
// followed by a fresh confirmation of the new label is then scored by labelAgreement
// as the disagreement it is. What must NOT happen is the empty field being filled in
// afterwards from the replacement label, which would pair the proposer -- ProposedBy
// is first-writer-wins, so it still names the original one -- with an answer they
// never gave; applyLabels' confirmedAtEntry guard is what prevents that (ADR-0048).
func retractConfirmation(prov *goldProvenance) {
	prov.ConfirmedBy, prov.ConfirmedAt = "", ""
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
// sample), a sheet confirmer that names a machine, a proposal for an unknown row, or
// a confirmId naming a row that does not exist or carries no label to confirm.
//
// confirmIDs, when non-nil, names EXACTLY the rows confirmedBy signs off, so a human
// can confirm chunk by chunk across sessions and the diff shows precisely which rows
// gained a confirmer. It and confirmStratum are alternative selectors; the caller
// rejects both at once.
//
// It also backfills the Proposed Label onto every labelled row that carries a
// proposer and no confirmer, BEFORE the merge (ADR-0048), so a human overriding a
// label preserves what the proposer proposed rather than overwriting it. Like
// ProposedBy it is first-writer-wins and is never filled on a row that arrived
// already confirmed -- not even after a relabel retracts that confirmation: the set
// does not record which of those were relabelled during the pass that confirmed them,
// and what it does not know it must not invent from the replacement label.
func applyLabels(rows []goldRow, sheet, proposed []sheetRow, proposedBy, confirmedBy string, confirmStratum goldStratum, confirmIDs map[string]struct{}, stamp string) ([]goldRow, error) {
	byID := map[string]int{}
	// confirmedAtEntry remembers which rows arrived carrying a human's signature. A
	// retraction below clears that signature, so ConfirmedBy alone can no longer tell
	// "never confirmed" from "confirmed, then overturned in this very run" -- and only
	// the first of those may gain a Proposed Label (ADR-0048).
	confirmedAtEntry := make([]bool, len(rows))
	for i, row := range rows {
		byID[rowID(row.URL)] = i
		confirmedAtEntry[i] = row.LabelProvenance.ConfirmedBy != ""
	}

	merged := make([]goldRow, len(rows))
	copy(merged, rows)

	// Backfill the Proposed Label before anything here can change a label (ADR-0048).
	// A row with no confirmer still carries the label its proposer put on it --
	// nobody has overridden it -- so its current label IS the Proposed Label. An
	// already confirmed row is left empty: the set does not record which of those
	// were relabelled during the pass that confirmed them, and filling it would put a
	// claim on the record that nothing supports. Running before the merge is what
	// makes an override preserve the OLD label rather than the human's new one.
	for i := range merged {
		prov := &merged[i].LabelProvenance
		if prov.ProposedLabel == "" && !confirmedAtEntry[i] && prov.ProposedBy != "" && merged[i].Label.Valid() {
			prov.ProposedLabel = merged[i].Label
		}
	}

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
		// The sheet's confirmer column is subject to the SAME rule as the
		// -confirmed-by flag. Without this check a machine-driven pass that writes the
		// column walks straight past the flag guard, and the false-drop guard arms
		// itself on these confirmations (ADR-0043/#264) -- so a machine could stamp the
		// evidence its own labels are judged by.
		if machineName(s.ConfirmedBy) {
			return nil, fmt.Errorf("sheet row %q: confirmed_by %q is not a human; confirmation cannot be automated (ADR-0043)", s.URL, s.ConfirmedBy)
		}
		// A confirmed label that changes retracts its confirmation, for the same
		// reason applyExpected retracts one whose values changed: the human confirmed
		// the old label, not this one.
		if merged[i].LabelProvenance.ConfirmedBy != "" && merged[i].Label != s.Label {
			retractConfirmation(&merged[i].LabelProvenance)
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
		// A confirmed label that changes retracts its confirmation, exactly as the
		// sheet path above does: the human confirmed the OLD label, and leaving their
		// name on a label a proposer has since replaced is the one thing the
		// confirmation record must never say.
		if merged[i].LabelProvenance.ConfirmedBy != "" && merged[i].Label != p.Label {
			retractConfirmation(&merged[i].LabelProvenance)
		}
		merged[i].Label = p.Label
		if p.Note != "" {
			merged[i].LabelProvenance.Note = p.Note
		}
	}

	// The id list is validated against the MERGED rows -- a label proposed in this
	// same run counts -- and before anything is stamped. An unknown id is a typo or a
	// chunk file left over from an older draw; an id on an unlabelled row is a
	// confirmation of nothing. Sorted so the reported fault is the same one every run.
	ids := make([]string, 0, len(confirmIDs))
	for id := range confirmIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		i, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("-confirm-ids names unknown row id %q; regenerate the confirmation sheet from the substrate", id)
		}
		if !merged[i].Label.Valid() {
			return nil, fmt.Errorf("-confirm-ids names row %q (%s), which carries no label to confirm", merged[i].URL, id)
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
		// A label that first lands in THIS run has no earlier label to preserve, so the
		// proposal itself is the Proposed Label -- including on a row whose proposer was
		// stamped a few lines above. Guarded on the confirmer like the backfill, and on
		// the confirmer the row ARRIVED with: a row a human had already signed keeps an
		// empty Proposed Label even after a relabel retracts that signature, because
		// ProposedBy still names the proposer of the label that was just replaced, and
		// the set has no idea what THEY would have said about the replacement. A row
		// being confirmed in this run still reads empty here, which is exactly the moment
		// its label is still the proposer's own.
		if prov.ProposedLabel == "" && prov.ConfirmedBy == "" && !confirmedAtEntry[i] && prov.ProposedBy != "" {
			prov.ProposedLabel = merged[i].Label
		}
		if confirmedBy == "" || prov.ConfirmedBy != "" {
			continue
		}
		if confirmStratum != "" && merged[i].Stratum != confirmStratum {
			continue
		}
		if confirmIDs != nil {
			if _, listed := confirmIDs[rowID(merged[i].URL)]; !listed {
				continue
			}
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
		// The sheet's confirmer column is subject to the SAME rule as the
		// -expected-confirmed-by flag, for the reason applyLabels states about its own
		// sheet: a machine-driven pass that writes the column otherwise walks straight
		// past the flag guard. What it buys is smaller here -- the false-drop guard arms
		// on LABEL confirmations, not on these -- but pendingExpectedConfirmations is a
		// ratchet over the #256 field-fidelity ground truth, and a machine must not be
		// able to lower that either.
		if machineName(s.ConfirmedBy) {
			return nil, fmt.Errorf("expected row %q: confirmed_by %q is not a human; confirmation cannot be automated (ADR-0043)", s.URL, s.ConfirmedBy)
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
// Both Boundary Strata report a PENDING count like the lone-posting one: ADR-0043
// requires every one of their rows confirmed, since they are the rows a hard-zero
// false-drop guard is decided on. Each block prints only once its stratum has rows,
// so a stratum that is defined and not yet drawn says nothing rather than reporting
// four zeroes.
//
// The agreement line is what a blind confirmation pass is FOR (ADR-0048): how often
// an independent human confirmer reached the label the row's proposer proposed. Its
// denominator counts only the rows where both are known, so it is legitimately zero
// on a set whose confirmations all predate the Proposed Label.
func printApplySummary(w io.Writer, rows []goldRow) {
	byLabel := map[bench.ExtractLabel]int{}
	labelled, proposedOnly, pending := 0, 0, 0
	expected, acceptedFires, expectedPending := 0, 0, 0
	randomRows, randomLabelled, randomSpotChecked := 0, 0, 0
	boundary := tallyStratum(rows, stratumBoundary)
	vetoBoundary := tallyStratum(rows, stratumVetoBoundary)
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
	// Printed outside the AllExtractLabels loop because it is not one of the scored
	// classes, and printed unconditionally so a set with none says so.
	fmt.Fprintf(w, "  %-17s %d (excluded from scoring)\n", bench.ExtractAmbiguous, byLabel[bench.ExtractAmbiguous])
	fmt.Fprintf(w, "  unconfirmed       %d\n", proposedOnly)
	fmt.Fprintf(w, "  pending human     %d (lone-posting rows awaiting confirmation)\n", pending)
	// Agreement between an independent human and the proposer, which is what a blind
	// confirmation pass is FOR (ADR-0048). ConfirmedBy is always a human -- both write
	// paths reject a machine name -- so no filter is needed here.
	if agreed, comparable := labelAgreement(rows); comparable > 0 {
		fmt.Fprintf(w, "  agreement         %d/%d (%.1f%%) (a human confirmer against the Proposed Label)\n",
			agreed, comparable, 100*float64(agreed)/float64(comparable))
	} else {
		fmt.Fprintln(w, "  agreement         0/0 (no confirmed row carries a Proposed Label yet)")
	}
	fmt.Fprintf(w, "  expected          %d\n", expected)
	fmt.Fprintf(w, "  expected-accepted-fires %d (residue rows carrying an argued exception)\n", acceptedFires)
	fmt.Fprintf(w, "  expected pending human  %d (rows awaiting confirmation)\n", expectedPending)
	if randomRows > 0 {
		fmt.Fprintf(w, "  random stratum    %d (labelled %d)\n", randomRows, randomLabelled)
		fmt.Fprintf(w, "  spot-checked      %d (random rows a human confirmed)\n", randomSpotChecked)
	}
	if boundary.Rows > 0 {
		fmt.Fprintf(w, "  boundary stratum  %d (labelled %d, ambiguous %d)\n", boundary.Rows, boundary.Labelled, boundary.Ambiguous)
		fmt.Fprintf(w, "  boundary pending  %d (boundary rows awaiting human confirmation)\n", boundary.Pending)
	}
	if vetoBoundary.Rows > 0 {
		fmt.Fprintf(w, "  veto-boundary     %d (labelled %d, ambiguous %d)\n", vetoBoundary.Rows, vetoBoundary.Labelled, vetoBoundary.Ambiguous)
		fmt.Fprintf(w, "  veto-b. pending   %d (veto-boundary rows awaiting human confirmation)\n", vetoBoundary.Pending)
	}
}

// stratumTally is one stratum's confirmation standing: the four numbers a maintainer
// reads after an apply.
type stratumTally struct{ Rows, Labelled, Ambiguous, Pending int }

// tallyStratum counts s's rows. Pending counts LABELLED rows with no human
// confirmer, so a freshly drawn and still unlabelled stratum reports 0 pending
// rather than every row -- the confirmation it is owed cannot be given before a
// label exists to confirm.
func tallyStratum(rows []goldRow, s goldStratum) stratumTally {
	var t stratumTally
	for _, row := range rows {
		if row.Stratum != s {
			continue
		}
		t.Rows++
		if row.Label.Valid() {
			t.Labelled++
		}
		if row.Label == bench.ExtractAmbiguous {
			t.Ambiguous++
		}
		if row.Label.Valid() && row.LabelProvenance.ConfirmedBy == "" {
			t.Pending++
		}
	}
	return t
}

// labelAgreement counts the rows where a human confirmer and the row's proposer can
// be compared at all -- a confirmed row that kept the label its proposer proposed --
// and how many of those agree. It is the measurement ADR-0048 asks a blind
// confirmation pass to produce: how often an independent human reaches the proposer's
// answer is the evidence for what the still-unconfirmed rows are worth. Rows confirmed
// before the Proposed Label existed carry none and are not comparable, so they are
// counted in neither figure rather than scored as agreement. ambiguous is compared like
// any other label: a proposer and a human who both found a page unresolvable agree.
func labelAgreement(rows []goldRow) (agreed, comparable int) {
	for _, row := range rows {
		prov := row.LabelProvenance
		if prov.ConfirmedBy == "" || prov.ProposedLabel == "" || !row.Label.Valid() {
			continue
		}
		comparable++
		if prov.ProposedLabel == row.Label {
			agreed++
		}
	}
	return agreed, comparable
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
