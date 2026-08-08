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
// included -- is withheld, and rows are emitted in hash-shuffled order, so a label
// cannot be inferred from the very signal the gold set exists to test the
// mechanism against (ADR-0043). The worksheet is a working artifact and is never
// committed. Exit: 2 on usage, 1 on IO, 0 otherwise.
func runGoldSetWorksheet(args []string) int {
	fs := flag.NewFlagSet("goldset-worksheet", flag.ExitOnError)
	dir := fs.String("dir", defaultGoldSetDir, "directory holding the Extract Gold Set")
	out := fs.String("out", "", "path to write the worksheet JSONL to (required; a working artifact, never committed)")
	seed := fs.String("seed", defaultSeed, "seed for the presentation shuffle")
	_ = fs.Parse(args)

	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench goldset-worksheet -out <worksheet.jsonl> [-dir d] [-seed s]")
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
		sheets = append(sheets, worksheetFor(row))
	}
	sort.Slice(sheets, func(i, j int) bool {
		return seededHash(*seed, sheets[i].URL) < seededHash(*seed, sheets[j].URL)
	})

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
		if s.ConfirmedBy != "" {
			merged[i].LabelProvenance.ConfirmedBy = s.ConfirmedBy
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
func printApplySummary(w io.Writer, rows []goldRow) {
	byLabel := map[bench.ExtractLabel]int{}
	labelled, proposedOnly, pending := 0, 0, 0
	expected, acceptedFires, expectedPending := 0, 0, 0
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
