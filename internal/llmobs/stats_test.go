package llmobs_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
)

// toMap turns the flat key/value slice Summary returns into a lookup for
// assertions.
func toMap(kv []any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			continue
		}
		m[key] = kv[i+1]
	}
	return m
}

func TestStatsSummary(t *testing.T) {
	stats := &llmobs.Stats{}
	// Drive stats through the exported Recorder (nil metrics/probe): three
	// classify calls (ok, error, timeout), one gate skip, two contents (unique,
	// since a nil probe never reports duplicates).
	rec := llmobs.NewRecorder(nil, nil, stats)
	ctx := t.Context()
	rec.Call(ctx, llmobs.KindClassify, llmobs.OutcomeOK, 0)
	rec.Call(ctx, llmobs.KindClassify, llmobs.OutcomeError, 0)
	rec.Call(ctx, llmobs.KindClassify, llmobs.OutcomeTimeout, 0)
	rec.Gated(ctx, llmobs.KindClassify, llmobs.ReasonCertain)
	rec.Content(ctx, llmobs.KindClassify, "a")
	rec.Content(ctx, llmobs.KindClassify, "b")

	m := toMap(stats.Summary())

	wantInt := map[string]int64{
		"classify_calls":    3,
		"classify_errors":   1,
		"classify_timeouts": 1,
		"classify_gated":    1,
		"extract_calls":     0,
	}
	for key, want := range wantInt {
		if got, ok := m[key].(int64); !ok || got != want {
			t.Errorf("%s = %v, want %d", key, m[key], want)
		}
	}

	wantRate := map[string]float64{
		"classify_gate_hit_rate": 0.25,   // gated 1 / (gated 1 + calls 3)
		"classify_error_rate":    0.3333, // errors 1 / calls 3
		"classify_timeout_rate":  0.3333, // timeouts 1 / calls 3
		"classify_dup_ratio":     0.0,    // no duplicates (nil probe)
		"extract_gate_hit_rate":  0.0,    // 0/0 guarded to 0
	}
	for key, want := range wantRate {
		if got, ok := m[key].(float64); !ok || got != want {
			t.Errorf("%s = %v, want %v", key, m[key], want)
		}
	}
}
