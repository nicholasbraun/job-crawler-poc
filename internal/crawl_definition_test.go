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
	t.Run("final-rung score floats seed the same-host job-link invariant", func(t *testing.T) {
		// The #98 seeding (ADR-0016) certain-accepts a dense same-host openings index
		// carrying a career keyword, while every weaker combination stays uncertain.
		// The load-bearing invariant that holds False-Certains at zero is
		// JobLinkWeight < CertainThreshold (saturated links ALONE stay uncertain);
		// certain requires the keyword too (CareerKeywordWeight+JobLinkWeight reaches
		// it). A zero RejectThreshold rejects only a no-signal page (score 0).
		if cfg.CareerKeywordWeight <= 0 || cfg.JobLinkWeight <= 0 {
			t.Errorf("weights must be positive, got career=%v joblink=%v", cfg.CareerKeywordWeight, cfg.JobLinkWeight)
		}
		if cfg.JobLinkSaturationCount <= 0 {
			t.Errorf("JobLinkSaturationCount = %v, want > 0 (a positive saturation count)", cfg.JobLinkSaturationCount)
		}
		if cfg.JobLinkWeight >= cfg.CertainThreshold {
			t.Errorf("JobLinkWeight %v must stay below CertainThreshold %v so saturated links alone stay uncertain", cfg.JobLinkWeight, cfg.CertainThreshold)
		}
		if cfg.CareerKeywordWeight+cfg.JobLinkWeight < cfg.CertainThreshold {
			t.Errorf("CareerKeywordWeight+JobLinkWeight %v must reach CertainThreshold %v so a keyword + dense index certain-accepts", cfg.CareerKeywordWeight+cfg.JobLinkWeight, cfg.CertainThreshold)
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
