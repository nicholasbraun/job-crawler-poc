package main

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// fitSample builds one hand-made sample. Its Score Signals are given literally rather
// than derived from a page: these tests exercise the FIT, and what a page carries is
// pagegate.Signals' own business and has its own tests.
func fitSample(t *testing.T, url string, label bench.ExtractLabel, signals ...string) scorerSample {
	t.Helper()
	u, err := crawler.NewURL(url)
	if err != nil {
		t.Fatalf("url %q: %v", url, err)
	}
	return scorerSample{URL: url, Host: u.Hostname, Label: label, Signals: signals, Accepted: true}
}

// separableSamples is a toy population where one word decides the label: every posting
// carries body:widget and nothing else does. Ten rows a side clears any min-df floor
// these tests use.
func separableSamples(t *testing.T) []scorerSample {
	t.Helper()
	samples := []scorerSample{}
	for i := range 10 {
		samples = append(samples,
			fitSample(t, "https://a"+string(rune('a'+i))+".test/jobs/1", bench.ExtractDetail,
				"sig:lone_posting", "body:widget", "body:filler", "body:common"),
			fitSample(t, "https://b"+string(rune('a'+i))+".test/careers", bench.ExtractHubIndex,
				"sig:job_links_ge_1", "body:filler", "body:common"),
		)
	}
	return samples
}

// writeGoldSetLines writes rows as a JSONL gold set in a temp directory and returns
// its path.
func writeGoldSetLines(t *testing.T, rows []goldRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), goldSetFile)
	var b strings.Builder
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("marshal %s: %v", row.URL, err)
		}
		b.Write(data)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write gold set: %v", err)
	}
	return path
}

// TestTheTrainerAndTheGateAgreeAboutTheSignalNamespaces holds the one thing the
// trainer restates rather than shares: the three Score Signal prefixes. They are the
// keys of the fitted table, so pagegate renaming one would leave the trainer reading
// every word as a structural signal -- truncating nothing, excluding no host word, and
// failing silently.
func TestTheTrainerAndTheGateAgreeAboutTheSignalNamespaces(t *testing.T) {
	u, err := crawler.NewURL("https://acme.test/careers/senior-engineer")
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	sigs := pagegate.Signals(u, &crawler.Content{
		Title:       "Senior Engineer gesucht",
		MainContent: "Ihre Aufgaben. Ihr Profil. Wir bieten. Vollzeit. Jetzt bewerben.",
		JSONLD:      []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
	})
	if len(sigs) == 0 {
		t.Fatal("the page carries no Score Signal at all")
	}

	counts := map[string]int{}
	for _, sig := range sigs {
		switch {
		case strings.HasPrefix(sig, structuralPrefix):
			counts[structuralPrefix]++
			if isVocabularySignal(sig) {
				t.Errorf("%q is structural but the trainer reads it as a Score Vocabulary entry", sig)
			}
		case strings.HasPrefix(sig, titlePrefix), strings.HasPrefix(sig, bodyPrefix):
			counts[titlePrefix+bodyPrefix]++
			if !isVocabularySignal(sig) {
				t.Errorf("%q is a Score Vocabulary entry the trainer reads as structural", sig)
			}
			if _, ok := vocabularyWordOf(sig); !ok {
				t.Errorf("%q carries no word the exclusion could match", sig)
			}
		default:
			t.Errorf("%q matches none of the trainer's three namespaces", sig)
		}
	}
	if counts[structuralPrefix] == 0 || counts[titlePrefix+bodyPrefix] == 0 {
		t.Errorf("the page produced %v; both namespaces must be exercised or this test proves nothing", counts)
	}
}

