package bench_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// richRows drives Score to a Report that exercises every tagged field: a Leak, a
// False-Certain, a structural Violation with a flipping LLM vote, and a clean
// negative. Every serialization test scores these so the numbers are real rather
// than hand-computed.
func richRows() []bench.VerdictRow {
	return []bench.VerdictRow{
		{URL: "leak", Category: bench.CategoryHubSelfHosted, Label: bench.LabelCareerPage, Gate: bench.GateReject},
		{URL: "fc", Category: bench.CategoryJobPostingSingle, Label: bench.LabelNotCareerPage, Gate: bench.GateCertainAccept},
		{URL: "ats", Category: bench.CategoryHubATSRoot, Label: bench.LabelCareerPage, Gate: bench.GateUncertain, LLMVotes: []bool{true, false}},
		{URL: "ok", Category: bench.CategoryAggregator, Label: bench.LabelNotCareerPage, Gate: bench.GateReject},
	}
}

// TestReportRoundTrip proves every tagged field survives EncodeReport ->
// DecodeReport: the Gate lists (Leaks/FalseCertains/Violations with GateOutcome
// Want/Got), the LLM scorecard + Flips, the per-category end-to-end map, and the
// review queue. A Score-produced Report leaves no nil slices/maps, so DeepEqual
// holds without any []T{}/nil coercion.
func TestReportRoundTrip(t *testing.T) {
	orig := bench.Score(richRows())

	var buf bytes.Buffer
	if err := bench.EncodeReport(&buf, orig); err != nil {
		t.Fatalf("EncodeReport: %v", err)
	}
	got, err := bench.DecodeReport(&buf)
	if err != nil {
		t.Fatalf("DecodeReport: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n orig = %+v\n got  = %+v", orig, got)
	}
}

// TestReportJSONShape locks the top-level JSON keys and the GateOutcome string
// encoding: the ATS violation's Want is certain-accept and Got is uncertain, so
// the serialized violation must carry those exact strings.
func TestReportJSONShape(t *testing.T) {
	data, err := json.Marshal(bench.Score(richRows()))
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"gate", "llm", "end_to_end", "review_queue"} {
		if _, ok := m[key]; !ok {
			t.Errorf("top-level key %q missing from report JSON", key)
		}
	}

	gate, ok := m["gate"].(map[string]any)
	if !ok {
		t.Fatalf("gate is not an object: %T", m["gate"])
	}
	violations, ok := gate["violations"].([]any)
	if !ok || len(violations) != 1 {
		t.Fatalf("gate.violations = %v, want exactly one", gate["violations"])
	}
	v, ok := violations[0].(map[string]any)
	if !ok {
		t.Fatalf("gate.violations[0] is not an object: %T", violations[0])
	}
	if v["want"] != "certain-accept" {
		t.Errorf("gate.violations[0].want = %v, want %q", v["want"], "certain-accept")
	}
	if v["got"] != "uncertain" {
		t.Errorf("gate.violations[0].got = %v, want %q", v["got"], "uncertain")
	}
}

// TestGateOutcomeJSONRoundTrip locks each outcome's quoted-string encoding and
// its inverse, plus that an unknown string is a decode error rather than a silent
// GateReject.
func TestGateOutcomeJSONRoundTrip(t *testing.T) {
	tests := []struct {
		outcome bench.GateOutcome
		json    string
	}{
		{bench.GateReject, `"reject"`},
		{bench.GateCertainAccept, `"certain-accept"`},
		{bench.GateUncertain, `"uncertain"`},
	}
	for _, tt := range tests {
		t.Run(tt.outcome.String(), func(t *testing.T) {
			data, err := json.Marshal(tt.outcome)
			if err != nil {
				t.Fatalf("Marshal(%v): %v", tt.outcome, err)
			}
			if string(data) != tt.json {
				t.Errorf("Marshal(%v) = %s, want %s", tt.outcome, data, tt.json)
			}
			var got bench.GateOutcome
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("Unmarshal(%s): %v", data, err)
			}
			if got != tt.outcome {
				t.Errorf("Unmarshal(%s) = %v, want %v", data, got, tt.outcome)
			}
		})
	}

	t.Run("unknown-errors", func(t *testing.T) {
		var got bench.GateOutcome
		if err := json.Unmarshal([]byte(`"bogus"`), &got); err == nil {
			t.Errorf("Unmarshal(%q) = nil error, want an unknown-outcome error", "bogus")
		}
	})
}
