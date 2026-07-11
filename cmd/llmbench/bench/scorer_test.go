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
