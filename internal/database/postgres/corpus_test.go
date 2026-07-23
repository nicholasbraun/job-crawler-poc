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
	url                string
	source             string
	sourceID           string
	sourceHash         string
	company            string
	title              string
	description        string
	location           string
	workArrangement    string
	companyKey         string
	country            string
	inconclusiveStreak int
	firstSeen          time.Time
	lastSeen           time.Time
	closedAt           *time.Time
	careerPageID       *uuid.UUID
}

// getListing reads the single job_listing row keyed on canonicalURL.
func getListing(t *testing.T, pool *pgxpool.Pool, canonicalURL string) corpusRow {
	t.Helper()
	var r corpusRow
	err := pool.QueryRow(context.Background(), `
		SELECT url, source, source_id, source_hash, company, title, description,
		       location, work_arrangement, company_key, country, inconclusive_streak,
		       first_seen, last_seen, closed_at, career_page_id
		FROM job_listing WHERE canonical_url = $1`, canonicalURL,
	).Scan(
		&r.url, &r.source, &r.sourceID, &r.sourceHash, &r.company, &r.title, &r.description,
		&r.location, &r.workArrangement, &r.companyKey, &r.country, &r.inconclusiveStreak,
		&r.firstSeen, &r.lastSeen, &r.closedAt, &r.careerPageID,
	)
	if err != nil {
		t.Fatalf("reading listing %q: %v", canonicalURL, err)
	}
	return r
}

// seedCareerPage inserts a company + career_page directly and returns the
// career_page id, giving the liveness tests a real FK target for job_listing.
// key must be unique per call within one test (company.company_key is UNIQUE).
func seedCareerPage(t *testing.T, pool *pgxpool.Pool, key string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var companyID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (company_key) VALUES ($1) RETURNING id`, key,
	).Scan(&companyID); err != nil {
		t.Fatalf("seeding company %q: %v", key, err)
	}
	var pageID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO career_page (company_id, url) VALUES ($1, $2) RETURNING id`,
		companyID, "https://"+key+"/careers",
	).Scan(&pageID); err != nil {
		t.Fatalf("seeding career page for %q: %v", key, err)
	}
	return pageID
}

