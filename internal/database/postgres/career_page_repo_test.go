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
	t.Run("ListURLs returns every catalogued url", func(t *testing.T) {
		urls, err := repo.ListURLs(t.Context())
		if err != nil {
			t.Fatalf("error listing career page urls: %v", err)
		}
		want := map[string]bool{
			acmeURL: true,
			"https://boards.greenhouse.io/globex/jobs/2": true,
		}
		if len(urls) != len(want) {
			t.Fatalf("want %d urls, got %d: %v", len(want), len(urls), urls)
		}
		for _, u := range urls {
			if !want[u] {
				t.Errorf("unexpected url in catalog: %q", u)
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

func TestCareerPageRepositoryListURLsEmpty(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCareerPageRepository(pool)

	urls, err := repo.ListURLs(t.Context())
	if err != nil {
		t.Fatalf("error listing career page urls: %v", err)
	}
	if urls == nil {
		t.Fatal("ListURLs must return a non-nil slice, got nil")
	}
	if len(urls) != 0 {
		t.Errorf("empty catalog should yield no urls, got %v", urls)
	}
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
