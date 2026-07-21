package postgres_test

import (
	"context"
	"strconv"
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
		URL:             "https://netflix.com/jobs/123",
		Title:           "Senior Software Engineer",
		Description:     "At Netflix you will be doing cool stuff",
		Company:         "netflix",
		CompanyKey:      "netflix.com",
		Location:        "Germany",
		Country:         "DE",
		WorkArrangement: crawler.WorkArrangementRemote,
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
		if got.Description != listing.Description {
			t.Errorf("want description %q, got %q", listing.Description, got.Description)
		}
		if got.Location != listing.Location {
			t.Errorf("want location %q, got %q", listing.Location, got.Location)
		}
		if got.CompanyKey != listing.CompanyKey {
			t.Errorf("want company_key %q, got %q", listing.CompanyKey, got.CompanyKey)
		}
		if got.WorkArrangement != crawler.WorkArrangementRemote {
			t.Errorf("want work_arrangement %q, got %q", crawler.WorkArrangementRemote, got.WorkArrangement)
		}
		if got.Country != listing.Country {
			t.Errorf("want country %q, got %q", listing.Country, got.Country)
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
		if got[0].CompanyKey != listing.CompanyKey {
			t.Errorf("company_key should round-trip via FindByDefinition: want %q, got %q", listing.CompanyKey, got[0].CompanyKey)
		}
		if got[0].WorkArrangement != crawler.WorkArrangementRemote {
			t.Errorf("work_arrangement should round-trip via FindByDefinition: want %q, got %q", crawler.WorkArrangementRemote, got[0].WorkArrangement)
		}
		if got[0].Country != listing.Country {
			t.Errorf("country should round-trip via FindByDefinition: want %q, got %q", listing.Country, got[0].Country)
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

func TestJobListingWorkArrangementRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewJobListingRepository(pool)
	def := createDefinition(t, pool, "work arrangement definition")

	// Every enum value must survive Save -> the work_arrangement column -> Scan ->
	// NormalizeWorkArrangement unchanged. Distinct URLs keep the rows from colliding
	// on (definition_id, url).
	arrangements := []crawler.WorkArrangement{
		crawler.WorkArrangementRemote,
		crawler.WorkArrangementOnsite,
		crawler.WorkArrangementHybrid,
		crawler.WorkArrangementUnspecified,
	}
	for _, arr := range arrangements {
		t.Run(string(arr), func(t *testing.T) {
			url := "https://example.com/jobs/" + string(arr)
			if err := repo.Save(t.Context(), def, &crawler.JobListing{URL: url, WorkArrangement: arr}); err != nil {
				t.Fatalf("error saving %q listing: %v", arr, err)
			}

			got, err := repo.FindByDefinition(t.Context(), def, "")
			if err != nil {
				t.Fatalf("error finding by definition: %v", err)
			}
			var found *crawler.JobListing
			for _, l := range got {
				if l.URL == url {
					found = l
					break
				}
			}
			if found == nil {
				t.Fatalf("saved %q listing %q not found", arr, url)
			}
			if found.WorkArrangement != arr {
				t.Errorf("work_arrangement round-trip: want %q, got %q", arr, found.WorkArrangement)
			}
		})
	}
}

// TestJobListingCountryRoundTrip asserts the resolved Country (ADR-0029) survives
// Save -> the country column -> Find/FindByDefinition unchanged, including the
// empty (unresolved) Country, which is stored and read back as "".
func TestJobListingCountryRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewJobListingRepository(pool)
	def := createDefinition(t, pool, "country round-trip definition")

	cases := []struct {
		name    string
		country string
	}{
		{"resolved", "DE"},
		{"unresolved is empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "https://example.com/jobs/country/" + tc.name
			if err := repo.Save(t.Context(), def, &crawler.JobListing{URL: url, Country: tc.country}); err != nil {
				t.Fatalf("error saving listing: %v", err)
			}

			got, err := repo.FindByDefinition(t.Context(), def, "")
			if err != nil {
				t.Fatalf("error finding by definition: %v", err)
			}
			var found *crawler.JobListing
			for _, l := range got {
				if l.URL == url {
					found = l
					break
				}
			}
			if found == nil {
				t.Fatalf("saved listing %q not found", url)
			}
			if found.Country != tc.country {
				t.Errorf("country round-trip: want %q, got %q", tc.country, found.Country)
			}
		})
	}
}

func TestFindByDefinitionEscapesLikeMetacharacters(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewJobListingRepository(pool)
	def := createDefinition(t, pool, "escape definition")

	// literal encodes the metacharacter as a real character; wildcard is the
	// row that a naive (unescaped) LIKE would spuriously match via that same
	// metacharacter acting as a wildcard.
	tests := []struct {
		name     string
		keyword  string
		literal  string // title containing the metacharacter literally
		wildcard string // title the metacharacter would wrongly match unescaped
	}{
		{name: "percent is literal not wildcard", keyword: "50% remote", literal: "50% remote role", wildcard: "50 something remote role"},
		{name: "underscore is literal not any-char", keyword: "C_C title", literal: "C_C title", wildcard: "CXC title"},
	}

	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Distinct URLs keep the two rows from colliding on (definition_id, url).
			literalURL := "https://example.com/escape/literal/" + strconv.Itoa(i)
			wildcardURL := "https://example.com/escape/wildcard/" + strconv.Itoa(i)

			if err := repo.Save(t.Context(), def, &crawler.JobListing{URL: literalURL, Title: tc.literal}); err != nil {
				t.Fatalf("error saving literal listing: %v", err)
			}
			if err := repo.Save(t.Context(), def, &crawler.JobListing{URL: wildcardURL, Title: tc.wildcard}); err != nil {
				t.Fatalf("error saving wildcard listing: %v", err)
			}

			got, err := repo.FindByDefinition(t.Context(), def, tc.keyword)
			if err != nil {
				t.Fatalf("error finding by keyword: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("want exactly 1 literal match for %q, got %d", tc.keyword, len(got))
			}
			if got[0].URL != literalURL {
				t.Errorf("want literal match %q, got %q", literalURL, got[0].URL)
			}
		})
	}
}

// createDefinition inserts a minimal crawl definition (job_listing.definition_id
// is an FK to it) and returns its generated ID. It is keyword-kind, not
// discovery, so repeated calls within one test do not trip the singleton
// discovery index (migration 0010); kind is immaterial to the listing/run tests
// that use this helper.
func createDefinition(t *testing.T, pool *pgxpool.Pool, name string) uuid.UUID {
	t.Helper()
	defRepo := postgres.NewCrawlDefinitionRepository(pool)
	def := &crawler.CrawlDefinition{
		Name:     name,
		Kind:     crawler.CrawlKindKeyword,
		Keywords: []string{"go"},
		MaxDepth: 1,
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