func TestScorerSamplesAreSortedAndScorableOnly(t *testing.T) {
	path := writeGoldSetLines(t, []goldRow{
		{URL: "https://z.test/jobs/1", Label: bench.ExtractDetail},
		{URL: "https://a.test/jobs/2", Label: bench.ExtractResidue},
		{URL: "https://m.test/jobs/3", Label: bench.ExtractAmbiguous},
		{URL: "https://n.test/jobs/4", Label: bench.ExtractLabel("")},
		{URL: "https://b.test/jobs/5", Label: bench.ExtractHubIndex},
	})

	samples, census, err := scorerSamples(path)
	if err != nil {
		t.Fatalf("scorerSamples: %v", err)
	}

	t.Run("only the scorable rows become samples", func(t *testing.T) {
		got := []string{}
		for _, s := range samples {
			got = append(got, s.URL)
		}
		want := []string{"https://a.test/jobs/2", "https://b.test/jobs/5", "https://z.test/jobs/1"}
		if !slices.Equal(got, want) {
			t.Errorf("samples = %v, want %v (sorted by URL, ambiguous and unlabelled set aside)", got, want)
		}
	})

	t.Run("the census counts what was set aside", func(t *testing.T) {
		if census.Rows != 5 {
			t.Errorf("rows = %d, want 5", census.Rows)
		}
		if census.Ambiguous != 1 {
			t.Errorf("ambiguous = %d, want 1", census.Ambiguous)
		}
		if census.Skipped != 1 {
			t.Errorf("skipped = %d, want 1", census.Skipped)
		}
	})

	// The whole regenerability guarantee rests on the row order being a function of the
	// file, and the row order is the URL. A repeated URL would make that key non-total,
	// leaving two rows' relative order -- and so the gradient's float sum -- to the
	// sort's internals; it would also halve one row's out-of-fold reading, since
	// crossValidate keys its scores by URL. Loud beats latent.
	t.Run("a repeated URL is refused rather than fitted", func(t *testing.T) {
		dup := writeGoldSetLines(t, []goldRow{
			{URL: "https://a.test/jobs/1", Label: bench.ExtractDetail},
			{URL: "https://a.test/jobs/1", Label: bench.ExtractHubIndex},
		})
		if _, _, err := scorerSamples(dup); err == nil {
			t.Fatal("scorerSamples accepted a gold set with the same URL twice")
		} else if !strings.Contains(err.Error(), "https://a.test/jobs/1") {
			t.Errorf("the error must name the repeated URL, got %v", err)
		}
	})
}

// TestHostWordsTokenizeLikeTheScoreVocabulary holds the shared tokenizer. The exclusion
// and the guard that re-derives it must agree about what "a word of a host" is, or the
// guard passes on words the exclusion never considered.
func TestHostWordsTokenizeLikeTheScoreVocabulary(t *testing.T) {
	tests := []struct {
		name string
		host string
		want []string
	}{
		{"a deep German host", "jobs.medizin.uni-tuebingen.de", []string{"de", "jobs", "medizin", "tuebingen", "uni"}},
		{"a one-rune label is below the minimum", "a.example.com", []string{"com", "example"}},
		{"case is folded", "JOBS.Example.COM", []string{"com", "example", "jobs"}},
		{"a digit run is a separator, never a word", "jobs2.example.com", []string{"com", "example", "jobs"}},
		{"an all-digit host yields nothing", "12.34.56.78", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hostWords(tt.host); !slices.Equal(got, tt.want) {
				t.Errorf("hostWords(%q) = %v, want %v", tt.host, got, tt.want)
			}
		})
	}
}

func TestFitScorerIsDeterministic(t *testing.T) {
	samples := separableSamples(t)
	opts := fitOptions{Vocabulary: 2, MinDF: 5}

	first := fitScorer(samples, nil, opts)
	t.Run("two fits over the same samples agree", func(t *testing.T) {
		if got := fitScorer(samples, nil, opts); !sameModel(got, first) {
			t.Errorf("a second fit differs:\n got  %v\n want %v", got, first)
		}
	})

	t.Run("the input slice's order changes nothing", func(t *testing.T) {
		shuffled := append([]scorerSample{}, samples...)
		slices.Reverse(shuffled)
		if got := fitScorer(shuffled, nil, opts); !sameModel(got, first) {
			t.Errorf("a reordered input produced a different fit:\n got  %v\n want %v", got, first)
		}
	})
}

// sameModel compares two fitted models exactly. A tolerance comparison would defeat
// the point: the artifact is guarded byte for byte, so the fit has to be equal, not
// close.
func sameModel(a, b scorerModel) bool {
	if a.Intercept != b.Intercept || len(a.Weights) != len(b.Weights) {
		return false
	}
	for name, w := range a.Weights {
		if b.Weights[name] != w {
			return false
		}
	}
	return true
}

// TestFitScorerSeparatesASeparableSet is the fit's sanity check: on a population where
// one word decides the label, that word must carry the most weight of any Score
// Vocabulary entry and every posting must outscore every non-posting.
func TestFitScorerSeparatesASeparableSet(t *testing.T) {
	samples := separableSamples(t)
	m := fitScorer(samples, nil, fitOptions{MinDF: 5})

	heaviest, best := "", 0.0
	for _, name := range m.vocabularyWords() {
		if w := math.Abs(m.Weights[name]); w > best {
			heaviest, best = name, w
		}
	}
	if heaviest != "body:widget" {
		t.Errorf("heaviest Score Vocabulary entry is %q (%.6f), want body:widget", heaviest, best)
	}

	lowestDetail, highestOther := math.Inf(1), math.Inf(-1)
	for _, s := range samples {
		score := m.score(s.Signals)
		if s.Label.Positive() {
			lowestDetail = math.Min(lowestDetail, score)
			continue
		}
		highestOther = math.Max(highestOther, score)
	}
	if lowestDetail <= highestOther {
		t.Errorf("the lowest posting scored %.6f and the highest non-posting %.6f; a separable set must separate", lowestDetail, highestOther)
	}
}

