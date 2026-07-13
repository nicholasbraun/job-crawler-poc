// This file is the pure per-metric comparison of two Reports: Diff folds A and B
// into a fixed-order slice of named scalar deltas (B relative to A) plus the
// symmetric difference of their gate violations. It runs no IO -- the diff verb's
// rendering lives in cmd/llmbench/diff.go.
package bench

import "math"

// ScalarDelta is one named metric's A/B values and signed change (B-A, 4dp).
type ScalarDelta struct {
	Name  string  `json:"name"`
	A     float64 `json:"a"`
	B     float64 `json:"b"`
	Delta float64 `json:"delta"`
}

// ReportDiff is the pure per-metric comparison of two Reports (B relative to A).
type ReportDiff struct {
	Metrics           []ScalarDelta `json:"metrics"`
	ViolationsAdded   []Violation   `json:"violations_added"`   // in B, not in A (key = URL+Category)
	ViolationsRemoved []Violation   `json:"violations_removed"` // in A, not in B
}

// Diff compares two Reports metric by metric, reporting B relative to A. The
// scalar metrics appear in a fixed order (gate, then llm, then end-to-end overall,
// then each present category); a category absent from both reports is skipped, and
// a category present on only one side reads the zero ClassScore for the missing
// side. Violations are compared by identity (URL+Category).
func Diff(a, b Report) ReportDiff {
	metrics := []ScalarDelta{}
	add := func(name string, av, bv float64) {
		metrics = append(metrics, ScalarDelta{Name: name, A: av, B: bv, Delta: round4(bv - av)})
	}

	add("gate.llm-call-rate", a.Gate.LLMCallRate, b.Gate.LLMCallRate)
	add("gate.leaks", float64(len(a.Gate.Leaks)), float64(len(b.Gate.Leaks)))
	add("gate.false-certains", float64(len(a.Gate.FalseCertains)), float64(len(b.Gate.FalseCertains)))
	add("gate.accepted-false-certains", float64(len(a.Gate.AcceptedFalseCertains)), float64(len(b.Gate.AcceptedFalseCertains)))
	add("gate.violations", float64(len(a.Gate.Violations)), float64(len(b.Gate.Violations)))

	add("llm.precision", a.LLM.Precision, b.LLM.Precision)
	add("llm.recall", a.LLM.Recall, b.LLM.Recall)
	add("llm.accuracy", a.LLM.Accuracy, b.LLM.Accuracy)
	add("llm.f1", a.LLM.F1, b.LLM.F1)
	add("llm.flip-rate", a.LLM.FlipRate, b.LLM.FlipRate)

	add("e2e.precision", a.EndToEnd.Overall.Precision, b.EndToEnd.Overall.Precision)
	add("e2e.recall", a.EndToEnd.Overall.Recall, b.EndToEnd.Overall.Recall)
	add("e2e.accuracy", a.EndToEnd.Overall.Accuracy, b.EndToEnd.Overall.Accuracy)
	add("e2e.f1", a.EndToEnd.Overall.F1, b.EndToEnd.Overall.F1)

	for _, cat := range AllCategories {
		ca, okA := a.EndToEnd.ByCategory[cat]
		cb, okB := b.EndToEnd.ByCategory[cat]
		if !okA && !okB {
			continue
		}
		add("e2e."+string(cat)+".precision", ca.Precision, cb.Precision)
		add("e2e."+string(cat)+".recall", ca.Recall, cb.Recall)
		add("e2e."+string(cat)+".f1", ca.F1, cb.F1)
	}

	added, removed := diffViolations(a.Gate.Violations, b.Gate.Violations)
	return ReportDiff{Metrics: metrics, ViolationsAdded: added, ViolationsRemoved: removed}
}

// round4 rounds to four decimals, matching the Scorer's rate rounding so a delta
// of two already-rounded rates carries no floating-point tail.
func round4(f float64) float64 { return math.Round(f*10000) / 10000 }

// diffViolations returns the symmetric difference of two violation lists keyed on
// URL+Category (a changed Want/Got at the same identity is neither added nor
// removed). Added preserves b-order; removed preserves a-order.
func diffViolations(a, b []Violation) (added, removed []Violation) {
	added, removed = []Violation{}, []Violation{}
	key := func(v Violation) string { return v.URL + "\x00" + string(v.Category) }

	inA := map[string]struct{}{}
	for _, v := range a {
		inA[key(v)] = struct{}{}
	}
	inB := map[string]struct{}{}
	for _, v := range b {
		inB[key(v)] = struct{}{}
	}
	for _, v := range b {
		if _, ok := inA[key(v)]; !ok {
			added = append(added, v)
		}
	}
	for _, v := range a {
		if _, ok := inB[key(v)]; !ok {
			removed = append(removed, v)
		}
	}
	return added, removed
}
