package main

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// hostBreadthCandidates builds a frame of candidates: n pages on each of hosts, at the
// given verdict, with capture lines numbered from 1 in the order produced. The URLs are
// deterministic so a test can assert on which page a reduction kept.
func hostBreadthCandidates(hosts []string, pagesPerHost int, verdict bool) []candidate {
	out := []candidate{}
	line := 0
	for _, host := range hosts {
		for i := 0; i < pagesPerHost; i++ {
			line++
			out = append(out, candidate{
				URL:     fmt.Sprintf("https://%s/jobs/%d", host, i),
				Verdict: verdict,
				TS:      "2026-08-22T17:00:00Z",
				Line:    line,
			})
		}
	}
	return out
}

// TestFramedByRendererTakesOneRendererAndNeverAssumesAnAbsentOne is ADR-0046's fence.
// The window this drawing was designed over flipped PARSE_STRUCTURAL_RENDERING part-way
// through, so the frame holds rows from two parsers; -since cannot separate them,
// because it is a floor and the new renderer is at the END of the window. A row carrying
// NO stamp predates #281 and its renderer is unknown, which is not the same as matching.
func TestFramedByRendererTakesOneRendererAndNeverAssumesAnAbsentOne(t *testing.T) {
	scan := captureScan{Candidates: []candidate{
		{URL: "https://a.test/1", Renderer: parser.RendererFlattened},
		{URL: "https://b.test/1", Renderer: parser.RendererStructural},
		{URL: "https://c.test/1", Renderer: parser.RendererFlattened},
		{URL: "https://d.test/1"},
	}}

	kept, dropped := framedByRenderer(scan, parser.RendererFlattened)
	if len(kept.Candidates) != 2 {
		t.Errorf("kept %d candidates, want the 2 stamped %s", len(kept.Candidates), parser.RendererFlattened)
	}
	if dropped != 2 {
		t.Errorf("dropped %d, want 2 (one other renderer, one unstamped)", dropped)
	}
	for _, c := range kept.Candidates {
		if c.Renderer != parser.RendererFlattened {
			t.Errorf("%s survived the fence carrying renderer %q", c.URL, c.Renderer)
		}
	}
}

// TestShippingRendererFollowsTheKillSwitch holds the drawing's default frame to the
// parser's own shipped behaviour rather than to a string in this file, so the day
// PARSE_STRUCTURAL_RENDERING's default flips the drawing follows it instead of silently
// fencing on the renderer nobody writes any more.
func TestShippingRendererFollowsTheKillSwitch(t *testing.T) {
	want := parser.RendererFlattened
	if parser.DefaultStructuralRendering {
		want = parser.RendererStructural
	}
	if got := shippingRenderer(); got != want {
		t.Errorf("shippingRenderer() = %q, want %q", got, want)
	}
}

// TestHostRepresentativesKeepsOnePagePerHostPerVerdict is the drawing's central claim:
// its sampling unit is a HOST, so the frame it draws from may carry each host at most
// once per verdict cell. A second page from one host would mean the quotas count hosts
// while the rows describe pages, and every weight over them would be wrong.
func TestHostRepresentativesKeepsOnePagePerHostPerVerdict(t *testing.T) {
	frame := append(
		hostBreadthCandidates([]string{"a.test", "b.test", "c.test"}, 4, true),
		hostBreadthCandidates([]string{"a.test", "b.test"}, 3, false)...,
	)

	reps, unparseable := hostRepresentatives(frame, "seed-v1")
	if unparseable != 0 {
		t.Errorf("unparseable = %d, want 0", unparseable)
	}
	// 3 hosts on the accept side + 2 on the abstain side, each represented once.
	if len(reps) != 5 {
		t.Fatalf("kept %d representatives, want 5", len(reps))
	}
	seen := map[string]int{}
	for _, r := range reps {
		seen[fmt.Sprintf("%s/%v", strings.Split(strings.TrimPrefix(r.URL, "https://"), "/")[0], r.Verdict)]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("%s is represented %d times, want 1", key, n)
		}
	}
}