// TestFitScorerNeverKeepsAnExcludedWord holds the leakage exclusion at its source: a
// word that predicts the label perfectly still gets no entry when it is a host word.
func TestFitScorerNeverKeepsAnExcludedWord(t *testing.T) {
	m := fitScorer(separableSamples(t), map[string]struct{}{"widget": {}}, fitOptions{MinDF: 5})
	for _, name := range []string{"body:widget", "title:widget"} {
		if _, ok := m.Weights[name]; ok {
			t.Errorf("%s carries a weight despite being excluded", name)
		}
	}
}

func TestSelectVocabularyKeepsWordsOnlyAndNeverAStructuralSignal(t *testing.T) {
	admissible := []string{
		"body:one", "body:three", "body:two", "sig:job_links_ge_1", "sig:lone_posting",
		"sig:vocab_groups_ge_1", "title:four", "title:five",
	}
	m := scorerModel{Weights: map[string]float64{
		"body:one": 0.9, "body:two": -0.8, "body:three": 0.1, "title:four": 0.05, "title:five": -0.02,
		"sig:job_links_ge_1": 0.001, "sig:lone_posting": 0.002, "sig:vocab_groups_ge_1": 0.003,
	}}

	got := selectVocabulary(admissible, m, 2)
	want := []string{"body:one", "body:two", "sig:job_links_ge_1", "sig:lone_posting", "sig:vocab_groups_ge_1"}
	if !slices.Equal(got, want) {
		t.Errorf("selectVocabulary = %v, want %v (the two heaviest words plus every structural signal)", got, want)
	}
}

func TestHostGroupedFoldsKeepAHostWhole(t *testing.T) {
	const folds = 5
	hosts := []string{}
	for i := range 40 {
		hosts = append(hosts, "host"+string(rune('a'+i%26))+string(rune('a'+i/26))+".test")
	}

	t.Run("a host lands in one fold, always the same one", func(t *testing.T) {
		for _, host := range hosts {
			want := foldOf(defaultFoldSeed, host, folds)
			for range 3 {
				if got := foldOf(defaultFoldSeed, host, folds); got != want {
					t.Fatalf("foldOf(%q) returned %d then %d", host, want, got)
				}
			}
			if want < 0 || want >= folds {
				t.Fatalf("foldOf(%q) = %d, outside [0,%d)", host, want, folds)
			}
		}
	})

	t.Run("every fold is populated", func(t *testing.T) {
		used := map[int]int{}
		for _, host := range hosts {
			used[foldOf(defaultFoldSeed, host, folds)]++
		}
		if len(used) != folds {
			t.Errorf("%d of %d folds hold a host: %v", len(used), folds, used)
		}
	})

	t.Run("a different seed is a different assignment", func(t *testing.T) {
		moved := 0
		for _, host := range hosts {
			if foldOf(defaultFoldSeed, host, folds) != foldOf("some-other-seed", host, folds) {
				moved++
			}
		}
		if moved == 0 {
			t.Error("no host moved fold under a different seed; the seed is not reaching the assignment")
		}
	})
}

// TestCrossValidateReportsRowsNoFoldCouldScore pins the coverage count. A held-out row
// whose fold has no training half is scored by nobody, and every caller reads a missing
// key as 0.0 -- a legitimate Posting Score and the lowest one possible, which sorts the
// row to the bottom of the out-of-fold ladder and makes it one of the first rows any cut
// vetoes. Returning the count is what lets the report say the ladder is fiction instead
// of printing it as a measurement.
func TestCrossValidateReportsRowsNoFoldCouldScore(t *testing.T) {
	opts := fitOptions{Vocabulary: 0, MinDF: 1}

	t.Run("every row is scored when the folds have both halves", func(t *testing.T) {
		samples := separableSamples(t)
		scores, unscored := crossValidate(samples, nil, opts, defaultFoldSeed, 2)
		if unscored != 0 {
			t.Errorf("unscored = %d, want 0 over %d rows on %d hosts", unscored, len(samples), hostCount(samples))
		}
		for _, s := range samples {
			if _, ok := scores[s.URL]; !ok {
				t.Errorf("%s has no out-of-fold score", s.URL)
			}
		}
	})

	t.Run("a single-host population leaves every row unscored", func(t *testing.T) {
		// One host means one fold holds everything, so that fold's training half is
		// empty and every other fold's held half is. Nothing is ever fitted, and
		// nothing is ever scored.
		samples := []scorerSample{
			fitSample(t, "https://only.test/jobs/1", bench.ExtractDetail, "body:widget"),
			fitSample(t, "https://only.test/jobs/2", bench.ExtractHubIndex, "body:filler"),
		}
		scores, unscored := crossValidate(samples, nil, opts, defaultFoldSeed, 2)
		if unscored != len(samples) {
			t.Errorf("unscored = %d, want %d", unscored, len(samples))
		}
		if len(scores) != 0 {
			t.Errorf("scores = %v, want none: no fold had a training half", scores)
		}
	})
}

