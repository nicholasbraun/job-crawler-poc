package postgres_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/uuid"
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

// TestCountryConstraintRoundTrip drives the Country Constraint column (ADR-0028,
// migration 0016) through Create/Get: the target Countries survive the round trip,
// and a definition created with no Countries reads back as an empty (non-nil,
// non-panicking) slice via the NOT NULL DEFAULT '{}' column.
func TestCountryConstraintRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	defs := postgres.NewCrawlDefinitionRepository(pool)

	t.Run("countries survive the round trip", func(t *testing.T) {
		def := &crawler.CrawlDefinition{
			Name:      "go-de-at",
			Kind:      crawler.CrawlKindKeyword,
			Keywords:  []string{"go"},
			Countries: []string{"DE", "AT"},
			MaxDepth:  1,
		}
		if err := defs.Create(t.Context(), def); err != nil {
			t.Fatalf("creating definition: %v", err)
		}

		got, err := defs.Get(t.Context(), def.ID)
		if err != nil {
			t.Fatalf("getting definition: %v", err)
		}
		if want := []string{"DE", "AT"}; !slices.Equal(got.Countries, want) {
			t.Errorf("countries: got %v, want %v", got.Countries, want)
		}
	})

	t.Run("no countries reads back as an empty slice", func(t *testing.T) {
		def := &crawler.CrawlDefinition{
			Name:     "go-anywhere",
			Kind:     crawler.CrawlKindKeyword,
			Keywords: []string{"go"},
			MaxDepth: 1,
		}
		if err := defs.Create(t.Context(), def); err != nil {
			t.Fatalf("creating definition: %v", err)
		}

		got, err := defs.Get(t.Context(), def.ID)
		if err != nil {
			t.Fatalf("getting definition: %v", err)
		}
		if len(got.Countries) != 0 {
			t.Errorf("countries: got %v, want empty (anywhere)", got.Countries)
		}
	})
}

// TestAppendSeedURL drives the additive Seed mutation (ADR-0018): a new Seed is
// appended, re-adding one is an idempotent no-op (no error, no duplicate), and an
// unknown definition maps to crawler.ErrNotFound.
func TestAppendSeedURL(t *testing.T) {
	pool := newTestPool(t)
	defs := postgres.NewCrawlDefinitionRepository(pool)

	def := &crawler.CrawlDefinition{
		Name:     "discovery",
		Kind:     crawler.CrawlKindDiscovery,
		SeedURLs: []string{"https://a.example.com"},
		MaxDepth: 1,
	}
	if err := defs.Create(t.Context(), def); err != nil {
		t.Fatalf("creating definition: %v", err)
	}

	if err := defs.AppendSeedURL(t.Context(), def.ID, "https://b.example.com"); err != nil {
		t.Fatalf("appending a new seed: %v", err)
	}
	// Idempotent: re-adding the same seed is a no-op — no error, no duplicate.
	if err := defs.AppendSeedURL(t.Context(), def.ID, "https://b.example.com"); err != nil {
		t.Fatalf("re-appending an existing seed: %v", err)
	}

	got, err := defs.Get(t.Context(), def.ID)
	if err != nil {
		t.Fatalf("getting definition: %v", err)
	}
	// array_append preserves order, so the seeds stay in insertion order and the
	// re-add adds nothing.
	want := []string{"https://a.example.com", "https://b.example.com"}
	if !slices.Equal(got.SeedURLs, want) {
		t.Errorf("seed_urls: got %v, want %v", got.SeedURLs, want)
	}

	// An unknown definition is distinguished from "already present" via
	// RowsAffected and maps to ErrNotFound.
	if err := defs.AppendSeedURL(t.Context(), uuid.New(), "https://c.example.com"); !errors.Is(err, crawler.ErrNotFound) {
		t.Errorf("unknown definition: got %v, want ErrNotFound", err)
	}
}
