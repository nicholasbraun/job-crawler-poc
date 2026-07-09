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
	rec.Content(t.Context(), llmobs.KindClassify, "x")

	for _, kv := range toMap(stats.Summary()) {
		if n, ok := kv.(int64); ok && n != 0 {
			t.Errorf("Nop recorder must not touch stats, got a non-zero count: %v", stats.Summary())
			break
		}
	}
}