func TestZeroLossThresholdIsTheDeepestCutThatLosesNoDetailRow(t *testing.T) {
	tests := []struct {
		name    string
		accepts []scoredAccept
		want    float64
		wantOK  bool
	}{
		{
			name: "a clean split cuts to the lowest posting",
			accepts: []scoredAccept{
				{URL: "a", Label: bench.ExtractResidue, Score: 0.10},
				{URL: "b", Label: bench.ExtractHubIndex, Score: 0.20},
				{URL: "c", Label: bench.ExtractDetail, Score: 0.400000},
				{URL: "d", Label: bench.ExtractDetail, Score: 0.90},
			},
			want: 0.4, wantOK: true,
		},
		{
			name: "a posting at the very bottom leaves nothing to cut",
			accepts: []scoredAccept{
				{URL: "a", Label: bench.ExtractDetail, Score: 0.010000},
				{URL: "b", Label: bench.ExtractResidue, Score: 0.90},
			},
			want: 0.01, wantOK: true,
		},
		{
			name: "a tie at the boundary keeps both",
			accepts: []scoredAccept{
				{URL: "a", Label: bench.ExtractResidue, Score: 0.250000},
				{URL: "b", Label: bench.ExtractDetail, Score: 0.250000},
			},
			want: 0.25, wantOK: true,
		},
		{
			name:    "no posting at all is not an operating point",
			accepts: []scoredAccept{{URL: "a", Label: bench.ExtractResidue, Score: 0.5}},
			wantOK:  false,
		},
		{name: "an empty accept set is not an operating point", accepts: []scoredAccept{}, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := zeroLossThreshold(tt.accepts)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if got != tt.want {
				t.Errorf("threshold = %v, want %v", got, tt.want)
			}
			// The rung rejects strictly below the threshold, so the cut must keep
			// every posting the set holds.
			for _, a := range tt.accepts {
				if a.Label.Positive() && a.Score < got {
					t.Errorf("%s (%v) is a posting the threshold %v drops", a.URL, a.Score, got)
				}
			}
		})
	}
}

// TestVetoCurveOnlyJudgesTheAcceptedRows holds the restriction the whole report rests
// on: a page the Positive Evidence rung does not accept is not part of the population
// the Learned Veto judges, however low it scores.
func TestVetoCurveOnlyJudgesTheAcceptedRows(t *testing.T) {
	samples := []scorerSample{
		{URL: "a", Label: bench.ExtractDetail, Accepted: true, Signals: []string{"hi"}},
		{URL: "b", Label: bench.ExtractResidue, Accepted: true, Signals: []string{"lo"}},
		{URL: "c", Label: bench.ExtractResidue, Accepted: false, Signals: []string{"floor"}},
	}
	scores := map[string]float64{"hi": 0.9, "lo": 0.5, "floor": 0.0}
	accepts := scoredAccepts(samples, func(s scorerSample) float64 { return scores[s.Signals[0]] })

	if len(accepts) != 2 {
		t.Fatalf("%d rows in the accept set, want 2 (the rejected row must not be judged)", len(accepts))
	}
	for _, a := range accepts {
		if a.URL == "c" {
			t.Fatal("the rejected row reached the accept set")
		}
	}
	for _, p := range vetoCurve(accepts, []float64{0.5, 1.0}) {
		if p.Vetoed+p.Survivors != 2 {
			t.Errorf("depth %v accounts for %d rows, want 2", p.Depth, p.Vetoed+p.Survivors)
		}
	}
}