// TestHostRepresentativesIsDeterministic holds the byte-reproducibility the whole
// drawing rests on: the same frame and seed must choose the same representatives
// whatever order the candidates arrive in, and a different seed must be a genuine
// resample rather than the same draw under another name.
func TestHostRepresentativesIsDeterministic(t *testing.T) {
	frame := hostBreadthCandidates([]string{"a.test", "b.test", "c.test", "d.test"}, 5, true)
	reversed := make([]candidate, len(frame))
	for i, c := range frame {
		reversed[len(frame)-1-i] = c
	}

	first, _ := hostRepresentatives(frame, "seed-v1")
	again, _ := hostRepresentatives(reversed, "seed-v1")
	if len(first) != len(again) {
		t.Fatalf("kept %d and %d representatives from the same frame", len(first), len(again))
	}
	for i := range first {
		if first[i].URL != again[i].URL {
			t.Errorf("representative %d is %q one way and %q the other; the choice must not depend on capture order", i, first[i].URL, again[i].URL)
		}
	}

	other, _ := hostRepresentatives(frame, "seed-v2")
	same := 0
	for i := range first {
		if first[i].URL == other[i].URL {
			same++
		}
	}
	if same == len(first) {
		t.Errorf("a different seed chose the same %d representatives; the seed is not keying the selection", same)
	}
}

// TestHostBreadthSelectionWeightsNormalizeToTheDrawnRows is the weighting contract every
// drawing in this file owes: each row carries the inverse of its own selection
// probability, and the drawing's weights sum to the drawing's OWN row count. A cell
// sampled at a different rate from its neighbour must therefore carry a different
// weight, which is the arithmetic that makes the two cells' quotas independent.
func TestHostBreadthSelectionWeightsNormalizeToTheDrawnRows(t *testing.T) {
	frame := append(
		hostBreadthCandidates(hostNames("accept", 100), 1, true),
		hostBreadthCandidates(hostNames("abstain", 400), 1, false)...,
	)
	plan := []cellPlan{{stratumHostBreadth, true, 50}, {stratumHostBreadth, false, 50}}

	sel, err := hostBreadthSelection(frame, plan, "seed-v1")
	if err != nil {
		t.Fatalf("hostBreadthSelection: %v", err)
	}
	if len(sel.Chosen) != 100 {
		t.Fatalf("chose %d rows, want 100", len(sel.Chosen))
	}

	total := 0.0
	byVerdict := map[bool]float64{}
	for _, c := range sel.Cells {
		total += c.Weight * float64(c.Sampled)
		byVerdict[c.Key.Verdict] = c.Weight
		if c.Key.Stratum != stratumHostBreadth {
			t.Errorf("cell %+v is in stratum %q, want %q", c.Key, c.Key.Stratum, stratumHostBreadth)
		}
	}
	if math.Abs(total-float64(len(sel.Chosen))) > 1e-9 {
		t.Errorf("weights sum to %.9f over %d rows; a drawing's weights must normalize to its own row count", total, len(sel.Chosen))
	}
	// The abstain cell is sampled at 50/400 against the accept cell's 50/100, so each
	// abstain row must stand for four times as many hosts.
	if ratio := byVerdict[false] / byVerdict[true]; math.Abs(ratio-4) > 1e-9 {
		t.Errorf("abstain/accept weight ratio = %.6f, want 4 (the cells are sampled at 50/400 and 50/100)", ratio)
	}
}

