package bench_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// boundaryRow builds one Boundary Stratum row. The weight is deliberately left at
// zero throughout this file: the boundary scorecard is unweighted by design, so a
// test that had to supply weights would be testing something else.
func boundaryRow(url string, label bench.ExtractLabel, extract, confirmed bool) bench.BoundaryRow {
	return bench.BoundaryRow{
		ExtractVerdictRow: bench.ExtractVerdictRow{URL: url, Label: label, Extract: extract},
		Confirmed:         confirmed,
	}
}

// TestScoreExtractBoundaryFoldsTheDisagreement pins what the boundary scorecard
// reports: the label mix it was measured over, what this config does to those pages,
// the Job Listings it drops -- the number a hard-zero guard turns on -- and how many
// of the labels behind that number a human has actually signed off.
func TestScoreExtractBoundaryFoldsTheDisagreement(t *testing.T) {
	sc := bench.ScoreExtractBoundary([]bench.BoundaryRow{
		boundaryRow("https://a.test/dropped-posting", bench.ExtractDetail, false, true),
		boundaryRow("https://b.test/dropped-posting", bench.ExtractDetail, false, false),
		boundaryRow("https://c.test/kept-posting", bench.ExtractDetail, true, true),
		boundaryRow("https://d.test/hub", bench.ExtractHubIndex, false, true),
		boundaryRow("https://e.test/about", bench.ExtractResidue, false, false),
		boundaryRow("https://f.test/unclear", bench.ExtractAmbiguous, false, false),
	})

	if sc.Rows != 6 {
		t.Errorf("Rows = %d, want 6", sc.Rows)
	}
	wantCounts := map[bench.ExtractLabel]int{
		bench.ExtractDetail: 3, bench.ExtractHubIndex: 1, bench.ExtractResidue: 1, bench.ExtractAmbiguous: 1,
	}
	if !reflect.DeepEqual(sc.Counts, wantCounts) {
		t.Errorf("Counts = %v, want %v", sc.Counts, wantCounts)
	}
	if sc.Extracted != 1 || sc.Skipped != 5 {
		t.Errorf("extracted %d / skipped %d, want 1 / 5", sc.Extracted, sc.Skipped)
	}
	wantDrops := []string{"https://a.test/dropped-posting", "https://b.test/dropped-posting"}
	if !reflect.DeepEqual(sc.FalseDrops, wantDrops) {
		t.Errorf("FalseDrops = %v, want %v (input order, the postings this config drops)", sc.FalseDrops, wantDrops)
	}
	if sc.AmbiguousSkipped != 1 {
		t.Errorf("AmbiguousSkipped = %d, want 1", sc.AmbiguousSkipped)
	}
	if sc.Confirmed != 3 || sc.Unconfirmed != 3 {
		t.Errorf("confirmed %d / unconfirmed %d, want 3 / 3", sc.Confirmed, sc.Unconfirmed)
	}
}

// TestScoreExtractBoundaryNeverCallsAnAmbiguousPageAFalseDrop is the acceptance
// criterion in the scorer: a page nobody could classify must become neither a
// false-drop nor a page the gate wrongly extracted, in EITHER direction of the
// decision. It is counted and named as unclassifiable instead.
func TestScoreExtractBoundaryNeverCallsAnAmbiguousPageAFalseDrop(t *testing.T) {
	for name, extract := range map[string]bool{"skipped": false, "extracted": true} {
		sc := bench.ScoreExtractBoundary([]bench.BoundaryRow{
			boundaryRow("https://a.test/unclear", bench.ExtractAmbiguous, extract, false),
		})
		if len(sc.FalseDrops) != 0 {
			t.Errorf("%s: an ambiguous page was counted as a false-drop: %v", name, sc.FalseDrops)
		}
		if sc.Counts[bench.ExtractAmbiguous] != 1 {
			t.Errorf("%s: the ambiguous row was not counted at all", name)
		}
	}
}

// TestScoreExtractBoundaryInitializesEveryCell keeps an empty cell visible rather
// than absent, so a reader of the JSON can tell "no residue rows" from "residue not
// reported", and so the report round-trips.
func TestScoreExtractBoundaryInitializesEveryCell(t *testing.T) {
	sc := bench.ScoreExtractBoundary(nil)
	for _, label := range append(append([]bench.ExtractLabel{}, bench.AllExtractLabels...), bench.ExtractAmbiguous) {
		if _, ok := sc.Counts[label]; !ok {
			t.Errorf("Counts has no cell for %q", label)
		}
	}
	if sc.FalseDrops == nil {
		t.Error("FalseDrops is nil; it must marshal as [] rather than null")
	}
}

// TestExtractReportRoundTripsTheBoundaryBlock proves the new block survives the
// -json surface, which is what a reviewer diffs two runs of.
func TestExtractReportRoundTripsTheBoundaryBlock(t *testing.T) {
	sc := bench.ScoreExtractBoundary([]bench.BoundaryRow{
		boundaryRow("https://a.test/dropped-posting", bench.ExtractDetail, false, true),
		boundaryRow("https://b.test/unclear", bench.ExtractAmbiguous, false, false),
	})
	report := bench.ExtractReport{Extract: bench.ScoreExtract(nil).Extract, Boundary: &sc}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back bench.ExtractReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Boundary == nil {
		t.Fatal("the boundary block did not survive the round trip")
	}
	if !reflect.DeepEqual(*back.Boundary, sc) {
		t.Errorf("round-tripped boundary block = %+v, want %+v", *back.Boundary, sc)
	}

	// An absent block must stay absent: the synthetic fixture set draws no boundary,
	// and a zeroed block there would read as "measured, and empty".
	plain, err := json.Marshal(bench.ExtractReport{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(plain), "boundary") {
		t.Errorf("a report with no boundary stratum still emitted the block: %s", plain)
	}
}
