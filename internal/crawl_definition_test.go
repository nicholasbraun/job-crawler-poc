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
	t.Run("final-rung score floats lock the confidence-score design invariants", func(t *testing.T) {
		// The final-rung design (ADR-0016) is a set of RELATIONSHIPS between the seeded
		// weights and the two thresholds. They are asserted as relationships, not raw
		// magic numbers, so the test survives coarse re-tuning while still locking the
		// behavior each threshold placement buys. Every weight is a positive
		// contribution; the thresholds are ordered and non-degenerate.
		if cfg.CareerKeywordWeight <= 0 || cfg.JobLinkWeight <= 0 || cfg.TitleStrengthWeight <= 0 ||
			cfg.JSONLDHubWeight <= 0 || cfg.ATSEmbedWeight <= 0 {
			t.Errorf("weights must be positive, got career=%v joblink=%v title=%v jsonld=%v ats=%v",
				cfg.CareerKeywordWeight, cfg.JobLinkWeight, cfg.TitleStrengthWeight, cfg.JSONLDHubWeight, cfg.ATSEmbedWeight)
		}
		if cfg.JobLinkSaturationCount <= 0 {
			t.Errorf("JobLinkSaturationCount = %v, want > 0 (a positive saturation count)", cfg.JobLinkSaturationCount)
		}
		// Extract Gate's own saturation count (ADR-0019, #115): a zero would make its
		// reject rung fire on nothing, silently reopening the extract-call rate.
		if cfg.ExtractJobLinkSaturationCount <= 0 {
			t.Errorf("ExtractJobLinkSaturationCount = %v, want > 0 (the extract-path saturation count)", cfg.ExtractJobLinkSaturationCount)
		}
		if !(cfg.RejectThreshold > 0 && cfg.RejectThreshold < cfg.CertainThreshold) {
			t.Errorf("want 0 < RejectThreshold < CertainThreshold, got reject=%v certain=%v", cfg.RejectThreshold, cfg.CertainThreshold)
		}
		// Lever D (#101): a page whose only signal is the weakest career keyword
		// auto-rejects, so the weak keyword alone must not clear reject.
		if cfg.CareerKeywordWeight > cfg.RejectThreshold {
			t.Errorf("CareerKeywordWeight %v must be <= RejectThreshold %v so a weak-keyword-only page auto-rejects", cfg.CareerKeywordWeight, cfg.RejectThreshold)
		}
		// Zero-Leak guard (#101): a keyword page that ALSO reads as a careers hub
		// (keyword + title strength) must clear reject, so the raised RejectThreshold
		// never drops a real career sub-page.
		if cfg.CareerKeywordWeight+cfg.TitleStrengthWeight <= cfg.RejectThreshold {
			t.Errorf("CareerKeywordWeight+TitleStrengthWeight %v must exceed RejectThreshold %v so a careers-hub page never leaks into reject", cfg.CareerKeywordWeight+cfg.TitleStrengthWeight, cfg.RejectThreshold)
		}
		// False-Certain guard, lexical-only (#101): lexical evidence alone (keyword +
		// title strength) stays below CertainThreshold, so certain still requires a
		// Structural Signal (an ATS embed, a JSON-LD hub, or a keyword + dense index).
		if cfg.CareerKeywordWeight+cfg.TitleStrengthWeight >= cfg.CertainThreshold {
			t.Errorf("CareerKeywordWeight+TitleStrengthWeight %v must stay below CertainThreshold %v so lexical evidence alone never certain-accepts", cfg.CareerKeywordWeight+cfg.TitleStrengthWeight, cfg.CertainThreshold)
		}
		// False-Certain guard, links-only (retained #98): saturated same-host links
		// alone stay uncertain -- certain requires the career keyword too.
		if cfg.JobLinkWeight >= cfg.CertainThreshold {
			t.Errorf("JobLinkWeight %v must stay below CertainThreshold %v so saturated links alone stay uncertain", cfg.JobLinkWeight, cfg.CertainThreshold)
		}
		// Retained certain conditions: a career keyword + a dense same-host index, an
		// ATS embed, or a JSON-LD hub each certain-accepts on its own.
		if cfg.CareerKeywordWeight+cfg.JobLinkWeight < cfg.CertainThreshold {
			t.Errorf("CareerKeywordWeight+JobLinkWeight %v must reach CertainThreshold %v so a keyword + dense index certain-accepts", cfg.CareerKeywordWeight+cfg.JobLinkWeight, cfg.CertainThreshold)
		}
		if cfg.ATSEmbedWeight < cfg.CertainThreshold {
			t.Errorf("ATSEmbedWeight %v must reach CertainThreshold %v so an ATS embed alone certain-accepts", cfg.ATSEmbedWeight, cfg.CertainThreshold)
		}
		if cfg.JSONLDHubWeight < cfg.CertainThreshold {
			t.Errorf("JSONLDHubWeight %v must reach CertainThreshold %v so a JSON-LD hub alone certain-accepts", cfg.JSONLDHubWeight, cfg.CertainThreshold)
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
