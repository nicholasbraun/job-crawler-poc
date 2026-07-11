package bench_test

import (
	"reflect"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

func TestGateOutcomeFrom(t *testing.T) {
	tests := []struct {
		name    string
		accept  bool
		certain bool
		want    bench.GateOutcome
	}{
		{"reject-not-certain", false, false, bench.GateReject},
		{"reject-even-if-certain", false, true, bench.GateReject},
		{"certain-accept", true, true, bench.GateCertainAccept},
		{"uncertain", true, false, bench.GateUncertain},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bench.GateOutcomeFrom(tt.accept, tt.certain); got != tt.want {
				t.Errorf("GateOutcomeFrom(%v, %v) = %v, want %v", tt.accept, tt.certain, got, tt.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	tests := []struct {
		name          string
		rows          []bench.VerdictRow
		total         int
		llmCalls      int
		llmCallRate   float64
		leaks         []string
		falseCertains []string
		violations    []bench.Violation
		failed        bool
	}{
		{
			name:          "empty",
			rows:          []bench.VerdictRow{},
			total:         0,
			llmCalls:      0,
			llmCallRate:   0,
			leaks:         []string{},
			falseCertains: []string{},
			violations:    []bench.Violation{},
			failed:        false,
		},
		{
			name: "clean-5-row-set",
			rows: []bench.VerdictRow{
				{URL: "a", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateCertainAccept},
				{URL: "b", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateUncertain},
				{URL: "c", Category: bench.CategoryAggregator, Label: bench.LabelNotCareerPage, Gate: bench.GateReject},
				{URL: "d", Category: bench.CategoryJobPostingSingle, Label: bench.LabelNotCareerPage, Gate: bench.GateReject},
				{URL: "e", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain},
			},
			total:         5,
			llmCalls:      2,
			llmCallRate:   0.4,
			leaks:         []string{},
			falseCertains: []string{},
			violations:    []bench.Violation{},
			failed:        false,
		},
		{
			name: "leak-self-hosted",
			rows: []bench.VerdictRow{
				{URL: "hub", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject},
			},
			total:         1,
			llmCalls:      0,
			llmCallRate:   0,
			leaks:         []string{"hub"},
			falseCertains: []string{},
			violations:    []bench.Violation{},
			failed:        true,
		},
		{
			name: "false-certain-posting",
			rows: []bench.VerdictRow{
				{URL: "post", Category: bench.CategoryJobPostingSingle, Label: bench.LabelNotCareerPage, Gate: bench.GateCertainAccept},
			},
			total:         1,
			llmCalls:      0,
			llmCallRate:   0,
			leaks:         []string{},
			falseCertains: []string{"post"},
			violations:    []bench.Violation{},
			failed:        true,
		},
		{
			name: "ats-leak-in-both-lists",
			rows: []bench.VerdictRow{
				{URL: "ats", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateReject},
			},
			total:         1,
			llmCalls:      0,
			llmCallRate:   0,
			leaks:         []string{"ats"},
			falseCertains: []string{},
			violations: []bench.Violation{
				{URL: "ats", Category: bench.CategoryHubATSRoot, Want: bench.GateCertainAccept, Got: bench.GateReject},
			},
			failed: true,
		},
		{
			name: "ats-uncertain-violation",
			rows: []bench.VerdictRow{
				{URL: "ats", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateUncertain},
			},
			total:         1,
			llmCalls:      1,
			llmCallRate:   1,
			leaks:         []string{},
			falseCertains: []string{},
			violations: []bench.Violation{
				{URL: "ats", Category: bench.CategoryHubATSRoot, Want: bench.GateCertainAccept, Got: bench.GateUncertain},
			},
			failed: true,
		},
		{
			name: "aggregator-uncertain-violation",
			rows: []bench.VerdictRow{
				{URL: "agg", Category: bench.CategoryAggregator, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain},
			},
			total:         1,
			llmCalls:      1,
			llmCallRate:   1,
			leaks:         []string{},
			falseCertains: []string{},
			violations: []bench.Violation{
				{URL: "agg", Category: bench.CategoryAggregator, Want: bench.GateReject, Got: bench.GateUncertain},
			},
			failed: true,
		},
		{
			name: "aggregator-certain-both-lists",
			rows: []bench.VerdictRow{
				{URL: "agg", Category: bench.CategoryAggregator, Label: bench.LabelNotCareerPage, Gate: bench.GateCertainAccept},
			},
			total:         1,
			llmCalls:      0,
			llmCallRate:   0,
			leaks:         []string{},
			falseCertains: []string{"agg"},
			violations: []bench.Violation{
				{URL: "agg", Category: bench.CategoryAggregator, Want: bench.GateReject, Got: bench.GateCertainAccept},
			},
			failed: true,
		},
		{
			name: "rate-rounding-third",
			rows: []bench.VerdictRow{
				{URL: "a", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain},
				{URL: "b", Category: bench.CategoryUnrelated, Label: bench.LabelNotCareerPage, Gate: bench.GateReject},
				{URL: "c", Category: bench.CategoryUnrelated, Label: bench.LabelNotCareerPage, Gate: bench.GateReject},
			},
			total:         3,
			llmCalls:      1,
			llmCallRate:   0.3333,
			leaks:         []string{},
			falseCertains: []string{},
			violations:    []bench.Violation{},
			failed:        false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.Score(tt.rows)
			g := got.Gate
			if g.Total != tt.total {
				t.Errorf("Total = %d, want %d", g.Total, tt.total)
			}
			if g.LLMCalls != tt.llmCalls {
				t.Errorf("LLMCalls = %d, want %d", g.LLMCalls, tt.llmCalls)
			}
			if g.LLMCallRate != tt.llmCallRate {
				t.Errorf("LLMCallRate = %v, want %v", g.LLMCallRate, tt.llmCallRate)
			}
			if !reflect.DeepEqual(g.Leaks, tt.leaks) {
				t.Errorf("Leaks = %v, want %v", g.Leaks, tt.leaks)
			}
			if !reflect.DeepEqual(g.FalseCertains, tt.falseCertains) {
				t.Errorf("FalseCertains = %v, want %v", g.FalseCertains, tt.falseCertains)
			}
			if !reflect.DeepEqual(g.Violations, tt.violations) {
				t.Errorf("Violations = %v, want %v", g.Violations, tt.violations)
			}
			if got.Failed() != tt.failed {
				t.Errorf("Failed() = %v, want %v", got.Failed(), tt.failed)
			}
		})
	}
}

// TestScoreLLM checks the LLM scorecard folds ONLY the forwarded (GateUncertain)
// subset: certain-accept and reject rows are excluded even though they carry a
// (default-false) LLMConfirm.
func TestScoreLLM(t *testing.T) {
	tests := []struct {
		name string
		rows []bench.VerdictRow
		want bench.ClassScore
	}{
		{
			name: "no-uncertain",
			rows: []bench.VerdictRow{
				{Gate: bench.GateCertainAccept, Label: bench.LabelCareerPage, Category: bench.CategoryHubATSRoot},
				{Gate: bench.GateReject, Label: bench.LabelNotCareerPage, Category: bench.CategoryAggregator},
			},
			want: bench.ClassScore{Total: 0, Precision: 0, Recall: 0, Accuracy: 0, F1: 0},
		},
		{
			name: "mixed-1each",
			rows: []bench.VerdictRow{
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMConfirm: true},   // tp
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMConfirm: false},  // fn
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMConfirm: true}, // fp
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMConfirm: false},
				{Gate: bench.GateCertainAccept, Label: bench.LabelCareerPage, Category: bench.CategoryHubATSRoot},
				{Gate: bench.GateReject, Label: bench.LabelNotCareerPage, Category: bench.CategoryAggregator},
			},
			want: bench.ClassScore{TP: 1, FP: 1, FN: 1, TN: 1, Total: 4, Precision: 0.5, Recall: 0.5, Accuracy: 0.5, F1: 0.5},
		},
		{
			name: "perfect",
			rows: []bench.VerdictRow{
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMConfirm: true},    // tp
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMConfirm: false}, // tn
			},
			want: bench.ClassScore{TP: 1, FP: 0, FN: 0, TN: 1, Total: 2, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
		},
		{
			name: "no-predicted-positive",
			rows: []bench.VerdictRow{
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMConfirm: false},   // fn
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMConfirm: false}, // tn
			},
			want: bench.ClassScore{TP: 0, FP: 0, FN: 1, TN: 1, Total: 2, Precision: 0, Recall: 0, Accuracy: 0.5, F1: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.Score(tt.rows).LLM
			want := bench.LLMScorecard{ClassScore: tt.want}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("LLM = %+v, want %+v", got, want)
			}
		})
	}
}

