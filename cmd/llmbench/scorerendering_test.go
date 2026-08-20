package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// The two-armed floors below are the A/B's non-vacuity tripwires. Without them
// "the arms agree" passes trivially the moment the renderer switch stops working
// and both parsers hand back the same bytes. Measured today at exactly 85 of 90 and
// 26 of 26; the five classifier fixtures that match under both renderings are
// JS-shell boards whose main region is all but empty, so there is no structure to
// render.
const (
	realPagesTwoArmedFloor        = 85
	labelledFixturesTwoArmedFloor = 26
)

// TestRenderingArmsAgreeOverCommittedPages is the A/B's guard, and it lives in
// `go test` rather than in the verb because nobody runs a benchmark verb in CI --
// the same reason TestExtractGate_CommittedSetNoFalseDrop exists beside the extract
// verb.
//
// The Extract Gate reads no page text except through crawler.FlattenedText, whose
// round-trip onto today's bytes the parser asserts byte for byte, so the two
// renderings MUST decide identically. A failure here is a defect in the renderer or
// in a derivation and the fix belongs there: never in this test, and never in a
// label.
func TestRenderingArmsAgreeOverCommittedPages(t *testing.T) {
	cfg := crawler.DefaultLLMGateConfig()

	tests := []struct {
		name           string
		dir            string
		loadRefs       func(fs.FS) ([]fixtureRef, error)
		twoArmedFloor  int
		expectLabelled bool
	}{
		{
			name:           "extract-labelled fixtures",
			dir:            "extract-testdata",
			loadRefs:       extractFixtureRefs,
			twoArmedFloor:  labelledFixturesTwoArmedFloor,
			expectLabelled: true,
		},
		{
			name:          "real classifier fixtures",
			dir:           "testdata",
			loadRefs:      classifierFixtureRefs,
			twoArmedFloor: realPagesTwoArmedFloor,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := replayRenderingFixtures(os.DirFS(tt.dir), tt.loadRefs, cfg, 8000)
			if err != nil {
				t.Fatalf("replayRenderingFixtures(%s): %v", tt.dir, err)
			}
			if len(rows) == 0 {
				t.Fatalf("replayRenderingFixtures(%s) returned no rows", tt.dir)
			}

			twoArmed, labelled := 0, 0
			for _, row := range rows {
				if row.TwoArmed {
					twoArmed++
				}
				if row.Label.Valid() {
					labelled++
				}
				if row.Flat.Extract != row.Struct.Extract {
					t.Errorf("%s: the two renderings disagree (flattened extract=%v rung=%q, structural extract=%v rung=%q); "+
						"the Extract Gate is invariant to the rendering by construction, so the fix belongs in the renderer or in crawler.FlattenedText, never in this test",
						row.URL, row.Flat.Extract, row.Flat.Rung, row.Struct.Extract, row.Struct.Rung)
				}
			}
			if twoArmed < tt.twoArmedFloor {
				t.Errorf("%s: only %d of %d rows carry different bytes in the two arms, want at least %d; "+
					"the agreement above is vacuous if the renderer switch is not doing anything",
					tt.dir, twoArmed, len(rows), tt.twoArmedFloor)
			}
			if tt.expectLabelled && labelled == 0 {
				t.Fatalf("%s: no row carries an extract label, so no false-drop set can be compared", tt.dir)
			}

			// The false-drop SETS, not their counts: comparing totals would miss a swap,
			// one posting starting to drop as another stops.
			flat := falseDropSet(t, rows, func(r renderingRow) renderingArm { return r.Flat })
			structural := falseDropSet(t, rows, func(r renderingRow) renderingArm { return r.Struct })
			for url := range flat {
				if !structural[url] {
					t.Errorf("%s is a false-drop under Flattened Text and not under the Structural Rendering", url)
				}
			}
			for url := range structural {
				if !flat[url] {
					t.Errorf("%s is a false-drop under the Structural Rendering and not under Flattened Text", url)
				}
			}
		})
	}
}

// TestMatchedBudgetBoundsBothArms is the matched-budget property: one budget,
// applied identically to both arms, so no arm can look better by having been shown
// more of the page. The budget is deliberately small enough to bite -- a cap that
// never engages would prove nothing.
func TestMatchedBudgetBoundsBothArms(t *testing.T) {
	const budget = 200

	rows, err := replayRenderingFixtures(os.DirFS("testdata"), classifierFixtureRefs, crawler.DefaultLLMGateConfig(), budget)
	if err != nil {
		t.Fatalf("replayRenderingFixtures: %v", err)
	}

	truncatedFlat, truncatedStruct := 0, 0
	for _, row := range rows {
		if row.Flat.PromptRunes > budget {
			t.Errorf("%s: flattened arm prompt is %d runes, over the %d-rune budget", row.URL, row.Flat.PromptRunes, budget)
		}
		if row.Struct.PromptRunes > budget {
			t.Errorf("%s: structural arm prompt is %d runes, over the %d-rune budget", row.URL, row.Struct.PromptRunes, budget)
		}
		if row.Flat.Truncated {
			truncatedFlat++
		}
		if row.Struct.Truncated {
			truncatedStruct++
		}
	}
	if truncatedFlat == 0 || truncatedStruct == 0 {
		t.Errorf("the %d-rune budget truncated %d flattened and %d structural rows; a cap that never engages proves nothing about matching",
			budget, truncatedFlat, truncatedStruct)
	}
}

