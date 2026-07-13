package bench_test

import (
	"reflect"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// TestDiff_Metrics locks the fixed 14-metric base order (gate, llm, e2e overall)
// and their signed deltas when ByCategory is nil on both sides, and that round4
// strips the floating-point tail a raw subtraction would leave (e.g. 0.6667-0.3333).
func TestDiff_Metrics(t *testing.T) {
	a := bench.Report{
		Gate: bench.GateScorecard{LLMCallRate: 0.5, Leaks: []string{"x"}},
		LLM: bench.LLMScorecard{
			ClassScore: bench.ClassScore{Precision: 0.8, Recall: 0.6, Accuracy: 0.7, F1: 0.6857},
			FlipRate:   0.2,
		},
		EndToEnd: bench.EndToEndScorecard{
			Overall: bench.ClassScore{Precision: 0.3333, Recall: 0.9, Accuracy: 0.9, F1: 0.9},
		},
	}
	b := bench.Report{
		Gate: bench.GateScorecard{LLMCallRate: 0.25, Leaks: []string{}},
		LLM: bench.LLMScorecard{
			ClassScore: bench.ClassScore{Precision: 0.9, Recall: 0.7, Accuracy: 0.8, F1: 0.7778},
			FlipRate:   0.1,
		},
		EndToEnd: bench.EndToEndScorecard{
			Overall: bench.ClassScore{Precision: 0.6667, Recall: 1.0, Accuracy: 0.95, F1: 0.95},
		},
	}

	want := []bench.ScalarDelta{
		{Name: "gate.llm-call-rate", A: 0.5, B: 0.25, Delta: -0.25},
		{Name: "gate.leaks", A: 1, B: 0, Delta: -1},
		{Name: "gate.false-certains", A: 0, B: 0, Delta: 0},
		{Name: "gate.accepted-false-certains", A: 0, B: 0, Delta: 0},
		{Name: "gate.violations", A: 0, B: 0, Delta: 0},
		{Name: "llm.precision", A: 0.8, B: 0.9, Delta: 0.1},
		{Name: "llm.recall", A: 0.6, B: 0.7, Delta: 0.1},
		{Name: "llm.accuracy", A: 0.7, B: 0.8, Delta: 0.1},
		{Name: "llm.f1", A: 0.6857, B: 0.7778, Delta: 0.0921},
		{Name: "llm.flip-rate", A: 0.2, B: 0.1, Delta: -0.1},
		{Name: "e2e.precision", A: 0.3333, B: 0.6667, Delta: 0.3334},
		{Name: "e2e.recall", A: 0.9, B: 1.0, Delta: 0.1},
		{Name: "e2e.accuracy", A: 0.9, B: 0.95, Delta: 0.05},
		{Name: "e2e.f1", A: 0.9, B: 0.95, Delta: 0.05},
	}
	if got := bench.Diff(a, b).Metrics; !reflect.DeepEqual(got, want) {
		t.Errorf("Diff Metrics mismatch:\n got  = %+v\n want = %+v", got, want)
	}
}

// TestDiff_PerCategory checks the per-category tail: a category present on both
// sides diffs both ClassScores, a category present on only one side reads the zero
// ClassScore for the missing side, categories absent from both are skipped, and
// the tail follows AllCategories order.
func TestDiff_PerCategory(t *testing.T) {
	a := bench.Report{
		EndToEnd: bench.EndToEndScorecard{
			ByCategory: map[bench.Category]bench.ClassScore{
				bench.CategoryHubATSRoot: {Precision: 1, Recall: 1, F1: 1, Total: 1},
			},
		},
	}
	b := bench.Report{
		EndToEnd: bench.EndToEndScorecard{
			ByCategory: map[bench.Category]bench.ClassScore{
				bench.CategoryHubATSRoot:   {Precision: 0.5, Recall: 0.5, F1: 0.5, Total: 2},
				bench.CategoryCultureAbout: {Precision: 0.4, Recall: 0.3, F1: 0.2, Total: 1},
			},
		},
	}

	metrics := bench.Diff(a, b).Metrics
	if len(metrics) != 20 {
		t.Fatalf("len(Metrics) = %d, want 20 (14 base + 6 per-category)", len(metrics))
	}
	wantTail := []bench.ScalarDelta{
		{Name: "e2e.hub_ats_root.precision", A: 1, B: 0.5, Delta: -0.5},
		{Name: "e2e.hub_ats_root.recall", A: 1, B: 0.5, Delta: -0.5},
		{Name: "e2e.hub_ats_root.f1", A: 1, B: 0.5, Delta: -0.5},
		{Name: "e2e.culture_about.precision", A: 0, B: 0.4, Delta: 0.4},
		{Name: "e2e.culture_about.recall", A: 0, B: 0.3, Delta: 0.3},
		{Name: "e2e.culture_about.f1", A: 0, B: 0.2, Delta: 0.2},
	}
	if got := metrics[14:]; !reflect.DeepEqual(got, wantTail) {
		t.Errorf("per-category tail mismatch:\n got  = %+v\n want = %+v", got, wantTail)
	}
}

// TestDiff_Violations checks the symmetric difference of gate violations keyed on
// URL+Category: disjoint sets split into added/removed, and the same identity on
// both sides (even with a differing Got) is neither added nor removed.
func TestDiff_Violations(t *testing.T) {
	ats := bench.Violation{URL: "ats", Category: bench.CategoryHubATSRoot, Want: bench.GateCertainAccept, Got: bench.GateReject}
	agg := bench.Violation{URL: "agg", Category: bench.CategoryAggregator, Want: bench.GateReject, Got: bench.GateUncertain}

	t.Run("disjoint", func(t *testing.T) {
		a := bench.Report{Gate: bench.GateScorecard{Violations: []bench.Violation{ats}}}
		b := bench.Report{Gate: bench.GateScorecard{Violations: []bench.Violation{agg}}}
		d := bench.Diff(a, b)
		if want := []bench.Violation{agg}; !reflect.DeepEqual(d.ViolationsAdded, want) {
			t.Errorf("ViolationsAdded = %+v, want %+v", d.ViolationsAdded, want)
		}
		if want := []bench.Violation{ats}; !reflect.DeepEqual(d.ViolationsRemoved, want) {
			t.Errorf("ViolationsRemoved = %+v, want %+v", d.ViolationsRemoved, want)
		}
	})

	t.Run("same-identity-different-got", func(t *testing.T) {
		atsUncertain := ats
		atsUncertain.Got = bench.GateUncertain
		a := bench.Report{Gate: bench.GateScorecard{Violations: []bench.Violation{ats}}}
		b := bench.Report{Gate: bench.GateScorecard{Violations: []bench.Violation{atsUncertain}}}
		d := bench.Diff(a, b)
		if len(d.ViolationsAdded) != 0 {
			t.Errorf("ViolationsAdded = %+v, want empty (same URL+Category)", d.ViolationsAdded)
		}
		if len(d.ViolationsRemoved) != 0 {
			t.Errorf("ViolationsRemoved = %+v, want empty (same URL+Category)", d.ViolationsRemoved)
		}
	})
}
