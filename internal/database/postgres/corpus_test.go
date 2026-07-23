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

// corpusRow is a job_listing row read back directly (the CorpusRepository is
// write-only, so tests query the table themselves to assert the round trip).
type corpusRow struct {
	url             string
	source          string
	sourceID        string
	sourceHash      string
	company         string
	title           string
	description     string
	location        string
	workArrangement string
	companyKey      string
	country         string
	firstSeen       time.Time
	lastSeen        time.Time
	closedAt        *time.Time
	careerPageID    *uuid.UUID
}

// getListing reads the single job_listing row keyed on canonicalURL.
func getListing(t *testing.T, pool *pgxpool.Pool, canonicalURL string) corpusRow {
	t.Helper()
	var r corpusRow
	err := pool.QueryRow(context.Background(), `
		SELECT url, source, source_id, source_hash, company, title, description,
		       location, work_arrangement, company_key, country,
		       first_seen, last_seen, closed_at, career_page_id
		FROM job_listing WHERE canonical_url = $1`, canonicalURL,
	).Scan(
		&r.url, &r.source, &r.sourceID, &r.sourceHash, &r.company, &r.title, &r.description,
		&r.location, &r.workArrangement, &r.companyKey, &r.country,
		&r.firstSeen, &r.lastSeen, &r.closedAt, &r.careerPageID,
	)
	if err != nil {
		t.Fatalf("reading listing %q: %v", canonicalURL, err)
	}
	return r
}