// TestVetoCurveCountsLeaksByLabel holds ADR-0049's reporting split: hub-index and
// residue removals are the two kinds of leak the rung sheds, and pooling them hides
// which one the cut is actually buying.
func TestVetoCurveCountsLeaksByLabel(t *testing.T) {
	accepts := []scoredAccept{
		{URL: "a", Label: bench.ExtractResidue, Score: 0.1},
		{URL: "b", Label: bench.ExtractHubIndex, Score: 0.2},
		{URL: "c", Label: bench.ExtractResidue, Score: 0.3},
		{URL: "d", Label: bench.ExtractDetail, Score: 0.8},
		{URL: "e", Label: bench.ExtractDetail, Score: 0.9},
	}
	got := measureVeto(accepts, vetoPoint{Threshold: 0.35})

	want := vetoPoint{Threshold: 0.35, Vetoed: 3, VetoShare: 0.6, HubIndexRemoved: 1, ResidueRemoved: 2, Survivors: 2, Precision: 1}
	if got != want {
		t.Errorf("measureVeto = %+v,\n              want %+v", got, want)
	}
}

// TestScorerModelAppliesTheGatesFunctionalForm holds the arithmetic the trainer
// duplicates from pagegate.Score: the intercept plus the weights of the signals the
// page carries, summed in Signals' order, squashed by the logistic. A signal with no
// entry contributes nothing, which is what lets the Score Vocabulary be truncated.
func TestScorerModelAppliesTheGatesFunctionalForm(t *testing.T) {
	m := scorerModel{Intercept: -0.5, Weights: map[string]float64{"body:a": 0.25, "sig:lone_posting": 1.5}}
	signals := []string{"sig:lone_posting", "body:a", "body:unweighted"}

	z := -0.5
	z += 1.5
	z += 0.25
	want := 1 / (1 + math.Exp(-z))
	if got := m.score(signals); got != want {
		t.Errorf("score = %v, want %v", got, want)
	}

	t.Run("a page carrying nothing scores the intercept", func(t *testing.T) {
		want := 1 / (1 + math.Exp(0.5))
		if got := m.score(nil); got != want {
			t.Errorf("score = %v, want %v", got, want)
		}
	})
}

func TestRenderWeightsSourceIsDeterministic(t *testing.T) {
	a := weightsArtifact{
		Model: scorerModel{
			Intercept: -1.234567,
			Weights: map[string]float64{
				"title:zulu": -0.5, "body:alpha": 0.25, "sig:lone_posting": 1.5, "body:mike": -0.125,
			},
		},
		Threshold:    0.605395,
		ScorableRows: 442,
		Hosts:        357,
	}

	src, err := renderWeightsSource(a)
	if err != nil {
		t.Fatalf("renderWeightsSource: %v", err)
	}

	t.Run("two renders are byte-equal", func(t *testing.T) {
		again, err := renderWeightsSource(a)
		if err != nil {
			t.Fatalf("renderWeightsSource: %v", err)
		}
		if string(again) != string(src) {
			t.Error("a second render differs from the first")
		}
	})

	t.Run("the output is valid Go", func(t *testing.T) {
		if _, err := parser.ParseFile(token.NewFileSet(), "posting_score_weights_gen.go", src, 0); err != nil {
			t.Errorf("parse: %v", err)
		}
	})

	t.Run("keys are emitted in sorted order", func(t *testing.T) {
		keys := regexp.MustCompile(`(?m)^\t"([^"]+)":`).FindAllStringSubmatch(string(src), -1)
		got := []string{}
		for _, k := range keys {
			got = append(got, k[1])
		}
		want := []string{"body:alpha", "body:mike", "sig:lone_posting", "title:zulu"}
		if !slices.Equal(got, want) {
			t.Errorf("keys = %v, want %v", got, want)
		}
	})

	t.Run("it carries the generated marker and the runnable directive", func(t *testing.T) {
		for _, want := range []string{
			`// Code generated by "llmbench train-scorer"; DO NOT EDIT.`,
			// Both paths relative to internal/pagegate, which is go generate's working
			// directory for this file: a repo-root-relative -in would resolve inside
			// the package and silently read nothing.
			"//go:generate go run ../../cmd/llmbench train-scorer -in ../../cmd/llmbench/extract-goldset/goldset.jsonl -out posting_score_weights_gen.go",
			"const VetoThreshold float64 = 0.605395",
			"const scoreIntercept = -1.234567",
			"442 scorable rows",
			"357 hosts",
		} {
			if !strings.Contains(string(src), want) {
				t.Errorf("missing from the rendered source: %s", want)
			}
		}
	})

	t.Run("nothing dates the artifact", func(t *testing.T) {
		if m := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`).FindString(string(src)); m != "" {
			t.Errorf("the rendered source carries %q; a generation stamp would break byte-identical regeneration", m)
		}
	})
}
