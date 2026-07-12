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
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
