package postgres_test

import (
	"errors"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

// TestSingleDiscoveryDefinition drives the singleton-discovery partial unique
// index (migration 0010) through the repository: at most one discovery
// definition, translated to crawler.ErrDiscoveryDefinitionExists (ADR-0017),
// while keyword definitions remain unconstrained.
func TestSingleDiscoveryDefinition(t *testing.T) {
	pool := newTestPool(t)
	defs := postgres.NewCrawlDefinitionRepository(pool)

	discovery := func(name string) *crawler.CrawlDefinition {
		return &crawler.CrawlDefinition{
			Name:     name,
			Kind:     crawler.CrawlKindDiscovery,
			SeedURLs: []string{"https://example.com"},
			MaxDepth: 1,
		}
	}

	if err := defs.Create(t.Context(), discovery("first-discovery")); err != nil {
		t.Fatalf("first discovery definition should insert: %v", err)
	}

	err := defs.Create(t.Context(), discovery("second-discovery"))
	if !errors.Is(err, crawler.ErrDiscoveryDefinitionExists) {
		t.Fatalf("second discovery definition: got %v, want ErrDiscoveryDefinitionExists", err)
	}

	// Keyword definitions are outside the index predicate and accumulate freely.
	for _, name := range []string{"keyword-a", "keyword-b"} {
		if err := defs.Create(t.Context(), &crawler.CrawlDefinition{
			Name:     name,
			Kind:     crawler.CrawlKindKeyword,
			Keywords: []string{"go"},
			MaxDepth: 1,
		}); err != nil {
			t.Fatalf("keyword definition %q should insert: %v", name, err)
		}
	}
}
