package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

// saveSearchable saves a crawl-lane listing with the text/structured fields the search
// index reads, keeping the search-test seeds terse. career_page_id is left NULL.
func saveSearchable(t *testing.T, repo *postgres.CorpusRepository, canonicalURL, title, description, company, country string, arrangement crawler.WorkArrangement) *crawler.JobListing {
	t.Helper()
	jl := &crawler.JobListing{
		CanonicalURL:    canonicalURL,
		URL:             canonicalURL,
		Source:          crawler.SourceLaneCrawl,
		Title:           title,
		Description:     description,
		Company:         company,
		Country:         country,
		WorkArrangement: arrangement,
	}
	if err := repo.Save(t.Context(), jl); err != nil {
		t.Fatalf("saving searchable listing %q: %v", canonicalURL, err)
	}
	return jl
}

// setLastSeen forces a row's last_seen so recency/paging order is deterministic, rather
// than relying on the microsecond gaps between Save's now() stamps.
func setLastSeen(t *testing.T, pool *pgxpool.Pool, canonicalURL string, ts time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE job_listing SET last_seen = $2 WHERE canonical_url = $1`, canonicalURL, ts,
	); err != nil {
		t.Fatalf("setting last_seen for %q: %v", canonicalURL, err)
	}
}

func setFirstSeen(t *testing.T, pool *pgxpool.Pool, canonicalURL string, ts time.Time) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE job_listing SET first_seen = $2 WHERE canonical_url = $1`, canonicalURL, ts,
	); err != nil {
		t.Fatalf("setting first_seen for %q: %v", canonicalURL, err)
	}
}

