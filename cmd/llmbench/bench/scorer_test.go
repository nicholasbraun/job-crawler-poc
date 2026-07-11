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

// TestReviewQueue checks the two disagreement axes (gate, pipeline), their fixed
// reason order, that verified rows are excluded, and that an uncertain row with no
// votes skips the pipeline axis. Descriptive selection only -- it never touches
// Failed()/the exit code.
func TestReviewQueue(t *testing.T) {
	tests := []struct {
		name string
		rows []bench.VerdictRow
		want []bench.ReviewItem
	}{
		{
			name: "empty",
			rows: []bench.VerdictRow{},
			want: []bench.ReviewItem{},
		},
		{
			name: "verified-excluded",
			rows: []bench.VerdictRow{
				{URL: "v", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject, Verified: true},
			},
			want: []bench.ReviewItem{},
		},
		{
			name: "clean-agree",
			rows: []bench.VerdictRow{
				{URL: "ok", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateCertainAccept},
			},
			want: []bench.ReviewItem{},
		},
		{
			name: "pipeline-only",
			rows: []bench.VerdictRow{
				{URL: "pipe", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateUncertain, LLMVotes: []bool{false}},
			},
			want: []bench.ReviewItem{
				{URL: "pipe", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Reasons: []bench.ReviewReason{bench.ReviewPipelineDisagrees}},
			},
		},
		{
			name: "gate-and-pipeline-leak",
			rows: []bench.VerdictRow{
				{URL: "leak", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject},
			},
			want: []bench.ReviewItem{
				{URL: "leak", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Reasons: []bench.ReviewReason{bench.ReviewGateDisagrees, bench.ReviewPipelineDisagrees}},
			},
		},
		{
			name: "false-certain-neg",
			rows: []bench.VerdictRow{
				{URL: "fc", Category: bench.CategoryJobPostingSingle, Label: bench.LabelNotCareerPage, Gate: bench.GateCertainAccept},
			},
			want: []bench.ReviewItem{
				{URL: "fc", Category: bench.CategoryJobPostingSingle, Label: bench.LabelNotCareerPage, Reasons: []bench.ReviewReason{bench.ReviewGateDisagrees, bench.ReviewPipelineDisagrees}},
			},
		},
		{
			name: "uncertain-no-votes-pipeline-skipped",
			rows: []bench.VerdictRow{
				{URL: "u", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain},
			},
			want: []bench.ReviewItem{},
		},
		{
			name: "order-preserved",
			rows: []bench.VerdictRow{
				{URL: "ok", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateCertainAccept},
				{URL: "all", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject},
				{URL: "v", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject, Verified: true},
			},
			want: []bench.ReviewItem{
				{URL: "all", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Reasons: []bench.ReviewReason{bench.ReviewGateDisagrees, bench.ReviewPipelineDisagrees}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.Score(tt.rows).ReviewQueue
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ReviewQueue = %+v, want %+v", got, tt.want)
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

// TestScoreLLM checks the LLM scorecard folds ONLY the rows the classifier saw
// (rows with LLMVotes): certain-accept and reject rows without votes are excluded.
// Every case here is N=1, so FlipRate is 0 and Flips is empty.
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
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true}},   // tp
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{false}},  // fn
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMVotes: []bool{true}}, // fp
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMVotes: []bool{false}},
				{Gate: bench.GateCertainAccept, Label: bench.LabelCareerPage, Category: bench.CategoryHubATSRoot},
				{Gate: bench.GateReject, Label: bench.LabelNotCareerPage, Category: bench.CategoryAggregator},
			},
			want: bench.ClassScore{TP: 1, FP: 1, FN: 1, TN: 1, Total: 4, Precision: 0.5, Recall: 0.5, Accuracy: 0.5, F1: 0.5},
		},
		{
			name: "perfect",
			rows: []bench.VerdictRow{
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true}},    // tp
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMVotes: []bool{false}}, // tn
			},
			want: bench.ClassScore{TP: 1, FP: 0, FN: 0, TN: 1, Total: 2, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
		},
		{
			name: "no-predicted-positive",
			rows: []bench.VerdictRow{
				{Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{false}},   // fn
				{Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMVotes: []bool{false}}, // tn
			},
			want: bench.ClassScore{TP: 0, FP: 0, FN: 1, TN: 1, Total: 2, Precision: 0, Recall: 0, Accuracy: 0.5, F1: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.Score(tt.rows).LLM
			want := bench.LLMScorecard{ClassScore: tt.want, FlipRate: 0, Flips: []string{}}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("LLM = %+v, want %+v", got, want)
			}
		})
	}
}

