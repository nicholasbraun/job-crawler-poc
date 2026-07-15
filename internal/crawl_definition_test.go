package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestDefaultLLMGateConfig(t *testing.T) {
	cfg := crawler.DefaultLLMGateConfig()
	t.Run("reject-path signals shed software-docs and media-taxonomy paths", func(t *testing.T) {
		for _, want := range []string{"docs", "tag", "category"} {
			if !containsString(cfg.RejectPathSignals, want) {
				t.Errorf("RejectPathSignals = %v, missing %q", cfg.RejectPathSignals, want)
			}
		}
	})
	t.Run("final-rung score floats are seeded behavior-neutral", func(t *testing.T) {
		// Both weak signals present sum to CareerKeywordWeight+JobLinkWeight; a
		// CertainThreshold strictly above that sum means nothing certain-accepts
		// from the final rung, and a zero RejectThreshold means only a no-signal
		// page (score 0) rejects — reproducing the pre-score `careerish || listsJobs`.
		maxScore := cfg.CareerKeywordWeight + cfg.JobLinkWeight
		if cfg.CareerKeywordWeight <= 0 || cfg.JobLinkWeight <= 0 {
			t.Errorf("weights must be positive, got career=%v joblink=%v", cfg.CareerKeywordWeight, cfg.JobLinkWeight)
		}
		if cfg.CertainThreshold <= maxScore {
			t.Errorf("CertainThreshold %v must exceed max final-rung score %v (nothing certain-accepts)", cfg.CertainThreshold, maxScore)
		}
		if cfg.RejectThreshold != 0 {
			t.Errorf("RejectThreshold = %v, want 0 (reject only a no-signal page)", cfg.RejectThreshold)
		}
	})
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