// TestScoreEndToEnd checks the end-to-end scorecard folds the production decision
// (reject=>negative, certain-accept=>positive, uncertain=>the LLM verdict) over
// ALL rows, both overall and sliced by category.
func TestScoreEndToEnd(t *testing.T) {
	tests := []struct {
		name       string
		rows       []bench.VerdictRow
		overall    bench.ClassScore
		byCategory map[bench.Category]bench.ClassScore
	}{
		{
			name: "composite",
			rows: []bench.VerdictRow{
				{URL: "ats", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateCertainAccept},                        // tp
				{URL: "hub", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateUncertain, LLMConfirm: true},       // tp
				{URL: "trap-fp", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain, LLMConfirm: true}, // fp
				{URL: "trap-tn", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain, LLMConfirm: false},
				{URL: "agg", Category: bench.CategoryAggregator, Label: bench.LabelNotCareerPage, Gate: bench.GateReject},        // tn
				{URL: "post", Category: bench.CategoryJobPostingSingle, Label: bench.LabelNotCareerPage, Gate: bench.GateReject}, // tn
			},
			overall: bench.ClassScore{TP: 2, FP: 1, FN: 0, TN: 3, Total: 6, Precision: 0.6667, Recall: 1, Accuracy: 0.8333, F1: 0.8},
			byCategory: map[bench.Category]bench.ClassScore{
				bench.CategoryHubATSRoot:       {TP: 1, FP: 0, FN: 0, TN: 0, Total: 1, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
				bench.CategoryHubSelfHosted:    {TP: 1, FP: 0, FN: 0, TN: 0, Total: 1, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
				bench.CategoryCultureAbout:     {TP: 0, FP: 1, FN: 0, TN: 1, Total: 2, Precision: 0, Recall: 0, Accuracy: 0.5, F1: 0},
				bench.CategoryAggregator:       {TP: 0, FP: 0, FN: 0, TN: 1, Total: 1, Precision: 0, Recall: 0, Accuracy: 1, F1: 0},
				bench.CategoryJobPostingSingle: {TP: 0, FP: 0, FN: 0, TN: 1, Total: 1, Precision: 0, Recall: 0, Accuracy: 1, F1: 0},
			},
		},
		{
			name: "leak-through-e2e",
			rows: []bench.VerdictRow{
				{URL: "leak", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject}, // fn
			},
			overall: bench.ClassScore{TP: 0, FP: 0, FN: 1, TN: 0, Total: 1, Precision: 0, Recall: 0, Accuracy: 0, F1: 0},
			byCategory: map[bench.Category]bench.ClassScore{
				bench.CategoryHubSelfHosted: {TP: 0, FP: 0, FN: 1, TN: 0, Total: 1, Precision: 0, Recall: 0, Accuracy: 0, F1: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.Score(tt.rows).EndToEnd
			if !reflect.DeepEqual(got.Overall, tt.overall) {
				t.Errorf("Overall = %+v, want %+v", got.Overall, tt.overall)
			}
			if !reflect.DeepEqual(got.ByCategory, tt.byCategory) {
				t.Errorf("ByCategory = %+v, want %+v", got.ByCategory, tt.byCategory)
			}
		})
	}
}