// dbNow reads the database clock, giving the absence-sweep tests a Cycle watermark
// sourced from the same clock Save stamps last_seen with (now()), avoiding app/DB skew.
func dbNow(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(context.Background(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("reading db clock: %v", err)
	}
	return now
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

	// Simulate a staleness sweep closing the posting (with an accrued streak), then
	// it reappearing.
	if _, err := pool.Exec(t.Context(),
		`UPDATE job_listing SET closed_at = now(), inconclusive_streak = 3 WHERE canonical_url = $1`, listing.CanonicalURL,
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
	if after.inconclusiveStreak != 0 {
		t.Errorf("inconclusive_streak should reset to 0 on re-save (confirmed alive), got %d", after.inconclusiveStreak)
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

// saveATS saves a minimal ATS-lane listing under careerPageID and returns it.
func saveATS(t *testing.T, repo *postgres.CorpusRepository, canonicalURL string, careerPageID uuid.UUID) *crawler.JobListing {
	t.Helper()
	jl := &crawler.JobListing{
		CanonicalURL: canonicalURL,
		URL:          canonicalURL,
		Source:       crawler.SourceLaneATS,
		SourceID:     canonicalURL,
		CareerPageID: careerPageID,
		Title:        "Engineer",
	}
	if err := repo.Save(t.Context(), jl); err != nil {
		t.Fatalf("saving ats listing %q: %v", canonicalURL, err)
	}
	return jl
}

// saveCrawl saves a minimal crawl-lane listing under careerPageID and returns it.
func saveCrawl(t *testing.T, repo *postgres.CorpusRepository, canonicalURL string, careerPageID uuid.UUID) *crawler.JobListing {
	t.Helper()
	jl := &crawler.JobListing{
		CanonicalURL: canonicalURL,
		URL:          canonicalURL,
		Source:       crawler.SourceLaneCrawl,
		SourceHash:   "h-" + canonicalURL,
		CareerPageID: careerPageID,
		Title:        "Engineer",
	}
	if err := repo.Save(t.Context(), jl); err != nil {
		t.Fatalf("saving crawl listing %q: %v", canonicalURL, err)
	}
	return jl
}

// TestCorpusCloseAbsent asserts the ATS absence-sweep (ADR-0035): an incomplete
// fetch closes nothing, a complete fetch closes only the Open ATS listings under the
// swept career_page not re-seen this Cycle (last_seen before the watermark), a
// re-seen listing survives, and a sibling board's listings are never touched.
func TestCorpusCloseAbsent(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	pageA := seedCareerPage(t, pool, "ats-a")
	pageB := seedCareerPage(t, pool, "ats-b")

	l1 := saveATS(t, repo, "ats:a:1", pageA)
	l2 := saveATS(t, repo, "ats:a:2", pageA)
	l3 := saveATS(t, repo, "ats:b:3", pageB)

	// Watermark from the DB clock, strictly after the initial saves.
	time.Sleep(10 * time.Millisecond)
	watermark := dbNow(t, pool)
	time.Sleep(10 * time.Millisecond)

	// L1 is re-seen this Cycle: its last_seen advances past the watermark.
	saveATS(t, repo, l1.CanonicalURL, pageA)

	t.Run("incomplete fetch closes nothing", func(t *testing.T) {
		n, err := repo.CloseAbsent(t.Context(), pageA, watermark, false)
		if err != nil {
			t.Fatalf("CloseAbsent (incomplete): %v", err)
		}
		if n != 0 {
			t.Errorf("incomplete fetch should close nothing, closed %d", n)
		}
		if got := getListing(t, pool, l1.CanonicalURL); got.closedAt != nil {
			t.Errorf("L1 should stay open on incomplete fetch")
		}
		if got := getListing(t, pool, l2.CanonicalURL); got.closedAt != nil {
			t.Errorf("L2 should stay open on incomplete fetch")
		}
	})

	t.Run("complete fetch closes only the absent listing, scoped to the board", func(t *testing.T) {
		n, err := repo.CloseAbsent(t.Context(), pageA, watermark, true)
		if err != nil {
			t.Fatalf("CloseAbsent (complete): %v", err)
		}
		if n != 1 {
			t.Errorf("complete fetch should close exactly L2, closed %d", n)
		}
		if got := getListing(t, pool, l2.CanonicalURL); got.closedAt == nil {
			t.Errorf("L2 (absent, last_seen before watermark) should be closed")
		}
		if got := getListing(t, pool, l1.CanonicalURL); got.closedAt != nil {
			t.Errorf("L1 (re-seen this Cycle) should stay open")
		}
		if got := getListing(t, pool, l3.CanonicalURL); got.closedAt != nil {
			t.Errorf("L3 (different board) should never be touched by a pageA sweep")
		}
	})
}

// TestCorpusApplyCrawlProbe asserts the crawl-lane refetch application (ADR-0035):
// Alive advances last_seen and clears the streak, Inconclusive accrues the streak
// without advancing last_seen until the threshold closes the listing, Dead closes
// immediately, an unprobed listing is never touched (a down collector closes
// nothing), and probing an unknown listing errors.
func TestCorpusApplyCrawlProbe(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	page := seedCareerPage(t, pool, "crawl-p")
	l := saveCrawl(t, repo, "https://ex.com/j/1", page)
	unprobed := saveCrawl(t, repo, "https://ex.com/j/unprobed", page)

	var aliveSeen time.Time

	t.Run("alive stays open, advances last_seen, clears streak", func(t *testing.T) {
		before := getListing(t, pool, l.CanonicalURL)
		time.Sleep(10 * time.Millisecond)

		state, err := repo.ApplyCrawlProbe(t.Context(), l.CanonicalURL, crawler.ProbeAlive, 3)
		if err != nil {
			t.Fatalf("ApplyCrawlProbe alive: %v", err)
		}
		if want := (crawler.LifecycleState{Open: true, InconclusiveStreak: 0}); state != want {
			t.Errorf("state = %+v, want %+v", state, want)
		}
		after := getListing(t, pool, l.CanonicalURL)
		if after.closedAt != nil {
			t.Errorf("alive should keep the listing open")
		}
		if after.inconclusiveStreak != 0 {
			t.Errorf("alive should clear the streak, got %d", after.inconclusiveStreak)
		}
		if !after.lastSeen.After(before.lastSeen) {
			t.Errorf("alive should advance last_seen: was %v, now %v", before.lastSeen, after.lastSeen)
		}
		aliveSeen = after.lastSeen
	})

	t.Run("inconclusive accrues streak without advancing last_seen", func(t *testing.T) {
		for i := 1; i <= 2; i++ {
			state, err := repo.ApplyCrawlProbe(t.Context(), l.CanonicalURL, crawler.ProbeInconclusive, 3)
			if err != nil {
				t.Fatalf("ApplyCrawlProbe inconclusive #%d: %v", i, err)
			}
			if want := (crawler.LifecycleState{Open: true, InconclusiveStreak: i}); state != want {
				t.Errorf("after inconclusive #%d: state = %+v, want %+v", i, state, want)
			}
			after := getListing(t, pool, l.CanonicalURL)
			if after.closedAt != nil {
				t.Errorf("inconclusive #%d should keep the listing open below threshold", i)
			}
			if after.inconclusiveStreak != i {
				t.Errorf("inconclusive #%d streak = %d, want %d", i, after.inconclusiveStreak, i)
			}
			if !after.lastSeen.Equal(aliveSeen) {
				t.Errorf("inconclusive #%d must not advance last_seen: was %v, now %v", i, aliveSeen, after.lastSeen)
			}
		}
	})

	t.Run("third inconclusive reaches the threshold and closes", func(t *testing.T) {
		state, err := repo.ApplyCrawlProbe(t.Context(), l.CanonicalURL, crawler.ProbeInconclusive, 3)
		if err != nil {
			t.Fatalf("ApplyCrawlProbe inconclusive (threshold): %v", err)
		}
		if want := (crawler.LifecycleState{Open: false, InconclusiveStreak: 3}); state != want {
			t.Errorf("state = %+v, want %+v", state, want)
		}
		after := getListing(t, pool, l.CanonicalURL)
		if after.closedAt == nil {
			t.Errorf("staleness backstop should have closed the listing")
		}
		if after.inconclusiveStreak != 3 {
			t.Errorf("streak = %d, want 3 preserved on staleness close", after.inconclusiveStreak)
		}
	})

	t.Run("dead closes a fresh listing immediately with streak 0", func(t *testing.T) {
		fresh := saveCrawl(t, repo, "https://ex.com/j/dead", page)
		state, err := repo.ApplyCrawlProbe(t.Context(), fresh.CanonicalURL, crawler.ProbeDead, 3)
		if err != nil {
			t.Fatalf("ApplyCrawlProbe dead: %v", err)
		}
		if want := (crawler.LifecycleState{Open: false, InconclusiveStreak: 0}); state != want {
			t.Errorf("state = %+v, want %+v", state, want)
		}
		after := getListing(t, pool, fresh.CanonicalURL)
		if after.closedAt == nil {
			t.Errorf("dead should close the listing immediately")
		}
	})

	t.Run("an unprobed listing is never touched (down collector closes nothing)", func(t *testing.T) {
		if got := getListing(t, pool, unprobed.CanonicalURL); got.closedAt != nil {
			t.Errorf("a listing that was never probed must stay open")
		}
	})

	t.Run("probing an unknown listing errors", func(t *testing.T) {
		if _, err := repo.ApplyCrawlProbe(t.Context(), "https://ex.com/j/nope", crawler.ProbeAlive, 3); err == nil {
			t.Errorf("expected an error probing an unknown listing")
		}
	})
}

// TestCorpusListOpen asserts ListOpen returns exactly the Open listings under a
// board with their refetch fields populated, and yields an empty (non-nil) slice
// for a board with none.
func TestCorpusListOpen(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCorpusRepository(pool)

	page := seedCareerPage(t, pool, "list-open")
	emptyPage := seedCareerPage(t, pool, "list-open-empty")

	o1 := saveCrawl(t, repo, "https://ex.com/j/open1", page)
	o2 := saveCrawl(t, repo, "https://ex.com/j/open2", page)
	closed := saveCrawl(t, repo, "https://ex.com/j/closed", page)
	if _, err := pool.Exec(t.Context(),
		`UPDATE job_listing SET closed_at = now() WHERE canonical_url = $1`, closed.CanonicalURL,
	); err != nil {
		t.Fatalf("closing listing: %v", err)
	}

	t.Run("returns only the open listings with refetch fields populated", func(t *testing.T) {
		open, err := repo.ListOpen(t.Context(), page)
		if err != nil {
			t.Fatalf("ListOpen: %v", err)
		}
		byURL := map[string]*crawler.JobListing{}
		for _, jl := range open {
			byURL[jl.CanonicalURL] = jl
		}
		if len(open) != 2 {
			t.Fatalf("want 2 open listings, got %d (%v)", len(open), byURL)
		}
		for _, want := range []*crawler.JobListing{o1, o2} {
			got, ok := byURL[want.CanonicalURL]
			if !ok {
				t.Fatalf("open listing %q missing", want.CanonicalURL)
			}
			if got.URL != want.URL {
				t.Errorf("url: want %q, got %q", want.URL, got.URL)
			}
			if got.SourceHash != want.SourceHash {
				t.Errorf("source_hash: want %q, got %q", want.SourceHash, got.SourceHash)
			}
			if got.Source != crawler.SourceLaneCrawl {
				t.Errorf("source: want crawl, got %q", got.Source)
			}
			if got.CareerPageID != page {
				t.Errorf("career_page_id: want %v, got %v", page, got.CareerPageID)
			}
		}
		if _, ok := byURL[closed.CanonicalURL]; ok {
			t.Errorf("the closed listing must not appear in ListOpen")
		}
	})

	t.Run("a board with no open listings yields an empty non-nil slice", func(t *testing.T) {
		open, err := repo.ListOpen(t.Context(), emptyPage)
		if err != nil {
			t.Fatalf("ListOpen (empty): %v", err)
		}
		if open == nil {
			t.Errorf("ListOpen must never return nil")
		}
		if len(open) != 0 {
			t.Errorf("want 0 open listings, got %d", len(open))
		}
	})
}