// closeSearchListing marks a row closed so the open-only default can be exercised.
func closeSearchListing(t *testing.T, pool *pgxpool.Pool, canonicalURL string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE job_listing SET closed_at = now() WHERE canonical_url = $1`, canonicalURL,
	); err != nil {
		t.Fatalf("closing %q: %v", canonicalURL, err)
	}
}

// searchURLOrder returns the canonical URLs of a result set in order, for order assertions.
func searchURLOrder(listings []*crawler.CorpusListing) []string {
	out := make([]string, len(listings))
	for i, l := range listings {
		out[i] = l.CanonicalURL
	}
	return out
}

// searchURLSet returns the canonical URLs of a result set as a membership set, for
// order-independent assertions.
func searchURLSet(listings []*crawler.CorpusListing) map[string]bool {
	m := make(map[string]bool, len(listings))
	for _, l := range listings {
		m[l.CanonicalURL] = true
	}
	return m
}

// TestSearchListingsRanking asserts relevance ranking: a title-weighted match outranks a
// description-weighted one, and equally-relevant matches fall back to a last_seen recency
// tiebreak (newest first).
func TestSearchListingsRanking(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	t.Run("title weight outranks description weight", func(t *testing.T) {
		inTitle := saveSearchable(t, repo, "https://ex.com/rank/title", "Golang Developer", "backend work", "acme", "DE", crawler.WorkArrangementRemote)
		saveSearchable(t, repo, "https://ex.com/rank/desc", "Backend Role", "we use Golang heavily", "acme", "DE", crawler.WorkArrangementRemote)

		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Keywords: []string{"Golang"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 matches, got %d (%v)", len(got), searchURLOrder(got))
		}
		if got[0].CanonicalURL != inTitle.CanonicalURL {
			t.Errorf("title match should rank first, got order %v", searchURLOrder(got))
		}
	})

	t.Run("equal relevance breaks on recency", func(t *testing.T) {
		older := saveSearchable(t, repo, "https://ex.com/rank/rec-old", "Zephyr Engineer", "", "acme", "DE", crawler.WorkArrangementRemote)
		newer := saveSearchable(t, repo, "https://ex.com/rank/rec-new", "Zephyr Engineer", "", "acme", "DE", crawler.WorkArrangementRemote)
		base := dbNow(t, pool)
		setLastSeen(t, pool, older.CanonicalURL, base)
		setLastSeen(t, pool, newer.CanonicalURL, base.Add(time.Hour))

		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Keywords: []string{"Zephyr"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 matches, got %d (%v)", len(got), searchURLOrder(got))
		}
		if got[0].CanonicalURL != newer.CanonicalURL {
			t.Errorf("newer listing should win the recency tiebreak, got order %v", searchURLOrder(got))
		}
	})
}

// TestSearchListingsFuzzy asserts the pg_trgm fuzzy tail: a misspelled keyword that never
// tokenizes to the indexed term still matches via word_similarity, while an unrelated term
// matches nothing (which also pins fuzzyMatchThreshold).
func TestSearchListingsFuzzy(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	target := saveSearchable(t, repo, "https://ex.com/fuzzy/1", "Senior Software Engineer", "", "acme", "DE", crawler.WorkArrangementRemote)

	t.Run("misspelled keyword matches fuzzily", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Keywords: []string{"enginer"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if !searchURLSet(got)[target.CanonicalURL] {
			t.Errorf("misspelled \"enginer\" should fuzzily match %q; got %v", target.CanonicalURL, searchURLOrder(got))
		}
	})

	t.Run("unrelated keyword matches nothing", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Keywords: []string{"astrophysicist"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if got == nil {
			t.Errorf("SearchListings must never return nil")
		}
		if len(got) != 0 {
			t.Errorf("an unrelated keyword should match nothing, got %v", searchURLOrder(got))
		}
	})
}

// TestSearchListingsFilterComposition asserts the structured filters (country,
// work-arrangement, open-closed) compose with each other and with the keyword match, and
// that the default restricts to Open listings.
func TestSearchListingsFilterComposition(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	deRemote := saveSearchable(t, repo, "https://ex.com/comp/de-remote", "Engineer", "", "acme", "DE", crawler.WorkArrangementRemote)
	deOnsite := saveSearchable(t, repo, "https://ex.com/comp/de-onsite", "Engineer", "", "acme", "DE", crawler.WorkArrangementOnsite)
	usOnsite := saveSearchable(t, repo, "https://ex.com/comp/us-onsite", "Engineer", "", "acme", "US", crawler.WorkArrangementOnsite)
	deClosed := saveSearchable(t, repo, "https://ex.com/comp/de-closed", "Engineer", "", "acme", "DE", crawler.WorkArrangementRemote)
	closeSearchListing(t, pool, deClosed.CanonicalURL)

	t.Run("country filter returns only that country, open only", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Countries: []string{"DE"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		set := searchURLSet(got)
		if !set[deRemote.CanonicalURL] || !set[deOnsite.CanonicalURL] {
			t.Errorf("DE filter should include both open DE listings, got %v", searchURLOrder(got))
		}
		if set[usOnsite.CanonicalURL] {
			t.Errorf("DE filter must exclude the US listing")
		}
		if set[deClosed.CanonicalURL] {
			t.Errorf("open-only default must exclude the closed DE listing")
		}
	})

	t.Run("country filter is case-insensitive", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Countries: []string{"de"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		set := searchURLSet(got)
		if !set[deRemote.CanonicalURL] || !set[deOnsite.CanonicalURL] {
			t.Errorf("lowercase \"de\" should match the DE listings, got %v", searchURLOrder(got))
		}
	})

	t.Run("work-arrangement filter returns only that arrangement", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{WorkArrangements: []crawler.WorkArrangement{crawler.WorkArrangementRemote}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		set := searchURLSet(got)
		if !set[deRemote.CanonicalURL] {
			t.Errorf("remote filter should include the open remote listing, got %v", searchURLOrder(got))
		}
		if set[deOnsite.CanonicalURL] || set[usOnsite.CanonicalURL] {
			t.Errorf("remote filter must exclude onsite listings")
		}
	})

	t.Run("IncludeClosed toggles the closed listing", func(t *testing.T) {
		excl, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Countries: []string{"DE"}})
		if err != nil {
			t.Fatalf("SearchListings (default): %v", err)
		}
		if searchURLSet(excl)[deClosed.CanonicalURL] {
			t.Errorf("closed listing must be excluded by default")
		}
		incl, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Countries: []string{"DE"}, IncludeClosed: true})
		if err != nil {
			t.Fatalf("SearchListings (IncludeClosed): %v", err)
		}
		if !searchURLSet(incl)[deClosed.CanonicalURL] {
			t.Errorf("IncludeClosed should surface the closed listing, got %v", searchURLOrder(incl))
		}
	})

	t.Run("keyword and structured filters compose", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{
			Keywords:         []string{"Engineer"},
			Countries:        []string{"DE"},
			WorkArrangements: []crawler.WorkArrangement{crawler.WorkArrangementRemote},
		})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if len(got) != 1 || got[0].CanonicalURL != deRemote.CanonicalURL {
			t.Errorf("keyword + DE + remote should return only the DE remote listing, got %v", searchURLOrder(got))
		}
	})
}

// TestSearchListingsEmptyAndBrowse asserts the no-match result is a non-nil empty slice and
// that the zero-value query browses every Open listing newest-first (no text predicate).
func TestSearchListingsEmptyAndBrowse(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	t.Run("no match yields a non-nil empty slice", func(t *testing.T) {
		saveSearchable(t, repo, "https://ex.com/empty/1", "Designer", "", "acme", "DE", crawler.WorkArrangementRemote)
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Keywords: []string{"zzzznotathing"}})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if got == nil {
			t.Fatalf("SearchListings must never return nil")
		}
		if len(got) != 0 {
			t.Errorf("want 0 matches, got %v", searchURLOrder(got))
		}
	})

	t.Run("zero-value query browses open listings newest-first", func(t *testing.T) {
		pool := newTestPool(t)
		repo := postgres.NewCorpusRepository(pool)

		a := saveSearchable(t, repo, "https://ex.com/browse/a", "Role A", "", "acme", "DE", crawler.WorkArrangementRemote)
		b := saveSearchable(t, repo, "https://ex.com/browse/b", "Role B", "", "acme", "DE", crawler.WorkArrangementRemote)
		c := saveSearchable(t, repo, "https://ex.com/browse/c", "Role C", "", "acme", "DE", crawler.WorkArrangementRemote)
		gone := saveSearchable(t, repo, "https://ex.com/browse/closed", "Role Closed", "", "acme", "DE", crawler.WorkArrangementRemote)
		closeSearchListing(t, pool, gone.CanonicalURL)

		base := dbNow(t, pool)
		setLastSeen(t, pool, a.CanonicalURL, base.Add(1*time.Minute))
		setLastSeen(t, pool, b.CanonicalURL, base.Add(2*time.Minute))
		setLastSeen(t, pool, c.CanonicalURL, base.Add(3*time.Minute))

		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		want := []string{c.CanonicalURL, b.CanonicalURL, a.CanonicalURL}
		order := searchURLOrder(got)
		if len(order) != len(want) {
			t.Fatalf("want %v open listings newest-first, got %v", want, order)
		}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("browse order: want %v, got %v", want, order)
			}
		}
	})
}

// TestSearchListingsSortFound asserts SortFound orders strictly by first_seen
// (newly-discovered postings, the live collection feed), distinct from SortRecent's
// last_seen order, and stays open-only by default.
func TestSearchListingsSortFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	a := saveSearchable(t, repo, "https://ex.com/found/a", "Role A", "", "acme", "DE", crawler.WorkArrangementRemote)
	b := saveSearchable(t, repo, "https://ex.com/found/b", "Role B", "", "acme", "DE", crawler.WorkArrangementRemote)
	c := saveSearchable(t, repo, "https://ex.com/found/c", "Role C", "", "acme", "DE", crawler.WorkArrangementRemote)
	gone := saveSearchable(t, repo, "https://ex.com/found/closed", "Role Closed", "", "acme", "DE", crawler.WorkArrangementRemote)
	closeSearchListing(t, pool, gone.CanonicalURL)

	base := dbNow(t, pool)
	// first_seen ascending a<b<c => SortFound order c,b,a.
	setFirstSeen(t, pool, a.CanonicalURL, base.Add(1*time.Minute))
	setFirstSeen(t, pool, b.CanonicalURL, base.Add(2*time.Minute))
	setFirstSeen(t, pool, c.CanonicalURL, base.Add(3*time.Minute))
	// last_seen reversed a>b>c => SortRecent would order a,b,c, so the two sorts diverge.
	setLastSeen(t, pool, a.CanonicalURL, base.Add(30*time.Minute))
	setLastSeen(t, pool, b.CanonicalURL, base.Add(20*time.Minute))
	setLastSeen(t, pool, c.CanonicalURL, base.Add(10*time.Minute))

	found, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Sort: crawler.SortFound})
	if err != nil {
		t.Fatalf("SearchListings SortFound: %v", err)
	}
	if got, want := searchURLOrder(found), []string{c.CanonicalURL, b.CanonicalURL, a.CanonicalURL}; !equalOrder(got, want) {
		t.Fatalf("SortFound order: want %v (first_seen desc), got %v", want, got)
	}

	recent, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Sort: crawler.SortRecent})
	if err != nil {
		t.Fatalf("SearchListings SortRecent: %v", err)
	}
	if got, want := searchURLOrder(recent), []string{a.CanonicalURL, b.CanonicalURL, c.CanonicalURL}; !equalOrder(got, want) {
		t.Fatalf("SortRecent order: want %v (last_seen desc), got %v", want, got)
	}
}

func equalOrder(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// TestListingCounts asserts ListingCounts reports distinct open/total rows (the
// true corpus size), counting a closed listing in total but not open.
func TestListingCounts(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	if open, total, err := repo.ListingCounts(t.Context()); err != nil || open != 0 || total != 0 {
		t.Fatalf("empty corpus: got open=%d total=%d err=%v, want 0/0/nil", open, total, err)
	}

	saveSearchable(t, repo, "https://ex.com/count/1", "Role 1", "", "acme", "DE", crawler.WorkArrangementRemote)
	saveSearchable(t, repo, "https://ex.com/count/2", "Role 2", "", "acme", "DE", crawler.WorkArrangementRemote)
	gone := saveSearchable(t, repo, "https://ex.com/count/3", "Role 3", "", "acme", "DE", crawler.WorkArrangementRemote)
	closeSearchListing(t, pool, gone.CanonicalURL)
	// Re-saving an existing listing must NOT change the counts (upsert, not a new row) —
	// this is exactly what the run's ListingsFound counter over-counts.
	saveSearchable(t, repo, "https://ex.com/count/1", "Role 1 refreshed", "", "acme", "DE", crawler.WorkArrangementRemote)

	open, total, err := repo.ListingCounts(t.Context())
	if err != nil {
		t.Fatalf("ListingCounts: %v", err)
	}
	if open != 2 || total != 3 {
		t.Errorf("counts: got open=%d total=%d, want open=2 total=3", open, total)
	}
}

// TestSearchListingsReturnsDepartment asserts the ATS-lane department attribute
// (ADR-0022, migration 0021) is persisted at Save and returned on the projected
// CorpusListing, so a SavedSearch panel can render it.
func TestSearchListingsReturnsDepartment(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	jl := &crawler.JobListing{
		CanonicalURL:    "https://ex.com/dept/1",
		URL:             "https://ex.com/dept/1",
		Source:          crawler.SourceLaneATS,
		Title:           "Platform Engineer",
		Company:         "acme",
		Country:         "DE",
		Department:      "Platform",
		WorkArrangement: crawler.WorkArrangementRemote,
	}
	if err := repo.Save(t.Context(), jl); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Keywords: []string{"Platform"}})
	if err != nil {
		t.Fatalf("SearchListings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 match, got %d (%v)", len(got), searchURLOrder(got))
	}
	if got[0].Department != "Platform" {
		t.Errorf("department: got %q, want %q", got[0].Department, "Platform")
	}
}

// TestSearchListingsPaging asserts Limit/Offset slice the ordered result set, and that a
// negative Offset is clamped to zero.
func TestSearchListingsPaging(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	base := dbNow(t, pool)
	order := []string{}
	// Seed newest-last, then order is the reverse (newest-first).
	for i := 0; i < 5; i++ {
		url := "https://ex.com/page/" + string(rune('a'+i))
		saveSearchable(t, repo, url, "Pager", "", "acme", "DE", crawler.WorkArrangementRemote)
		setLastSeen(t, pool, url, base.Add(time.Duration(i)*time.Minute))
		order = append([]string{url}, order...) // prepend => newest-first
	}

	t.Run("first page", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Limit: 2, Offset: 0})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if o := searchURLOrder(got); len(o) != 2 || o[0] != order[0] || o[1] != order[1] {
			t.Errorf("first page: want %v, got %v", order[:2], o)
		}
	})

	t.Run("second page", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Limit: 2, Offset: 2})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if o := searchURLOrder(got); len(o) != 2 || o[0] != order[2] || o[1] != order[3] {
			t.Errorf("second page: want %v, got %v", order[2:4], o)
		}
	})

	t.Run("negative offset clamps to zero", func(t *testing.T) {
		got, err := repo.SearchListings(t.Context(), crawler.ListingQuery{Limit: 1, Offset: -5})
		if err != nil {
			t.Fatalf("SearchListings: %v", err)
		}
		if o := searchURLOrder(got); len(o) != 1 || o[0] != order[0] {
			t.Errorf("negative offset should start at the first row: want %v, got %v", order[:1], o)
		}
	})
}