// countListings returns the total number of job_listing rows.
func countListings(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM job_listing`).Scan(&n); err != nil {
		t.Fatalf("counting listings: %v", err)
	}
	return n
}

// TestCorpusUpsertDedupsByCanonicalURL asserts the Corpus is keyed on
// canonical_url (ADR-0034): a first Save inserts and round-trips, re-saving the
// same identity refreshes in place (one row, mutated fields, preserved
// first_seen, advanced last_seen), and a distinct identity is a new row.
func TestCorpusUpsertDedupsByCanonicalURL(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	listing := &crawler.JobListing{
		CanonicalURL:    "https://ex.com/j/1",
		URL:             "https://ex.com/j/1?utm_source=x",
		Source:          crawler.SourceLaneCrawl,
		SourceHash:      "h1",
		Title:           "Senior Software Engineer",
		Description:     "cool stuff",
		Company:         "netflix",
		CompanyKey:      "netflix.com",
		Location:        "Germany",
		Country:         "DE",
		WorkArrangement: crawler.WorkArrangementRemote,
	}

	t.Run("first Save inserts and round-trips", func(t *testing.T) {
		if err := repo.Save(t.Context(), listing); err != nil {
			t.Fatalf("saving listing: %v", err)
		}
		if n := countListings(t, pool); n != 1 {
			t.Fatalf("want 1 row, got %d", n)
		}
		got := getListing(t, pool, listing.CanonicalURL)
		if got.url != listing.URL {
			t.Errorf("url: want %q, got %q", listing.URL, got.url)
		}
		if got.title != listing.Title {
			t.Errorf("title: want %q, got %q", listing.Title, got.title)
		}
		if got.companyKey != listing.CompanyKey {
			t.Errorf("company_key: want %q, got %q", listing.CompanyKey, got.companyKey)
		}
		if got.workArrangement != string(crawler.WorkArrangementRemote) {
			t.Errorf("work_arrangement: want %q, got %q", crawler.WorkArrangementRemote, got.workArrangement)
		}
		if got.country != listing.Country {
			t.Errorf("country: want %q, got %q", listing.Country, got.country)
		}
	})

	t.Run("re-saving same canonical_url upserts in place", func(t *testing.T) {
		before := getListing(t, pool, listing.CanonicalURL)
		if !before.firstSeen.Equal(before.lastSeen) {
			t.Fatalf("on insert first_seen (%v) and last_seen (%v) should match", before.firstSeen, before.lastSeen)
		}

		// now() is the transaction start time; a brief pause guarantees the upsert's
		// last_seen advances past the original first_seen.
		time.Sleep(10 * time.Millisecond)

		updated := *listing
		updated.Title = "Staff Software Engineer"
		if err := repo.Save(t.Context(), &updated); err != nil {
			t.Fatalf("re-saving listing: %v", err)
		}

		if n := countListings(t, pool); n != 1 {
			t.Fatalf("upsert should not create a duplicate; want 1 row, got %d", n)
		}
		after := getListing(t, pool, listing.CanonicalURL)
		if after.title != "Staff Software Engineer" {
			t.Errorf("want updated title, got %q", after.title)
		}
		if !after.firstSeen.Equal(before.firstSeen) {
			t.Errorf("first_seen should be preserved: was %v, now %v", before.firstSeen, after.firstSeen)
		}
		if !after.lastSeen.After(before.firstSeen) {
			t.Errorf("last_seen (%v) should advance past first_seen (%v)", after.lastSeen, before.firstSeen)
		}
	})

	t.Run("a distinct canonical_url is a new row", func(t *testing.T) {
		other := *listing
		other.CanonicalURL = "https://ex.com/j/2"
		other.URL = "https://ex.com/j/2"
		if err := repo.Save(t.Context(), &other); err != nil {
			t.Fatalf("saving distinct listing: %v", err)
		}
		if n := countListings(t, pool); n != 2 {
			t.Fatalf("distinct canonical_url should be 2 rows, got %d", n)
		}
	})
}

// TestCorpusStampsLaneIdentityHash asserts Save persists the Source Lane,
// source_id, and source_hash for both lanes, and writes career_page_id NULL when
// the listing carries no Career Page (uuid.Nil).
func TestCorpusStampsLaneIdentityHash(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	ats := &crawler.JobListing{
		CanonicalURL: "greenhouse:acme:123",
		URL:          "https://boards.greenhouse.io/acme/jobs/123",
		Source:       crawler.SourceLaneATS,
		SourceID:     "123",
		Title:        "Go Engineer",
	}
	if err := repo.Save(t.Context(), ats); err != nil {
		t.Fatalf("saving ats listing: %v", err)
	}
	gotATS := getListing(t, pool, ats.CanonicalURL)
	if gotATS.source != "ats" {
		t.Errorf("source: want %q, got %q", "ats", gotATS.source)
	}
	if gotATS.sourceID != "123" {
		t.Errorf("source_id: want %q, got %q", "123", gotATS.sourceID)
	}
	if gotATS.careerPageID != nil {
		t.Errorf("career_page_id: want NULL for uuid.Nil, got %v", *gotATS.careerPageID)
	}

	crawl := &crawler.JobListing{
		CanonicalURL: "https://ex.com/j/1",
		URL:          "https://ex.com/j/1",
		Source:       crawler.SourceLaneCrawl,
		SourceHash:   "h1",
		Title:        "Backend Engineer",
	}
	if err := repo.Save(t.Context(), crawl); err != nil {
		t.Fatalf("saving crawl listing: %v", err)
	}
	gotCrawl := getListing(t, pool, crawl.CanonicalURL)
	if gotCrawl.source != "crawl" {
		t.Errorf("source: want %q, got %q", "crawl", gotCrawl.source)
	}
	if gotCrawl.sourceHash != "h1" {
		t.Errorf("source_hash: want %q, got %q", "h1", gotCrawl.sourceHash)
	}
	if gotCrawl.sourceID != "" {
		t.Errorf("source_id: want empty on crawl lane, got %q", gotCrawl.sourceID)
	}
}

// TestCorpusReopensOnResave asserts a returning posting reopens in place
// (ADR-0035): a listing manually marked closed has its closed_at cleared on the
// next Save, and first_seen is preserved.
func TestCorpusReopensOnResave(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	listing := &crawler.JobListing{
		CanonicalURL: "https://ex.com/j/1",
		URL:          "https://ex.com/j/1",
		Source:       crawler.SourceLaneCrawl,
		SourceHash:   "h1",
		Title:        "Engineer",
	}
	if err := repo.Save(t.Context(), listing); err != nil {
		t.Fatalf("saving listing: %v", err)
	}
	before := getListing(t, pool, listing.CanonicalURL)

	// Simulate a liveness sweep closing the posting, then it reappearing.
	if _, err := pool.Exec(t.Context(),
		`UPDATE job_listing SET closed_at = now() WHERE canonical_url = $1`, listing.CanonicalURL,
	); err != nil {
		t.Fatalf("marking closed: %v", err)
	}

	if err := repo.Save(t.Context(), listing); err != nil {
		t.Fatalf("re-saving listing: %v", err)
	}
	after := getListing(t, pool, listing.CanonicalURL)
	if after.closedAt != nil {
		t.Errorf("closed_at should be cleared on re-save, got %v", *after.closedAt)
	}
	if !after.firstSeen.Equal(before.firstSeen) {
		t.Errorf("first_seen should be preserved across reopen: was %v, now %v", before.firstSeen, after.firstSeen)
	}
}

// TestCorpusWorkArrangementRoundTrip asserts every WorkArrangement enum value
// survives Save -> the work_arrangement column -> read back unchanged. Distinct
// canonical URLs keep the rows from colliding.
func TestCorpusWorkArrangementRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	arrangements := []crawler.WorkArrangement{
		crawler.WorkArrangementRemote,
		crawler.WorkArrangementOnsite,
		crawler.WorkArrangementHybrid,
		crawler.WorkArrangementUnspecified,
	}
	for _, arr := range arrangements {
		t.Run(string(arr), func(t *testing.T) {
			canonical := "https://example.com/jobs/" + string(arr)
			jl := &crawler.JobListing{
				CanonicalURL:    canonical,
				URL:             canonical,
				Source:          crawler.SourceLaneCrawl,
				WorkArrangement: arr,
			}
			if err := repo.Save(t.Context(), jl); err != nil {
				t.Fatalf("saving %q listing: %v", arr, err)
			}
			got := getListing(t, pool, canonical)
			if got.workArrangement != string(arr) {
				t.Errorf("work_arrangement round-trip: want %q, got %q", arr, got.workArrangement)
			}
		})
	}
}

// TestCorpusCountryRoundTrip asserts the resolved Country (ADR-0029) survives
// Save -> the country column -> read back unchanged, including the empty
// (unresolved) Country, which is stored and read back as "".
func TestCorpusCountryRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	cases := []struct {
		name    string
		country string
	}{
		{"resolved", "DE"},
		{"unresolved is empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonical := "https://example.com/jobs/country/" + tc.name
			jl := &crawler.JobListing{
				CanonicalURL: canonical,
				URL:          canonical,
				Source:       crawler.SourceLaneCrawl,
				Country:      tc.country,
			}
			if err := repo.Save(t.Context(), jl); err != nil {
				t.Fatalf("saving listing: %v", err)
			}
			got := getListing(t, pool, canonical)
			if got.country != tc.country {
				t.Errorf("country round-trip: want %q, got %q", tc.country, got.country)
			}
		})
	}
}
