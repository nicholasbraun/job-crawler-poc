package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

func TestJobListingRepository(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewJobListingRepository(pool)

	defA := createDefinition(t, pool, "definition A")
	defB := createDefinition(t, pool, "definition B")

	listing := &crawler.JobListing{
		URL:         "https://netflix.com/jobs/123",
		Title:       "Senior Software Engineer",
		Description: "At Netflix you will be doing cool stuff",
		Company:     "netflix",
		Location:    "Germany",
		Remote:      true,
		TechStack:   []string{"golang", "postgres"},
	}

	t.Run("Save inserts a row keyed by definition_id and url", func(t *testing.T) {
		if err := repo.Save(t.Context(), defA, listing); err != nil {
			t.Fatalf("error saving job listing: %v", err)
		}

		listings, err := repo.Find(t.Context())
		if err != nil {
			t.Fatalf("error finding job listings: %v", err)
		}
		if len(listings) != 1 {
			t.Fatalf("want 1 listing, got %d", len(listings))
		}

		got := listings[0]
		if got.URL != listing.URL {
			t.Errorf("want url %q, got %q", listing.URL, got.URL)
		}
		if got.Company != listing.Company {
			t.Errorf("want company %q, got %q", listing.Company, got.Company)
		}
		if !got.Remote {
			t.Error("want remote true, got false")
		}
		if len(got.TechStack) != 2 {
			t.Fatalf("want 2 tech stack items, got %v", got.TechStack)
		}
	})

	t.Run("re-saving same (definition_id, url) upserts in place", func(t *testing.T) {
		firstSeen, lastSeen := timestamps(t, pool, defA, listing.URL)
		if !firstSeen.Equal(lastSeen) {
			t.Fatalf("on insert first_seen (%v) and last_seen (%v) should match", firstSeen, lastSeen)
		}

		// now() is the transaction start time; a brief pause guarantees the
		// upsert's last_seen advances past the original first_seen.
		time.Sleep(10 * time.Millisecond)

		updated := *listing
		updated.Title = "Staff Software Engineer"
		if err := repo.Save(t.Context(), defA, &updated); err != nil {
			t.Fatalf("error re-saving job listing: %v", err)
		}

		listings, err := repo.Find(t.Context())
		if err != nil {
			t.Fatalf("error finding job listings: %v", err)
		}
		if len(listings) != 1 {
			t.Fatalf("upsert should not create a duplicate; want 1 listing, got %d", len(listings))
		}
		if listings[0].Title != "Staff Software Engineer" {
			t.Errorf("want updated title, got %q", listings[0].Title)
		}

		newFirstSeen, newLastSeen := timestamps(t, pool, defA, listing.URL)
		if !newFirstSeen.Equal(firstSeen) {
			t.Errorf("first_seen should be preserved: was %v, now %v", firstSeen, newFirstSeen)
		}
		if !newLastSeen.After(firstSeen) {
			t.Errorf("last_seen (%v) should advance past first_seen (%v)", newLastSeen, firstSeen)
		}
	})

	t.Run("same url under a different definition_id inserts a distinct row", func(t *testing.T) {
		if err := repo.Save(t.Context(), defB, listing); err != nil {
			t.Fatalf("error saving job listing under second definition: %v", err)
		}

		listings, err := repo.Find(t.Context())
		if err != nil {
			t.Fatalf("error finding job listings: %v", err)
		}
		if len(listings) != 2 {
			t.Fatalf("distinct definition_id + same url should be 2 rows, got %d", len(listings))
		}
	})

	t.Run("FindByDefinition scopes to one definition", func(t *testing.T) {
		got, err := repo.FindByDefinition(t.Context(), defA, "")
		if err != nil {
			t.Fatalf("error finding by definition: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("want 1 listing for defA, got %d", len(got))
		}
		if got[0].URL != listing.URL {
			t.Errorf("want url %q, got %q", listing.URL, got[0].URL)
		}
	})

	t.Run("FindByDefinition filters by keyword over title and description", func(t *testing.T) {
		// defA's listing has title "Staff Software Engineer" and a description
		// mentioning Netflix; both are matched case-insensitively.
		if got, err := repo.FindByDefinition(t.Context(), defA, "STAFF"); err != nil {
			t.Fatalf("error finding by keyword: %v", err)
		} else if len(got) != 1 {
			t.Errorf("title keyword should match; want 1, got %d", len(got))
		}

		if got, err := repo.FindByDefinition(t.Context(), defA, "netflix"); err != nil {
			t.Fatalf("error finding by keyword: %v", err)
		} else if len(got) != 1 {
			t.Errorf("description keyword should match; want 1, got %d", len(got))
		}

		if got, err := repo.FindByDefinition(t.Context(), defA, "nonexistent"); err != nil {
			t.Fatalf("error finding by keyword: %v", err)
		} else if len(got) != 0 {
			t.Errorf("non-matching keyword should return none; got %d", len(got))
		}
	})

	t.Run("FindByDefinition returns empty (non-nil) for an unknown definition", func(t *testing.T) {
		got, err := repo.FindByDefinition(t.Context(), uuid.New(), "")
		if err != nil {
			t.Fatalf("error finding by definition: %v", err)
		}
		if got == nil {
			t.Error("want empty non-nil slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("want 0 listings, got %d", len(got))
		}
	})
}

// createDefinition inserts a minimal crawl definition (job_listing.definition_id
// is an FK to it) and returns its generated ID.
func createDefinition(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	defRepo := postgres.NewCrawlDefinitionRepository(pool)
	def := &crawler.CrawlDefinition{
		Name:       name,
		Kind:       crawler.CrawlKindDiscovery,
		SeedURLs:   []string{"https://example.com"},
		MaxDepth:   1,
		MaxDomains: 1,
	}
	if err := defRepo.Create(t.Context(), def); err != nil {
		t.Fatalf("error creating crawl definition: %v", err)
	}
	return def.ID
}

func timestamps(t *testing.T, pool *pgxpool.Pool, definitionID uuid.UUID, url string) (firstSeen, lastSeen time.Time) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT first_seen, last_seen FROM job_listing WHERE definition_id = $1 AND url = $2`,
		definitionID, url,
	).Scan(&firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("error reading timestamps: %v", err)
	}
	return firstSeen, lastSeen
}
