package bench_test

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// cs builds a ClassScore with its derived rates spelled out, so the expected
// scorecard reads as a literal confusion matrix plus the rates ScoreExtract must
// produce.
func cs(tp, fp, fn, tn int, precision, recall, accuracy, f1 float64) bench.ClassScore {
	return bench.ClassScore{
		TP: tp, FP: fp, FN: fn, TN: tn, Total: tp + fp + fn + tn,
		Precision: precision, Recall: recall, Accuracy: accuracy, F1: f1,
	}
}

func TestExtractLabel(t *testing.T) {
	tests := []struct {
		label    bench.ExtractLabel
		valid    bool
		positive bool
	}{
		{bench.ExtractDetail, true, true},
		{bench.ExtractHubIndex, true, false},
		{bench.ExtractResidue, true, false},
		{bench.ExtractLabel("nonsense"), false, false},
		{bench.ExtractLabel(""), false, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.label), func(t *testing.T) {
			if got := tt.label.Valid(); got != tt.valid {
				t.Errorf("Valid() = %v, want %v", got, tt.valid)
			}
			if got := tt.label.Positive(); got != tt.positive {
				t.Errorf("Positive() = %v, want %v", got, tt.positive)
			}
		})
	}
}

// TestScoreExtract exercises the pure extract scorer: the binary collapse
// (hub-index + residue both count as leaks when extracted), the per-class slices,
// the false-drop that forces a non-zero result, the soft extract-call rate, and
// the residue counts. It mirrors the classifier scorer_test's field-by-field
// assertion style.
func TestScoreExtract(t *testing.T) {
	detail := func(url string, extract bool) bench.ExtractVerdictRow {
		return bench.ExtractVerdictRow{URL: url, Label: bench.ExtractDetail, Extract: extract}
	}
	hub := func(url string, extract bool) bench.ExtractVerdictRow {
		return bench.ExtractVerdictRow{URL: url, Label: bench.ExtractHubIndex, Extract: extract}
	}
	residue := func(url string, extract bool) bench.ExtractVerdictRow {
		return bench.ExtractVerdictRow{URL: url, Label: bench.ExtractResidue, Extract: extract}
	}

	tests := []struct {
		name             string
		rows             []bench.ExtractVerdictRow
		total            int
		extractCalls     int
		extractCallRate  float64
		falseDrops       []string
		leaks            []string
		overall          bench.ClassScore
		byClass          map[bench.ExtractLabel]bench.ClassScore
		residueCount     int
		residueExtracted int
		failed           bool
	}{
		{
			name:            "empty",
			rows:            []bench.ExtractVerdictRow{},
			total:           0,
			extractCalls:    0,
			extractCallRate: 0,
			falseDrops:      []string{},
			leaks:           []string{},
			overall:         cs(0, 0, 0, 0, 0, 0, 0, 0),
			byClass:         map[bench.ExtractLabel]bench.ClassScore{},
			failed:          false,
		},
		{
			name: "clean-detail-extract-negatives-skip",
			rows: []bench.ExtractVerdictRow{
				detail("d", true),
				hub("h", false),
				residue("r", false),
			},
			total:           3,
			extractCalls:    1,
			extractCallRate: 0.3333,
			falseDrops:      []string{},
			leaks:           []string{},
			overall:         cs(1, 0, 0, 2, 1, 1, 1, 1),
			byClass: map[bench.ExtractLabel]bench.ClassScore{
				bench.ExtractDetail:   cs(1, 0, 0, 0, 1, 1, 1, 1),
				bench.ExtractHubIndex: cs(0, 0, 0, 1, 0, 0, 1, 0),
				bench.ExtractResidue:  cs(0, 0, 0, 1, 0, 0, 1, 0),
			},
			residueCount:     1,
			residueExtracted: 0,
			failed:           false,
		},
		{
			name: "binary-collapse-both-negatives-leak",
			rows: []bench.ExtractVerdictRow{
				hub("h-leak", true),
				hub("h-skip", false),
				residue("r-leak", true),
				residue("r-skip", false),
			},
			total:           4,
			extractCalls:    2,
			extractCallRate: 0.5,
			falseDrops:      []string{},
			leaks:           []string{"h-leak", "r-leak"},
			overall:         cs(0, 2, 0, 2, 0, 0, 0.5, 0),
			byClass: map[bench.ExtractLabel]bench.ClassScore{
				bench.ExtractHubIndex: cs(0, 1, 0, 1, 0, 0, 0.5, 0),
				bench.ExtractResidue:  cs(0, 1, 0, 1, 0, 0, 0.5, 0),
			},
			residueCount:     2,
			residueExtracted: 1,
			failed:           false,
		},
		{
			name:            "false-drop-forces-non-zero",
			rows:            []bench.ExtractVerdictRow{detail("d-drop", false)},
			total:           1,
			extractCalls:    0,
			extractCallRate: 0,
			falseDrops:      []string{"d-drop"},
			leaks:           []string{},
			overall:         cs(0, 0, 1, 0, 0, 0, 0, 0),
			byClass: map[bench.ExtractLabel]bench.ClassScore{
				bench.ExtractDetail: cs(0, 0, 1, 0, 0, 0, 0, 0),
			},
			residueCount:     0,
			residueExtracted: 0,
			failed:           true,
		},
		{
			name: "per-class-mixed-quadrants",
			rows: []bench.ExtractVerdictRow{
				detail("d-ok", true),
				detail("d-drop", false),
				hub("h-leak", true),
				hub("h-skip", false),
				residue("r-leak", true),
				residue("r-skip", false),
			},
			total:           6,
			extractCalls:    3,
			extractCallRate: 0.5,
			falseDrops:      []string{"d-drop"},
			leaks:           []string{"h-leak", "r-leak"},
			overall:         cs(1, 2, 1, 2, 0.3333, 0.5, 0.5, 0.4),
			byClass: map[bench.ExtractLabel]bench.ClassScore{
				bench.ExtractDetail:   cs(1, 0, 1, 0, 1, 0.5, 0.5, 0.6667),
				bench.ExtractHubIndex: cs(0, 1, 0, 1, 0, 0, 0.5, 0),
				bench.ExtractResidue:  cs(0, 1, 0, 1, 0, 0, 0.5, 0),
			},
			residueCount:     2,
			residueExtracted: 1,
			failed:           true,
		},
		{
			name: "soft-rate-one-third-never-fails",
			rows: []bench.ExtractVerdictRow{
				residue("r-leak", true),
				residue("r-skip-1", false),
				residue("r-skip-2", false),
			},
			total:           3,
			extractCalls:    1,
			extractCallRate: 0.3333,
			falseDrops:      []string{},
			leaks:           []string{"r-leak"},
			overall:         cs(0, 1, 0, 2, 0, 0, 0.6667, 0),
			byClass: map[bench.ExtractLabel]bench.ClassScore{
				bench.ExtractResidue: cs(0, 1, 0, 2, 0, 0, 0.6667, 0),
			},
			residueCount:     3,
			residueExtracted: 1,
			failed:           false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.ScoreExtract(tt.rows)
			e := got.Extract
			if e.Total != tt.total {
				t.Errorf("Total = %d, want %d", e.Total, tt.total)
			}
			if e.ExtractCalls != tt.extractCalls {
				t.Errorf("ExtractCalls = %d, want %d", e.ExtractCalls, tt.extractCalls)
			}
			if e.ExtractCallRate != tt.extractCallRate {
				t.Errorf("ExtractCallRate = %v, want %v", e.ExtractCallRate, tt.extractCallRate)
			}
			if !reflect.DeepEqual(e.FalseDrops, tt.falseDrops) {
				t.Errorf("FalseDrops = %v, want %v", e.FalseDrops, tt.falseDrops)
			}
			if !reflect.DeepEqual(e.Leaks, tt.leaks) {
				t.Errorf("Leaks = %v, want %v", e.Leaks, tt.leaks)
			}
			if !reflect.DeepEqual(e.Overall, tt.overall) {
				t.Errorf("Overall = %+v, want %+v", e.Overall, tt.overall)
			}
			if !reflect.DeepEqual(e.ByClass, tt.byClass) {
				t.Errorf("ByClass = %+v, want %+v", e.ByClass, tt.byClass)
			}
			if e.ResidueCount != tt.residueCount {
				t.Errorf("ResidueCount = %d, want %d", e.ResidueCount, tt.residueCount)
			}
			if e.ResidueExtracted != tt.residueExtracted {
				t.Errorf("ResidueExtracted = %d, want %d", e.ResidueExtracted, tt.residueExtracted)
			}
			if got.Failed() != tt.failed {
				t.Errorf("Failed() = %v, want %v", got.Failed(), tt.failed)
			}
		})
	}
}

