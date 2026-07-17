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

func TestCareerPageRepository(t *testing.T) {
	pool := newTestPool(t)
	companyRepo := postgres.NewCompanyRepository(pool)
	repo := postgres.NewCareerPageRepository(pool)

	// career_page.company_id is an FK to company, so a company must exist first.
	acme := &crawler.Company{CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Acme"}
	if err := companyRepo.Upsert(t.Context(), acme); err != nil {
		t.Fatalf("error seeding company: %v", err)
	}
	globex := &crawler.Company{CompanyKey: "greenhouse:globex", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Globex"}
	if err := companyRepo.Upsert(t.Context(), globex); err != nil {
		t.Fatalf("error seeding second company: %v", err)
	}

	const acmeURL = "https://boards.greenhouse.io/acme/jobs/1"

	t.Run("Upsert inserts a career page", func(t *testing.T) {
		page := &crawler.CareerPage{
			CompanyID:        acme.ID,
			URL:              acmeURL,
			PolitenessDomain: "boards.greenhouse.io",
		}
		if err := repo.Upsert(t.Context(), page); err != nil {
			t.Fatalf("error upserting career page: %v", err)
		}
		if countCareerPages(t, pool) != 1 {
			t.Errorf("want 1 career page, got %d", countCareerPages(t, pool))
		}
	})

	t.Run("re-upserting same (company_id, url) updates in place, preserving first_seen", func(t *testing.T) {
		firstSeen, lastSeen := careerPageTimestamps(t, pool, acme.ID, acmeURL)
		if !firstSeen.Equal(lastSeen) {
			t.Fatalf("on insert first_seen (%v) and last_seen (%v) should match", firstSeen, lastSeen)
		}

		time.Sleep(10 * time.Millisecond)

		page := &crawler.CareerPage{
			CompanyID:        acme.ID,
			URL:              acmeURL,
			PolitenessDomain: "boards.greenhouse.io",
		}
		if err := repo.Upsert(t.Context(), page); err != nil {
			t.Fatalf("error re-upserting career page: %v", err)
		}
		if countCareerPages(t, pool) != 1 {
			t.Errorf("upsert should not duplicate; want 1 career page, got %d", countCareerPages(t, pool))
		}

		newFirstSeen, newLastSeen := careerPageTimestamps(t, pool, acme.ID, acmeURL)
		if !newFirstSeen.Equal(firstSeen) {
			t.Errorf("first_seen should be preserved: was %v, now %v", firstSeen, newFirstSeen)
		}
		if !newLastSeen.After(firstSeen) {
			t.Errorf("last_seen (%v) should advance past first_seen (%v)", newLastSeen, firstSeen)
		}
	})

	t.Run("same host, different tenants share politeness_domain but are distinct rows", func(t *testing.T) {
		page := &crawler.CareerPage{
			CompanyID:        globex.ID,
			URL:              "https://boards.greenhouse.io/globex/jobs/2",
			PolitenessDomain: "boards.greenhouse.io",
		}
		if err := repo.Upsert(t.Context(), page); err != nil {
			t.Fatalf("error upserting second tenant career page: %v", err)
		}
		if countCareerPages(t, pool) != 2 {
			t.Fatalf("distinct company_id + url should be 2 rows, got %d", countCareerPages(t, pool))
		}

		domains := politenessDomains(t, pool)
		if len(domains) != 1 || domains[0] != "boards.greenhouse.io" {
			t.Errorf("both career pages should share politeness_domain boards.greenhouse.io, got %v", domains)
		}
	})

	// Runs after the upserts above, so the Catalog holds both career pages.
	t.Run("ListSeeds pairs each url with its owning company key", func(t *testing.T) {
		seeds, err := repo.ListSeeds(t.Context())
		if err != nil {
			t.Fatalf("error listing career page seeds: %v", err)
		}
		want := map[string]string{
			acmeURL: "greenhouse:acme",
			"https://boards.greenhouse.io/globex/jobs/2": "greenhouse:globex",
		}
		if len(seeds) != len(want) {
			t.Fatalf("want %d seeds, got %d: %v", len(want), len(seeds), seeds)
		}
		for _, s := range seeds {
			wantKey, ok := want[s.URL]
			if !ok {
				t.Errorf("unexpected url in catalog: %q", s.URL)
				continue
			}
			if s.CompanyKey != wantKey {
				t.Errorf("seed %q CompanyKey: want %q, got %q", s.URL, wantKey, s.CompanyKey)
			}
		}
	})

	// Runs after the upserts above, so the Catalog holds both career pages.
	t.Run("List returns full entities including company_id", func(t *testing.T) {
		pages, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("error listing career pages: %v", err)
		}
		if len(pages) != 2 {
			t.Fatalf("want 2 career pages, got %d", len(pages))
		}

		byURL := map[string]*crawler.CareerPage{}
		for _, p := range pages {
			byURL[p.URL] = p
		}
		if acmePage := byURL[acmeURL]; acmePage == nil {
			t.Fatalf("%q missing from list", acmeURL)
		} else if acmePage.CompanyID != acme.ID {
			t.Errorf("acme page company_id: want %v, got %v", acme.ID, acmePage.CompanyID)
		}
		if globexPage := byURL["https://boards.greenhouse.io/globex/jobs/2"]; globexPage == nil {
			t.Error("globex page missing from list")
		} else if globexPage.PolitenessDomain != "boards.greenhouse.io" {
			t.Errorf("globex politeness_domain: want boards.greenhouse.io, got %q", globexPage.PolitenessDomain)
		}
	})
}

func TestCareerPageRepositoryListSeedsEmpty(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCareerPageRepository(pool)

	seeds, err := repo.ListSeeds(t.Context())
	if err != nil {
		t.Fatalf("error listing career page seeds: %v", err)
	}
	if seeds == nil {
		t.Fatal("ListSeeds must return a non-nil slice, got nil")
	}
	if len(seeds) != 0 {
		t.Errorf("empty catalog should yield no seeds, got %v", seeds)
	}
}

func TestCareerPageRepositoryFirstSeenByDay(t *testing.T) {
	pool := newTestPool(t)
	companyRepo := postgres.NewCompanyRepository(pool)
	repo := postgres.NewCareerPageRepository(pool)

	acme := &crawler.Company{CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Acme"}
	if err := companyRepo.Upsert(t.Context(), acme); err != nil {
		t.Fatalf("error seeding company: %v", err)
	}

	// Two pages first-seen on 2026-01-10 (one near each end of the UTC day, so a
	// non-UTC session bucketing would split them), a gap day 2026-01-11 with
	// nothing, and one page on 2026-01-12. Upsert always stamps first_seen = now(),
	// so seed the timestamps directly to control the buckets.
	day10 := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	day12 := time.Date(2026, 1, 12, 0, 0, 0, 0, time.UTC)
	seedCareerPageFirstSeen(t, pool, acme.ID, "https://boards.greenhouse.io/acme/jobs/1", time.Date(2026, 1, 10, 8, 0, 0, 0, time.UTC))
	seedCareerPageFirstSeen(t, pool, acme.ID, "https://boards.greenhouse.io/acme/jobs/2", time.Date(2026, 1, 10, 23, 59, 0, 0, time.UTC))
	seedCareerPageFirstSeen(t, pool, acme.ID, "https://boards.greenhouse.io/acme/jobs/3", time.Date(2026, 1, 12, 6, 0, 0, 0, time.UTC))

	counts, err := repo.FirstSeenByDay(t.Context())
	if err != nil {
		t.Fatalf("error counting first-seen by day: %v", err)
	}

	want := []crawler.DayCount{{Day: day10, Count: 2}, {Day: day12, Count: 1}}
	if len(counts) != len(want) {
		t.Fatalf("want %d day buckets (empty gap day omitted), got %d: %+v", len(want), len(counts), counts)
	}
	for i, w := range want {
		if !counts[i].Day.Equal(w.Day) {
			t.Errorf("bucket %d day: want %s, got %s", i, w.Day, counts[i].Day.UTC())
		}
		if counts[i].Count != w.Count {
			t.Errorf("bucket %d count for %s: want %d, got %d", i, w.Day.Format("2006-01-02"), w.Count, counts[i].Count)
		}
	}
}

func TestCareerPageRepositoryFirstSeenByDayEmpty(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCareerPageRepository(pool)

	counts, err := repo.FirstSeenByDay(t.Context())
	if err != nil {
		t.Fatalf("error counting first-seen by day: %v", err)
	}
	if counts == nil {
		t.Fatal("FirstSeenByDay must return a non-nil slice, got nil")
	}
	if len(counts) != 0 {
		t.Errorf("empty catalog should yield no day buckets, got %+v", counts)
	}
}

// seedCareerPageFirstSeen inserts a career_page row with an explicit first_seen,
// which the repository's Upsert cannot set (it always stamps now()).
func seedCareerPageFirstSeen(t *testing.T, pool *pgxpool.Pool, companyID uuid.UUID, url string, firstSeen time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO career_page (company_id, url, politeness_domain, first_seen, last_seen)
		 VALUES ($1, $2, $3, $4, $4)`,
		companyID, url, "boards.greenhouse.io", firstSeen,
	)
	if err != nil {
		t.Fatalf("error seeding career page with first_seen %s: %v", firstSeen, err)
	}
}

func TestCareerPageRepositoryDelete(t *testing.T) {
	pool := newTestPool(t)
	companyRepo := postgres.NewCompanyRepository(pool)
	repo := postgres.NewCareerPageRepository(pool)

	// career_page.company_id is an FK to company, so a company must exist first.
	acme := &crawler.Company{CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Acme"}
	if err := companyRepo.Upsert(t.Context(), acme); err != nil {
		t.Fatalf("error seeding company: %v", err)
	}

	const url = "https://boards.greenhouse.io/acme/jobs/1"
	if err := repo.Upsert(t.Context(), &crawler.CareerPage{
		CompanyID:        acme.ID,
		URL:              url,
		PolitenessDomain: "boards.greenhouse.io",
	}); err != nil {
		t.Fatalf("error seeding career page: %v", err)
	}

	// Upsert does not write back an id, so recover the entity from List — the
	// same path the Catalog Doctor uses to obtain ids.
	id := careerPageIDByURL(t, repo, url)

	t.Run("deletes an existing career page by id", func(t *testing.T) {
		if countCareerPages(t, pool) != 1 {
			t.Fatalf("want 1 career page before delete, got %d", countCareerPages(t, pool))
		}
		if err := repo.Delete(t.Context(), id); err != nil {
			t.Fatalf("error deleting career page: %v", err)
		}
		if countCareerPages(t, pool) != 0 {
			t.Errorf("want 0 career pages after delete, got %d", countCareerPages(t, pool))
		}
	})

	t.Run("deleting the same id again is an idempotent no-op", func(t *testing.T) {
		if err := repo.Delete(t.Context(), id); err != nil {
			t.Fatalf("re-deleting should be a no-op, got error: %v", err)
		}
		if countCareerPages(t, pool) != 0 {
			t.Errorf("want 0 career pages, got %d", countCareerPages(t, pool))
		}
	})

	t.Run("deleting an unknown id is a no-op", func(t *testing.T) {
		if err := repo.Delete(t.Context(), uuid.New()); err != nil {
			t.Fatalf("deleting an unknown id should be a no-op, got error: %v", err)
		}
		if countCareerPages(t, pool) != 0 {
			t.Errorf("want 0 career pages, got %d", countCareerPages(t, pool))
		}
	})
}

func TestCareerPageRepositoryReattribute(t *testing.T) {
	pool := newTestPool(t)
	companyRepo := postgres.NewCompanyRepository(pool)
	repo := postgres.NewCareerPageRepository(pool)

	acme := &crawler.Company{CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Acme"}
	if err := companyRepo.Upsert(t.Context(), acme); err != nil {
		t.Fatalf("error seeding first company: %v", err)
	}
	globex := &crawler.Company{CompanyKey: "greenhouse:globex", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Globex"}
	if err := companyRepo.Upsert(t.Context(), globex); err != nil {
		t.Fatalf("error seeding second company: %v", err)
	}

	const url = "https://boards.greenhouse.io/acme/jobs/1"
	if err := repo.Upsert(t.Context(), &crawler.CareerPage{
		CompanyID:        acme.ID,
		URL:              url,
		PolitenessDomain: "boards.greenhouse.io",
	}); err != nil {
		t.Fatalf("error seeding career page: %v", err)
	}

	firstSeen, lastSeen := careerPageTimestamps(t, pool, acme.ID, url)
	id := careerPageIDByURL(t, repo, url)

	t.Run("re-points the page to the new company without inserting", func(t *testing.T) {
		if err := repo.Reattribute(t.Context(), id, globex.ID); err != nil {
			t.Fatalf("error re-attributing career page: %v", err)
		}
		if countCareerPages(t, pool) != 1 {
			t.Fatalf("re-attribution should re-point, not insert; want 1 career page, got %d", countCareerPages(t, pool))
		}

		pages, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("error listing career pages: %v", err)
		}
		if len(pages) != 1 {
			t.Fatalf("want 1 career page, got %d", len(pages))
		}
		if pages[0].CompanyID != globex.ID {
			t.Errorf("company_id after re-attribution: want %v, got %v", globex.ID, pages[0].CompanyID)
		}
	})

	t.Run("preserves first_seen and last_seen — a correction, not a re-sighting", func(t *testing.T) {
		// Now keyed by the new owner, since company_id moved to globex.
		newFirstSeen, newLastSeen := careerPageTimestamps(t, pool, globex.ID, url)
		if !newFirstSeen.Equal(firstSeen) {
			t.Errorf("first_seen should be preserved: was %v, now %v", firstSeen, newFirstSeen)
		}
		if !newLastSeen.Equal(lastSeen) {
			t.Errorf("last_seen should be preserved: was %v, now %v", lastSeen, newLastSeen)
		}
	})

	t.Run("re-attributing to the same company again is an idempotent no-op", func(t *testing.T) {
		if err := repo.Reattribute(t.Context(), id, globex.ID); err != nil {
			t.Fatalf("re-attributing to the same owner should be a no-op, got error: %v", err)
		}
		if countCareerPages(t, pool) != 1 {
			t.Errorf("want 1 career page, got %d", countCareerPages(t, pool))
		}
		pages, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("error listing career pages: %v", err)
		}
		if len(pages) != 1 || pages[0].CompanyID != globex.ID {
			t.Errorf("page should still be owned by globex, got %+v", pages)
		}
	})

	t.Run("re-attributing an unknown id is a no-op", func(t *testing.T) {
		if err := repo.Reattribute(t.Context(), uuid.New(), acme.ID); err != nil {
			t.Fatalf("re-attributing an unknown id should be a no-op, got error: %v", err)
		}
		if countCareerPages(t, pool) != 1 {
			t.Errorf("want 1 career page, got %d", countCareerPages(t, pool))
		}
	})

	t.Run("a UNIQUE(company_id, url) collision surfaces as an error", func(t *testing.T) {
		// Seed a second page with the same URL already owned by globex, then try
		// to move the acme-origin page onto globex — merge is the Doctor's job,
		// so the primitive must surface the violation rather than swallow it.
		acme2 := &crawler.Company{CompanyKey: "greenhouse:acme2", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Acme Two"}
		if err := companyRepo.Upsert(t.Context(), acme2); err != nil {
			t.Fatalf("error seeding third company: %v", err)
		}
		const collidingURL = "https://boards.greenhouse.io/shared/jobs/9"
		if err := repo.Upsert(t.Context(), &crawler.CareerPage{
			CompanyID:        acme2.ID,
			URL:              collidingURL,
			PolitenessDomain: "boards.greenhouse.io",
		}); err != nil {
			t.Fatalf("error seeding acme2 page: %v", err)
		}
		if err := repo.Upsert(t.Context(), &crawler.CareerPage{
			CompanyID:        globex.ID,
			URL:              collidingURL,
			PolitenessDomain: "boards.greenhouse.io",
		}); err != nil {
			t.Fatalf("error seeding globex page: %v", err)
		}

		acme2PageID := careerPageIDByURLAndCompany(t, repo, collidingURL, acme2.ID)
		if err := repo.Reattribute(t.Context(), acme2PageID, globex.ID); err == nil {
			t.Error("re-attributing into an existing (company_id, url) should violate the UNIQUE constraint, got nil error")
		}
	})
}

// careerPageIDByURL looks up the id of the single career page with the given
// URL via List, failing the test if it is missing or ambiguous.
func careerPageIDByURL(t *testing.T, repo *postgres.CareerPageRepository, url string) uuid.UUID {
	t.Helper()
	pages, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("error listing career pages: %v", err)
	}
	var found *crawler.CareerPage
	for _, p := range pages {
		if p.URL == url {
			if found != nil {
				t.Fatalf("more than one career page has url %q; use careerPageIDByURLAndCompany", url)
			}
			found = p
		}
	}
	if found == nil {
		t.Fatalf("no career page with url %q", url)
	}
	return found.ID
}

// careerPageIDByURLAndCompany looks up the id of the career page identified by
// (companyID, url) via List, failing the test if it is missing.
func careerPageIDByURLAndCompany(t *testing.T, repo *postgres.CareerPageRepository, url string, companyID uuid.UUID) uuid.UUID {
	t.Helper()
	pages, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("error listing career pages: %v", err)
	}
	for _, p := range pages {
		if p.URL == url && p.CompanyID == companyID {
			return p.ID
		}
	}
	t.Fatalf("no career page with url %q owned by %v", url, companyID)
	return uuid.Nil
}

func careerPageTimestamps(t *testing.T, pool *pgxpool.Pool, companyID uuid.UUID, url string) (firstSeen, lastSeen time.Time) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT first_seen, last_seen FROM career_page WHERE company_id = $1 AND url = $2`,
		companyID, url,
	).Scan(&firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("error reading career page timestamps: %v", err)
	}
	return firstSeen, lastSeen
}

func countCareerPages(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM career_page`).Scan(&n); err != nil {
		t.Fatalf("error counting career pages: %v", err)
	}
	return n
}

// politenessDomains returns the distinct politeness_domain values across all
// career_page rows.
func politenessDomains(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT DISTINCT politeness_domain FROM career_page`)
	if err != nil {
		t.Fatalf("error querying politeness domains: %v", err)
	}
	defer rows.Close()

	domains := []string{}
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			t.Fatalf("error scanning politeness domain: %v", err)
		}
		domains = append(domains, d)
	}
	return domains
}
