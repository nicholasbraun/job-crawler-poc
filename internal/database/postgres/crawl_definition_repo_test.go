package postgres_test

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

// TestSeededCollectionDefinition asserts migration 0019 seeds the singleton
// Collection Crawl definition (ADR-0036): resolvable at the fixed
// CollectionDefinitionID with kind 'collection', and its stored url_filter
// round-trips equal to DefaultCollectionURLFilterConfig() so the SQL seed and the
// Go helper cannot drift.
func TestSeededCollectionDefinition(t *testing.T) {
	pool := newTestPool(t)
	defs := postgres.NewCrawlDefinitionRepository(pool)

	def, err := defs.Get(t.Context(), crawler.CollectionDefinitionID)
	if err != nil {
		t.Fatalf("getting seeded collection definition: %v", err)
	}
	if def.Kind != crawler.CrawlKindCollection {
		t.Errorf("kind = %q, want %q", def.Kind, crawler.CrawlKindCollection)
	}
	if !reflect.DeepEqual(def.URLFilter, crawler.DefaultCollectionURLFilterConfig()) {
		t.Errorf("seeded url_filter drifted from DefaultCollectionURLFilterConfig()\n got: %+v\nwant: %+v",
			def.URLFilter, crawler.DefaultCollectionURLFilterConfig())
	}
}

// TestSingleCollectionDefinition drives the singleton-collection partial unique
// index (migration 0019): the migration already seeded one collection definition,
// so a second Create with kind 'collection' is rejected by the index.
func TestSingleCollectionDefinition(t *testing.T) {
	pool := newTestPool(t)
	defs := postgres.NewCrawlDefinitionRepository(pool)

	err := defs.Create(t.Context(), &crawler.CrawlDefinition{
		Name:     "second collection",
		Kind:     crawler.CrawlKindCollection,
		MaxDepth: 1,
	})
	if err == nil {
		t.Fatal("creating a second collection definition should be rejected by the singleton index")
	}
}

// TestSingleDiscoveryDefinition drives the singleton-discovery partial unique
// index (migration 0010) through the repository: at most one discovery
// definition, translated to crawler.ErrDiscoveryDefinitionExists (ADR-0017),
// while non-discovery definitions remain unconstrained. The kind value on the
// non-discovery fixtures is immaterial — only "discovery" is index-constrained.
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

	// Non-discovery definitions are outside the index predicate and accumulate
	// freely; the kind value is immaterial to the index.
	for _, name := range []string{"non-discovery-a", "non-discovery-b"} {
		if err := defs.Create(t.Context(), &crawler.CrawlDefinition{
			Name:     name,
			Kind:     crawler.CrawlKind("test"),
			MaxDepth: 1,
		}); err != nil {
			t.Fatalf("non-discovery definition %q should insert: %v", name, err)
		}
	}
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
