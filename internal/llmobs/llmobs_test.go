package llmobs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
)

// timeoutErr is a net.Error that reports a timeout, standing in for what an
// http.Client Timeout surfaces.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want llmobs.Outcome
	}{
		{"nil is ok", nil, llmobs.OutcomeOK},
		{"deadline exceeded is timeout", context.DeadlineExceeded, llmobs.OutcomeTimeout},
		{"wrapped deadline is timeout", fmt.Errorf("sending request: %w", context.DeadlineExceeded), llmobs.OutcomeTimeout},
		{"net timeout is timeout", timeoutErr{}, llmobs.OutcomeTimeout},
		{"wrapped net timeout is timeout", fmt.Errorf("sending request: %w", timeoutErr{}), llmobs.OutcomeTimeout},
		{"generic error is error", errors.New("boom"), llmobs.OutcomeError},
		{"non-200 status is error", errors.New("openrouter: status 500: oops"), llmobs.OutcomeError},
		{"canceled is error not timeout", context.Canceled, llmobs.OutcomeError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llmobs.Classify(tt.err); got != tt.want {
				t.Errorf("Classify(%v) = %q, want %q", tt.err, got, tt.want)
			}
		})
	}
}

// TestShadowVerdictOf pins the mapping from one Shadow Extraction's result to the
// verdict recorded for it (ADR-0044), including the error-wins rule: on the error
// path the Extraction is the zero value, so its verdict field means nothing and a
// stale true must never be counted as a false-drop.
func TestShadowVerdictOf(t *testing.T) {
	tests := []struct {
		name         string
		err          error
		isJobPosting bool
		want         llmobs.ShadowVerdict
	}{
		{"a posting the gate rejected is an accept", nil, true, llmobs.ShadowAccept},
		{"an abstain is an abstain", nil, false, llmobs.ShadowAbstain},
		{"a failed call is an error verdict", errors.New("boom"), false, llmobs.ShadowError},
		{"an error wins over a stale verdict", errors.New("boom"), true, llmobs.ShadowError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := llmobs.ShadowVerdictOf(tt.err, tt.isJobPosting); got != tt.want {
				t.Errorf("ShadowVerdictOf(%v, %v) = %q, want %q", tt.err, tt.isJobPosting, got, tt.want)
			}
		})
	}
}

func TestDupProbeNilClientReportsUnique(t *testing.T) {
	probe := llmobs.NewDupProbe(nil)

	dup, err := probe.Observe(t.Context(), llmobs.KindClassify, "some content")
	if err != nil {
		t.Fatalf("Observe returned error: %v", err)
	}
	if dup {
		t.Error("a nil-client probe must report every content as unique")
	}
}

func TestNopRecorderRecordsNothing(t *testing.T) {
	stats := &llmobs.Stats{}
	// A Nop recorder ignores everything; a separate stats stays zero, proving the
	// call sites are inert when instrumentation is not wired.
	rec := llmobs.Nop()
	rec.Call(t.Context(), llmobs.KindClassify, llmobs.OutcomeError, 0)
	rec.Gated(t.Context(), llmobs.KindExtract, llmobs.ReasonIrrelevant)
	rec.Shadow(t.Context(), llmobs.ShadowAccept, "positive_evidence")
	rec.ShadowDropped(t.Context(), "positive_evidence")
	rec.Content(t.Context(), llmobs.KindClassify, "x")

	for _, kv := range toMap(stats.Summary()) {
		if n, ok := kv.(int64); ok && n != 0 {
			t.Errorf("Nop recorder must not touch stats, got a non-zero count: %v", stats.Summary())
			break
		}
	}
}