// TestLoadExtractManifest checks the single-field-labelled loader: a valid set
// parses, and every structural fault wraps ErrInvalidManifest (malformed JSON is
// an error but need not be the sentinel, mirroring the classifier case).
func TestLoadExtractManifest(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
		invalid bool
	}{
		{
			name: "valid-three-entries",
			json: `[
				{"file":"a.html","url":"https://job-boards.greenhouse.io/acme/jobs/1","label":"detail","verified":true},
				{"file":"b.html","url":"https://acme.example/careers","label":"hub-index","verified":true},
				{"file":"c.html","url":"https://acme.example/about/culture","label":"residue","verified":false}
			]`,
		},
		{
			name:    "empty-file",
			json:    `[{"file":"","url":"https://x.example","label":"detail"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "empty-url",
			json:    `[{"file":"a.html","url":"","label":"detail"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "unknown-label",
			json:    `[{"file":"a.html","url":"https://x.example","label":"maybe"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "classifier-label-rejected",
			json:    `[{"file":"a.html","url":"https://x.example","label":"career_page"}]`,
			wantErr: true,
			invalid: true,
		},
		{
			name: "duplicate-file",
			json: `[
				{"file":"a.html","url":"https://x.example","label":"detail"},
				{"file":"a.html","url":"https://y.example","label":"hub-index"}
			]`,
			wantErr: true,
			invalid: true,
		},
		{
			name:    "malformed-json",
			json:    `{`,
			wantErr: true,
			invalid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := bench.LoadExtractManifest(manifestFS(t, tt.json))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("LoadExtractManifest() = nil error, want error")
				}
				if tt.invalid && !errors.Is(err, bench.ErrInvalidManifest) {
					t.Fatalf("LoadExtractManifest() error = %v, want errors.Is ErrInvalidManifest", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadExtractManifest() unexpected error: %v", err)
			}
			if len(m.Entries) != 3 {
				t.Fatalf("len(Entries) = %d, want 3", len(m.Entries))
			}
			if m.Entries[0].Label != bench.ExtractDetail || !m.Entries[0].Verified {
				t.Errorf("entry 0 = {Label:%q Verified:%v}, want detail/true", m.Entries[0].Label, m.Entries[0].Verified)
			}
			if m.Entries[1].Label != bench.ExtractHubIndex {
				t.Errorf("entry 1 Label = %q, want hub-index", m.Entries[1].Label)
			}
			if m.Entries[2].Label != bench.ExtractResidue || m.Entries[2].Verified {
				t.Errorf("entry 2 = {Label:%q Verified:%v}, want residue/false", m.Entries[2].Label, m.Entries[2].Verified)
			}
		})
	}
}

func TestExtractEntryReadHTML(t *testing.T) {
	fsys := fstest.MapFS{
		"pages/x.html": &fstest.MapFile{Data: []byte("<title>x</title>")},
	}
	e := bench.ExtractEntry{File: "x.html"}
	got, err := e.ReadHTML(fsys)
	if err != nil {
		t.Fatalf("ReadHTML() error: %v", err)
	}
	if string(got) != "<title>x</title>" {
		t.Errorf("ReadHTML() = %q, want %q", got, "<title>x</title>")
	}
}

// TestLoadExtractManifest_CommittedSet guards that the real reject-rung regression set
// parses, every fixture's HTML is present and non-empty, and all three label
// classes are populated (so every scorecard slice has data). Dependency-light: no
// parser or gate runs here -- the real pipeline is exercised only by manual
// `go run`.
func TestLoadExtractManifest_CommittedSet(t *testing.T) {
	fsys := os.DirFS("../extract-testdata")
	m, err := bench.LoadExtractManifest(fsys)
	if err != nil {
		t.Fatalf("LoadExtractManifest(extract-testdata) error: %v", err)
	}
	if len(m.Entries) == 0 {
		t.Fatal("committed reject-rung regression set is empty")
	}
	counts := map[bench.ExtractLabel]int{}
	for _, e := range m.Entries {
		counts[e.Label]++
		html, err := e.ReadHTML(fsys)
		if err != nil {
			t.Errorf("ReadHTML(%q) error: %v", e.File, err)
			continue
		}
		if len(html) == 0 {
			t.Errorf("ReadHTML(%q) returned empty bytes", e.File)
		}
	}
	for _, l := range bench.AllExtractLabels {
		if counts[l] == 0 {
			t.Errorf("label %q has no fixtures in the committed reject-rung regression set", l)
		}
	}
}

// TestScoreExtractSetsAmbiguousAsideEntirely is the acceptance criterion in the main
// scorer: a page a review could not classify is REPORTED and never scored. It costs a
// call like any other row, so it stays in Total and ExtractCalls; it enters no
// confusion cell, no per-class slice, no false-drop and no leak, in EITHER direction
// of the gate's decision -- because collapsing it into a class would let a labeller's
// shrug decide whether the build goes red.
func TestScoreExtractSetsAmbiguousAsideEntirely(t *testing.T) {
	for name, extract := range map[string]bool{"skipped": false, "extracted": true} {
		r := bench.ScoreExtract([]bench.ExtractVerdictRow{
			{URL: "https://a.test/posting", Label: bench.ExtractDetail, Extract: true},
			{URL: "https://b.test/unclear", Label: bench.ExtractAmbiguous, Extract: extract},
		})
		e := r.Extract

		if e.Total != 2 {
			t.Errorf("%s: Total = %d, want 2 (an unclassifiable page is still a page the gate ruled on)", name, e.Total)
		}
		wantCalls := 1
		if extract {
			wantCalls = 2
		}
		if e.ExtractCalls != wantCalls {
			t.Errorf("%s: ExtractCalls = %d, want %d (cost is cost whatever the label)", name, e.ExtractCalls, wantCalls)
		}
		if e.Ambiguous != 1 {
			t.Errorf("%s: Ambiguous = %d, want 1", name, e.Ambiguous)
		}
		if want := boolToInt(extract); e.AmbiguousExtracted != want {
			t.Errorf("%s: AmbiguousExtracted = %d, want %d", name, e.AmbiguousExtracted, want)
		}
		if len(e.FalseDrops) != 0 {
			t.Errorf("%s: an ambiguous page became a false-drop: %v", name, e.FalseDrops)
		}
		if len(e.Leaks) != 0 {
			t.Errorf("%s: an ambiguous page became a leak: %v", name, e.Leaks)
		}
		if _, scored := e.ByClass[bench.ExtractAmbiguous]; scored {
			t.Errorf("%s: the ambiguous label got a per-class slice; it is not a class the gate is scored against", name)
		}
		if e.ResidueCount != 0 {
			t.Errorf("%s: ResidueCount = %d, want 0", name, e.ResidueCount)
		}
		// The one scored row is a correctly-extracted detail, so the confusion must
		// read as a perfect 1-of-1 whatever the ambiguous row did.
		if e.Overall.Total != 1 || e.Overall.TP != 1 {
			t.Errorf("%s: the confusion is over %d rows (TP %d), want exactly the 1 scored row", name, e.Overall.Total, e.Overall.TP)
		}
	}
}

// boolToInt is the one-line adapter the table above needs to state its expectation.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestExtractLabelScoredSeparatesReportingFromScoring pins the distinction the whole
// ambiguity design rests on: ambiguous is a VALID label -- a row carrying it is
// labelled, not unlabelled, and the tooling must keep it rather than skip it -- and
// it is not a SCORED one.
func TestExtractLabelScoredSeparatesReportingFromScoring(t *testing.T) {
	for _, label := range bench.AllExtractLabels {
		if !label.Valid() || !label.Scored() {
			t.Errorf("%q: valid=%v scored=%v, want both true", label, label.Valid(), label.Scored())
		}
	}
	if !bench.ExtractAmbiguous.Valid() {
		t.Error("ambiguous is not a valid label; a row a review could not settle is labelled, not unlabelled")
	}
	if bench.ExtractAmbiguous.Scored() {
		t.Error("ambiguous reports itself as scored")
	}
	if bench.ExtractAmbiguous.Positive() {
		t.Error("ambiguous reports itself as the positive class")
	}
	for _, label := range bench.AllExtractLabels {
		if label == bench.ExtractAmbiguous {
			t.Error("AllExtractLabels contains ambiguous; it drives the per-class breakdowns and the every-class-exercised guard, and no drawing can promise an undecidable row exists")
		}
	}
}