// TestHostBreadthSelectionTakesAWholeCellWhenTheQuotaDoesNotBind holds the convention
// every quota in this file shares: a cell smaller than its quota is taken whole and
// lands on the weight a census produces, so one arithmetic serves a bound and an
// unbound cell alike.
func TestHostBreadthSelectionTakesAWholeCellWhenTheQuotaDoesNotBind(t *testing.T) {
	frame := append(
		hostBreadthCandidates(hostNames("accept", 10), 1, true),
		hostBreadthCandidates(hostNames("abstain", 10), 1, false)...,
	)
	plan := []cellPlan{{stratumHostBreadth, true, 500}, {stratumHostBreadth, false, 400}}

	sel, err := hostBreadthSelection(frame, plan, "seed-v1")
	if err != nil {
		t.Fatalf("hostBreadthSelection: %v", err)
	}
	if len(sel.Chosen) != 20 {
		t.Errorf("chose %d rows from a 20-host frame, want all 20", len(sel.Chosen))
	}
	for _, c := range sel.Cells {
		if math.Abs(c.Weight-1) > 1e-9 {
			t.Errorf("cell %+v carries weight %.9f; a fully-taken cell weighs exactly 1", c.Key, c.Weight)
		}
	}
}

// TestHostBreadthSelectionRefusesAPlanItCannotWeight covers the two ways a caller can
// hand this drawing a design whose weights would be meaningless: a cell outside its own
// stratum, and a plan that does not cover both verdicts. Both are refusals rather than
// silent corrections, because either one produces a file whose weights cannot be
// reconstructed afterwards.
func TestHostBreadthSelectionRefusesAPlanItCannotWeight(t *testing.T) {
	frame := hostBreadthCandidates(hostNames("accept", 4), 1, true)

	for _, tc := range []struct {
		name string
		plan []cellPlan
	}{
		{"a foreign stratum", []cellPlan{{stratumRandom, true, 2}, {stratumHostBreadth, false, 2}}},
		{"only one verdict", []cellPlan{{stratumHostBreadth, true, 2}}},
		{"a duplicate cell", []cellPlan{{stratumHostBreadth, true, 2}, {stratumHostBreadth, true, 3}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := hostBreadthSelection(frame, tc.plan, "seed-v1"); err == nil {
				t.Error("the plan was accepted; a design whose weights cannot be reconstructed must be refused")
			}
		})
	}
}

// TestHostBreadthSelectionRefusesAnEmptyFrame is the guard on the honest failure: a
// frame with no host in it weights nothing, and returning an empty selection would write
// an empty drawing rather than say so.
func TestHostBreadthSelectionRefusesAnEmptyFrame(t *testing.T) {
	if _, err := hostBreadthSelection(nil, hostBreadthSamplePlan, "seed-v1"); err == nil {
		t.Error("an empty frame was accepted; there is nothing to weight")
	}
}