// TestGoldSetLegScoresAStructuralRowInBothArms drives the gold-set leg over a
// two-line file: one row drawn before the renderer stamp existed and one stamped
// structural-v1.
//
// It is what makes that leg a real code path rather than theatre. The 457 committed
// rows are all unstamped, so today the leg reports a derived zero; the seam starts
// working by itself the moment a harvest under PARSE_STRUCTURAL_RENDERING produces
// a stamped row, and this test is the proof that it will.
func TestGoldSetLegScoresAStructuralRowInBothArms(t *testing.T) {
	const structuralURL = "https://example.com/jobs/senior-go-engineer-4711"
	const flatURL = "https://example.com/jobs/staff-platform-engineer-4712"

	structural, err := parser.NewHTMLParser(parser.WithStructuralRendering(true)).Parse([]byte(`
		<html><body><main>
			<h1>Senior Go Engineer</h1>
			<p>We are hiring in Berlin.</p>
			<ul><li>Own the crawler</li><li>Review designs</li></ul>
			<a href="/apply/senior-go-engineer">Apply now</a>
		</main></body></html>`))
	if err != nil {
		t.Fatalf("parse structural fixture: %v", err)
	}

	path := filepath.Join(t.TempDir(), "goldset.jsonl")
	if err := writeGoldSet(path, []goldRow{
		{
			URL:     flatURL,
			Label:   bench.ExtractDetail,
			Content: crawler.Content{Title: "Staff Platform Engineer", MainContent: "Staff Platform Engineer We are hiring in Berlin Apply now"},
		},
		{
			URL:      structuralURL,
			Renderer: parser.RendererStructural,
			Label:    bench.ExtractDetail,
			Content:  *structural,
		},
	}); err != nil {
		t.Fatalf("write gold set: %v", err)
	}

	rows, census, skipped, err := replayRenderingGoldSet(path, crawler.DefaultLLMGateConfig(), 8000)
	if err != nil {
		t.Fatalf("replayRenderingGoldSet: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0: both rows carry a valid label", skipped)
	}
	if census[parser.RendererStructural] != 1 || census[rendererUnstamped] != 1 {
		t.Errorf("renderer census = %v, want one structural-v1 and one unstamped", census)
	}

	byURL := map[string]renderingRow{}
	for _, row := range rows {
		byURL[row.URL] = row
	}
	if len(byURL) != 2 {
		t.Fatalf("measured %d rows, want 2", len(byURL))
	}

	got := byURL[structuralURL]
	if !got.TwoArmed {
		t.Errorf("%s: TwoArmed = false, want true: a structural-v1 row's stored bytes and their reduction differ", structuralURL)
	}
	if got.Flat.Extract != got.Struct.Extract {
		t.Errorf("%s: the two arms disagree (flattened %v rung %q, structural %v rung %q)",
			structuralURL, got.Flat.Extract, got.Flat.Rung, got.Struct.Extract, got.Struct.Rung)
	}
	if got.Struct.PromptRunes <= got.Flat.PromptRunes {
		t.Errorf("%s: structural prompt is %d runes against the flattened arm's %d; structure has to cost budget",
			structuralURL, got.Struct.PromptRunes, got.Flat.PromptRunes)
	}

	// The pre-stamp row is the honest zero: FlattenedText is idempotent, so its two
	// arms read the same bytes and the leg says so rather than hiding it.
	pre := byURL[flatURL]
	if pre.TwoArmed {
		t.Errorf("%s: TwoArmed = true, want false: an unstamped row reduces to itself", flatURL)
	}
	if pre.Flat.PromptRunes != pre.Struct.PromptRunes {
		t.Errorf("%s: arms differ in size (%d vs %d runes) although the row is not two-armed",
			flatURL, pre.Flat.PromptRunes, pre.Struct.PromptRunes)
	}
}