// TestScoreLLMRepeats checks the N-repeat vote fold: the scored verdict is the
// ceil-majority of a row's LLMVotes (even split breaks toward accept), and the
// scorecard's FlipRate/Flips report the non-unanimous rows in input order.
func TestScoreLLMRepeats(t *testing.T) {
	tests := []struct {
		name string
		rows []bench.VerdictRow
		want bench.ClassScore
		flip float64
		urls []string
	}{
		{
			name: "unanimous-true-n3",
			rows: []bench.VerdictRow{
				{URL: "u", Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true, true, true}},
			},
			want: bench.ClassScore{TP: 1, FP: 0, FN: 0, TN: 0, Total: 1, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
			flip: 0,
			urls: []string{},
		},
		{
			name: "majority-true-2of3",
			rows: []bench.VerdictRow{
				{URL: "u", Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true, true, false}},
			},
			want: bench.ClassScore{TP: 1, FP: 0, FN: 0, TN: 0, Total: 1, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
			flip: 1,
			urls: []string{"u"},
		},
		{
			name: "majority-false-1of3",
			rows: []bench.VerdictRow{
				{URL: "u", Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true, false, false}},
			},
			want: bench.ClassScore{TP: 0, FP: 0, FN: 1, TN: 0, Total: 1, Precision: 0, Recall: 0, Accuracy: 0, F1: 0},
			flip: 1,
			urls: []string{"u"},
		},
		{
			name: "tie-2of4-positive",
			rows: []bench.VerdictRow{
				{URL: "u", Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMVotes: []bool{true, true, false, false}},
			},
			want: bench.ClassScore{TP: 0, FP: 1, FN: 0, TN: 0, Total: 1, Precision: 0, Recall: 0, Accuracy: 0, F1: 0},
			flip: 1,
			urls: []string{"u"},
		},
		{
			name: "tie-1of2-positive",
			rows: []bench.VerdictRow{
				{URL: "u", Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true, false}},
			},
			want: bench.ClassScore{TP: 1, FP: 0, FN: 0, TN: 0, Total: 1, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
			flip: 1,
			urls: []string{"u"},
		},
		{
			name: "flip-rate-fraction",
			rows: []bench.VerdictRow{
				{URL: "a", Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true, true}},           // unanimous, TP
				{URL: "b", Gate: bench.GateUncertain, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true, false}},          // flip, majority true, TP
				{URL: "c", Gate: bench.GateUncertain, Label: bench.LabelNotCareerPage, Category: bench.CategoryCultureAbout, LLMVotes: []bool{true, false, false}}, // flip, majority false, TN
			},
			want: bench.ClassScore{TP: 2, FP: 0, FN: 0, TN: 1, Total: 3, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
			flip: 0.6667,
			urls: []string{"b", "c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bench.Score(tt.rows).LLM
			want := bench.LLMScorecard{ClassScore: tt.want, FlipRate: tt.flip, Flips: tt.urls}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("LLM = %+v, want %+v", got, want)
			}
		})
	}
}

// TestScoreLLMIsolated proves isolated-style rows -- votes attached to non-uncertain
// rows -- enter the LLM scorecard while leaving the Gate scorecard, its exit code,
// and the end-to-end (production) decision untouched. This is exactly why isolated
// mode exists: measure the LLM on hard cases the gate hides (here, a Leak).
func TestScoreLLMIsolated(t *testing.T) {
	rows := []bench.VerdictRow{
		{URL: "cert", Gate: bench.GateCertainAccept, Label: bench.LabelCareerPage, Category: bench.CategoryHubATSRoot, LLMVotes: []bool{true}},
		{URL: "rej", Gate: bench.GateReject, Label: bench.LabelNotCareerPage, Category: bench.CategoryAggregator, LLMVotes: []bool{false}},
		{URL: "leak", Gate: bench.GateReject, Label: bench.LabelCareerPage, Category: bench.CategoryHubSelfHosted, LLMVotes: []bool{true}},
	}
	got := bench.Score(rows)

	// LLM scorecard measures all three voted rows: cert(tp), rej(tn), leak(tp).
	wantLLM := bench.LLMScorecard{
		ClassScore: bench.ClassScore{TP: 2, FP: 0, FN: 0, TN: 1, Total: 3, Precision: 1, Recall: 1, Accuracy: 1, F1: 1},
		FlipRate:   0,
		Flips:      []string{},
	}
	if !reflect.DeepEqual(got.LLM, wantLLM) {
		t.Errorf("LLM = %+v, want %+v", got.LLM, wantLLM)
	}

	// Gate is orthogonal: the isolated votes do not save the leak from the exit code.
	if !reflect.DeepEqual(got.Gate.Leaks, []string{"leak"}) {
		t.Errorf("Gate.Leaks = %v, want [leak]", got.Gate.Leaks)
	}
	if !got.Failed() {
		t.Errorf("Failed() = false, want true (the leak must fail the run)")
	}

	// End-to-end ignores the isolated votes on cert/rej: cert=>positive, both
	// rejects=>negative regardless of votes. leak is a rejected real page => FN.
	wantE2E := bench.ClassScore{TP: 1, FP: 0, FN: 1, TN: 1, Total: 3, Precision: 1, Recall: 0.5, Accuracy: 0.6667, F1: 0.6667}
	if !reflect.DeepEqual(got.EndToEnd.Overall, wantE2E) {
		t.Errorf("EndToEnd.Overall = %+v, want %+v", got.EndToEnd.Overall, wantE2E)
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
				{URL: "ats", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateCertainAccept},                              // tp
				{URL: "hub", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateUncertain, LLMVotes: []bool{true}},       // tp
				{URL: "trap-fp", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain, LLMVotes: []bool{true}}, // fp
				{URL: "trap-tn", Category: bench.CategoryCultureAbout, Label: bench.LabelNotCareerPage, Gate: bench.GateUncertain, LLMVotes: []bool{false}},
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