// TestValidateDrawnHostBreadthRowsRefusesACorruptDraw runs the pre-write guard over
// every corruption it exists to stop. Nothing is written until it passes, so each of
// these is the difference between a bad draw and a bad committed record.
func TestValidateDrawnHostBreadthRowsRefusesACorruptDraw(t *testing.T) {
	sel := selection{Cells: []cellResult{
		{Key: cellKey{Stratum: stratumHostBreadth, Verdict: true}, Population: 2, Sampled: 2, Weight: 1},
		{Key: cellKey{Stratum: stratumHostBreadth, Verdict: false}, Population: 2, Sampled: 2, Weight: 1},
	}}
	good := []goldRow{
		{URL: "https://a.test/jobs/1", Verdict: true, Stratum: stratumHostBreadth, Weight: 1},
		{URL: "https://b.test/jobs/1", Verdict: true, Stratum: stratumHostBreadth, Weight: 1},
		{URL: "https://c.test/jobs/1", Verdict: false, Stratum: stratumHostBreadth, Weight: 1},
		{URL: "https://d.test/jobs/1", Verdict: false, Stratum: stratumHostBreadth, Weight: 1},
	}
	if err := validateDrawnHostBreadthRows(sel, good, map[string]struct{}{}); err != nil {
		t.Fatalf("a well-formed draw was refused: %v", err)
	}

	corrupt := func(mutate func(rows []goldRow) []goldRow) []goldRow {
		rows := append([]goldRow{}, good...)
		return mutate(rows)
	}
	for _, tc := range []struct {
		name      string
		rows      []goldRow
		committed map[string]struct{}
		want      string
	}{
		{
			name:      "a page the substrate already carries",
			rows:      good,
			committed: map[string]struct{}{"https://b.test/jobs/1": {}},
			want:      "already carries",
		},
		{
			name: "the same page twice",
			rows: corrupt(func(rows []goldRow) []goldRow { rows[1].URL = rows[0].URL; return rows }),
			want: "twice",
		},
		{
			name: "a second page from one host",
			rows: corrupt(func(rows []goldRow) []goldRow { rows[1].URL = "https://a.test/jobs/2"; return rows }),
			want: "samples HOSTS",
		},
		{
			name: "a row outside the stratum",
			rows: corrupt(func(rows []goldRow) []goldRow { rows[2].Stratum = stratumRandom; return rows }),
			want: "want \"host-breadth\"",
		},
		{
			name: "a weight no cell produced",
			rows: corrupt(func(rows []goldRow) []goldRow { rows[3].Weight = 0.5; return rows }),
			want: "keyed wrong",
		},
		{
			name: "a row that arrived labelled",
			rows: corrupt(func(rows []goldRow) []goldRow { rows[0].Label = "detail"; return rows }),
			want: "UNLABELLED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			committed := tc.committed
			if committed == nil {
				committed = map[string]struct{}{}
			}
			err := validateDrawnHostBreadthRows(sel, tc.rows, committed)
			if err == nil {
				t.Fatal("the draw was accepted; it would have corrupted the committed record")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name the corruption (%q)", err, tc.want)
			}
		})
	}
}

// TestHostBreadthSameHostMayRepresentBothVerdicts records a deliberate choice rather
// than an accident: the cluster is (host, verdict), so a host publishing both an
// accepted and an abstained page represents itself once in each cell. Pooling the two
// would make one cell's quota silently change the other's weights.
func TestHostBreadthSameHostMayRepresentBothVerdicts(t *testing.T) {
	frame := append(
		hostBreadthCandidates([]string{"a.test"}, 3, true),
		hostBreadthCandidates([]string{"a.test"}, 3, false)...,
	)
	reps, _ := hostRepresentatives(frame, "seed-v1")
	if len(reps) != 2 {
		t.Fatalf("kept %d representatives for one host across two verdicts, want 2", len(reps))
	}
	if reps[0].Verdict == reps[1].Verdict {
		t.Error("both representatives carry the same verdict; the cluster is (host, verdict)")
	}
}

// TestPrintHostBreadthSummaryStatesThePreExclusionCaveat holds the honesty of the
// report mode's own numbers: it never opens the gold set, so its host populations still
// count pages an earlier drawing holds, and the summary has to say so where an operator
// reads the figure rather than only in a doc comment.
func TestPrintHostBreadthSummaryStatesThePreExclusionCaveat(t *testing.T) {
	var buf bytes.Buffer
	printHostBreadthSummary(&buf, hostBreadthSummary{
		Scan: captureScan{Lines: 10},
		Sel:  selection{Cells: []cellResult{{Key: cellKey{Stratum: stratumHostBreadth, Verdict: true}, Population: 3, Sampled: 3, Weight: 1}}},
	})
	out := buf.String()
	if !strings.Contains(out, "PRE-exclusion") {
		t.Errorf("the report mode's summary does not state that its populations are pre-exclusion:\n%s", out)
	}
	if !strings.Contains(out, "wrote                nothing") {
		t.Errorf("the report mode's summary does not say it wrote nothing:\n%s", out)
	}
}

// hostNames builds n distinct host names under one prefix.
func hostNames(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("%s-%d.test", prefix, i)
	}
	return out
}
