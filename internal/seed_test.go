package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestSeedsFromURLs(t *testing.T) {
	t.Run("maps each url to a seed with empty provenance", func(t *testing.T) {
		urls := []string{"https://acme.com/careers", "https://globex.io"}
		seeds := crawler.SeedsFromURLs(urls)

		if len(seeds) != len(urls) {
			t.Fatalf("want %d seeds, got %d", len(urls), len(seeds))
		}
		for i, s := range seeds {
			if s.URL != urls[i] {
				t.Errorf("seed %d URL: want %q, got %q", i, urls[i], s.URL)
			}
			// Discovery roams: bare URLs carry no fence and no attribution.
			if s.Scope != "" {
				t.Errorf("seed %d Scope: want empty (roam), got %q", i, s.Scope)
			}
			if s.Owner != "" {
				t.Errorf("seed %d Owner: want empty (roam), got %q", i, s.Owner)
			}
		}
	})

	t.Run("returns non-nil empty slice for empty input", func(t *testing.T) {
		seeds := crawler.SeedsFromURLs(nil)
		if seeds == nil {
			t.Fatal("want non-nil slice, got nil")
		}
		if len(seeds) != 0 {
			t.Errorf("want empty slice, got %v", seeds)
		}
	})
}