// TestRenderingReportNamesBothArmsAndTheDelta pins the report's contract: per arm
// the extract-call rate and the false-drop count, the delta between them, the
// budget both arms ran at, and every URL the arms disagreed on.
func TestRenderingReportNamesBothArmsAndTheDelta(t *testing.T) {
	const flipped = "https://example.com/jobs/flipped-1"

	block := scoreRenderingBlock("hand-built", []renderingRow{
		{
			URL:       flipped,
			Label:     bench.ExtractDetail,
			PageWords: 100,
			TwoArmed:  true,
			Flat:      renderingArm{Extract: true, PromptRunes: 100, PageWords: 100},
			Struct:    renderingArm{Extract: false, Rung: "positive_evidence", PromptRunes: 110, PageWords: 95},
		},
		{
			URL:       "https://example.com/jobs/steady-2",
			Label:     bench.ExtractDetail,
			PageWords: 50,
			TwoArmed:  true,
			Flat:      renderingArm{Extract: true, PromptRunes: 50, PageWords: 50},
			Struct:    renderingArm{Extract: true, PromptRunes: 55, PageWords: 48},
		},
	})

	var buf bytes.Buffer
	printRenderingReport(&buf, renderingReport{Budget: 8000, LabelledFixtures: &block})
	out := buf.String()

	for _, want := range []string{
		"8000 runes",
		"arm flattened     extract-calls 2  rate 1.0000  false-drops 0",
		"arm structural    extract-calls 1  rate 0.5000  false-drops 1",
		"delta             extract-call-rate -0.5000   false-drops +1",
		"arm-flip          " + flipped,
		"drop-drift        " + flipped,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report does not state %q\n--- report ---\n%s", want, out)
		}
	}
}

// TestArmDisagreementIsTheOnlyFailure pins the exit rule: this verb fails on the
// DIFFERENCE between the arms and never on their level. score-capture already exits
// 1 over the committed gold set on 13 tolerated false-drops; a verb that inherited
// that rule would be red on day one for reasons that have nothing to do with the
// rendering, and the A/B's finding would be invisible underneath it.
func TestArmDisagreementIsTheOnlyFailure(t *testing.T) {
	tests := []struct {
		name             string
		rows             []renderingRow
		wantFlips        []string
		wantDriftedDrops []string
	}{
		{
			name: "identical arms",
			rows: []renderingRow{
				{URL: "a", Label: bench.ExtractDetail, Flat: renderingArm{Extract: true}, Struct: renderingArm{Extract: true}},
				{URL: "b", Label: bench.ExtractHubIndex, Flat: renderingArm{}, Struct: renderingArm{}},
			},
			wantFlips:        []string{},
			wantDriftedDrops: []string{},
		},
		{
			name: "a flip on an unlabelled page is still a flip",
			rows: []renderingRow{
				{URL: "a", Flat: renderingArm{Extract: true}, Struct: renderingArm{}},
			},
			wantFlips:        []string{"a"},
			wantDriftedDrops: []string{},
		},
		{
			name: "the same non-zero false-drop set in both arms is not a failure",
			rows: []renderingRow{
				{URL: "a", Label: bench.ExtractDetail, Flat: renderingArm{}, Struct: renderingArm{}},
				{URL: "b", Label: bench.ExtractDetail, Flat: renderingArm{}, Struct: renderingArm{}},
			},
			wantFlips:        []string{},
			wantDriftedDrops: []string{},
		},
		{
			name: "equal drop counts, different pages, is a drift",
			rows: []renderingRow{
				{URL: "a", Label: bench.ExtractDetail, Flat: renderingArm{}, Struct: renderingArm{Extract: true}},
				{URL: "b", Label: bench.ExtractDetail, Flat: renderingArm{Extract: true}, Struct: renderingArm{}},
			},
			wantFlips:        []string{"a", "b"},
			wantDriftedDrops: []string{"a", "b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flips, drifted := armsDisagree(tt.rows)
			if !equalStrings(flips, tt.wantFlips) {
				t.Errorf("flips = %v, want %v", flips, tt.wantFlips)
			}
			if !equalStrings(drifted, tt.wantDriftedDrops) {
				t.Errorf("driftedDrops = %v, want %v", drifted, tt.wantDriftedDrops)
			}

			report := renderingReport{LabelledFixtures: &renderingBlock{Flips: flips, DriftedDrops: drifted}}
			wantFailed := len(tt.wantFlips) > 0 || len(tt.wantDriftedDrops) > 0
			if report.Failed() != wantFailed {
				t.Errorf("Failed() = %v, want %v", report.Failed(), wantFailed)
			}
		})
	}
}

// falseDropSet folds one arm's rows through the real scorer, so this test and
// score-capture agree on what a false-drop is by construction.
func falseDropSet(t *testing.T, rows []renderingRow, pick func(renderingRow) renderingArm) map[string]bool {
	t.Helper()
	verdicts := []bench.ExtractVerdictRow{}
	for _, row := range rows {
		if !row.Label.Valid() {
			continue
		}
		verdicts = append(verdicts, bench.ExtractVerdictRow{URL: row.URL, Label: row.Label, Extract: pick(row).Extract})
	}
	set := map[string]bool{}
	if len(verdicts) == 0 {
		return set
	}
	for _, url := range bench.ScoreExtract(verdicts).Extract.FalseDrops {
		set[url] = true
	}
	return set
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
