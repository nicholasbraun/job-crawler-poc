package postgres_test

import (
	"context"
	"sort"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

func TestCatalogDoctorApplyEndToEnd(t *testing.T) {
	pool := newTestPool(t)
	ctx := t.Context()
	companyRepo := postgres.NewCompanyRepository(pool)
	pageRepo := postgres.NewCareerPageRepository(pool)
	store := postgres.NewCatalogDoctorStore(companyRepo, pageRepo)

	seedPage := func(companyKey, ats, domain, url string) {
		t.Helper()
		co := &crawler.Company{CompanyKey: companyKey, ATSProvider: ats, DisplayDomain: domain, Name: companyKey}
		if err := companyRepo.Upsert(ctx, co); err != nil {
			t.Fatalf("seed company %s: %v", companyKey, err)
		}
		page := &crawler.CareerPage{CompanyID: co.ID, URL: url, PolitenessDomain: domain}
		if err := pageRepo.Upsert(ctx, page); err != nil {
			t.Fatalf("seed page %s: %v", url, err)
		}
	}

	// A known-bad Catalog:
	//   healthy self-hosted hub -> kept untouched;
	//   aggregator host + page  -> page and Company swept;
	//   self-hosted company with a real hub AND a single posting -> posting deleted,
	//     hub + Company survive (selective deletion);
	//   fake join.com host-Company owning two tenant roots + one tenant posting ->
	//     roots re-attributed to per-tenant Companies, posting deleted, host swept;
	//   canonical-duplicate http/https pair -> merged to one row, Company survives.
	seedPage("acme.com", "", "acme.com", "https://acme.com/careers")
	seedPage("eu-startups.com", "", "eu-startups.com", "https://eu-startups.com/directory")
	seedPage("sybillatechnologies.com", "", "sybillatechnologies.com", "https://sybillatechnologies.com/careers")
	// Second page on the same self-hosted company (a single posting), added directly.
	sybillaID := companyIDByKey(t, pool, "sybillatechnologies.com")
	if err := pageRepo.Upsert(ctx, &crawler.CareerPage{
		CompanyID: sybillaID, URL: "https://sybillatechnologies.com/career/senior-role", PolitenessDomain: "sybillatechnologies.com",
	}); err != nil {
		t.Fatalf("seed sybilla posting: %v", err)
	}
	// Fake join.com host-Company: two tenant roots + one tenant posting, all owned
	// by the eTLD+1 "join.com" Company (the #46 mis-attribution).
	joinCo := &crawler.Company{CompanyKey: "join.com", DisplayDomain: "join.com", Name: "join.com"}
	if err := companyRepo.Upsert(ctx, joinCo); err != nil {
		t.Fatalf("seed join.com: %v", err)
	}
	for _, url := range []string{
		"https://join.com/companies/zara",
		"https://join.com/companies/accenture",
		"https://join.com/companies/zara/16405887-role",
	} {
		if err := pageRepo.Upsert(ctx, &crawler.CareerPage{CompanyID: joinCo.ID, URL: url, PolitenessDomain: "join.com"}); err != nil {
			t.Fatalf("seed join page %s: %v", url, err)
		}
	}
	// Canonical-duplicate pair under one Company.
	dupCo := &crawler.Company{CompanyKey: "dup.com", DisplayDomain: "dup.com", Name: "dup.com"}
	if err := companyRepo.Upsert(ctx, dupCo); err != nil {
		t.Fatalf("seed dup.com: %v", err)
	}
	for _, url := range []string{"https://dup.com/careers", "http://dup.com/careers"} {
		if err := pageRepo.Upsert(ctx, &crawler.CareerPage{CompanyID: dupCo.ID, URL: url, PolitenessDomain: "dup.com"}); err != nil {
			t.Fatalf("seed dup page %s: %v", url, err)
		}
	}

	if countCareerPages(t, pool) != 9 {
		t.Fatalf("seed: want 9 career pages, got %d", countCareerPages(t, pool))
	}
	if countCompanies(t, pool) != 5 {
		t.Fatalf("seed: want 5 companies, got %d", countCompanies(t, pool))
	}

	// Plan + Apply over the real repositories.
	pages, err := pageRepo.List(ctx)
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	companies, err := companyRepo.List(ctx)
	if err != nil {
		t.Fatalf("list companies: %v", err)
	}
	result := catalogdoctor.Plan(pages, companies)
	if err := catalogdoctor.Apply(ctx, store, result); err != nil {
		t.Fatalf("apply: %v", err)
	}

	// Final state.
	if got := countCareerPages(t, pool); got != 5 {
		t.Errorf("after apply: want 5 career pages, got %d", got)
	}
	wantCompanies := []string{"acme.com", "dup.com", "join:accenture", "join:zara", "sybillatechnologies.com"}
	if got := companyKeys(t, pool); !equalKeys(got, wantCompanies) {
		t.Errorf("after apply: company keys = %v, want %v", got, wantCompanies)
	}
	// The two join tenant roots are now owned by their per-tenant Companies.
	if got := companyKeyOwning(t, pool, "https://join.com/companies/zara"); got != "join:zara" {
		t.Errorf("join zara root owner = %q, want join:zara", got)
	}
	if got := companyKeyOwning(t, pool, "https://join.com/companies/accenture"); got != "join:accenture" {
		t.Errorf("join accenture root owner = %q, want join:accenture", got)
	}
	// The healthy self-hosted hub is untouched.
	if got := companyKeyOwning(t, pool, "https://acme.com/careers"); got != "acme.com" {
		t.Errorf("acme hub owner = %q, want acme.com", got)
	}
	// The canonical-duplicate collapsed onto the https survivor.
	if got := companyKeyOwning(t, pool, "https://dup.com/careers"); got != "dup.com" {
		t.Errorf("dup survivor owner = %q, want dup.com", got)
	}
	if pageExists(t, pool, "http://dup.com/careers") {
		t.Errorf("http dup twin should have been merged away")
	}
	// The sybilla posting is gone but its hub (and Company) survives.
	if pageExists(t, pool, "https://sybillatechnologies.com/career/senior-role") {
		t.Errorf("sybilla single posting should have been deleted")
	}
	if !pageExists(t, pool, "https://sybillatechnologies.com/careers") {
		t.Errorf("sybilla hub should survive")
	}

	// Idempotency: a second Plan over the cleaned Catalog is all-keep with no
	// orphans, and a second Apply changes nothing.
	pages2, err := pageRepo.List(ctx)
	if err != nil {
		t.Fatalf("re-list pages: %v", err)
	}
	companies2, err := companyRepo.List(ctx)
	if err != nil {
		t.Fatalf("re-list companies: %v", err)
	}
	result2 := catalogdoctor.Plan(pages2, companies2)
	for _, d := range result2.Pages {
		if d.Action != catalogdoctor.Keep {
			t.Errorf("second plan: %s action = %s, want keep (idempotent)", d.Page.URL, d.Action)
		}
	}
	if len(result2.Orphans) != 0 {
		t.Errorf("second plan: orphans = %d, want 0", len(result2.Orphans))
	}
	if err := catalogdoctor.Apply(ctx, store, result2); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if got := countCareerPages(t, pool); got != 5 {
		t.Errorf("after second apply: career pages = %d, want 5 (unchanged)", got)
	}
	if got := countCompanies(t, pool); got != 5 {
		t.Errorf("after second apply: companies = %d, want 5 (unchanged)", got)
	}
}

func companyIDByKey(t *testing.T, pool *pgxpool.Pool, key string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `SELECT id FROM company WHERE company_key = $1`, key).Scan(&id); err != nil {
		t.Fatalf("company id for %s: %v", key, err)
	}
	return id
}

func companyKeys(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT company_key FROM company`)
	if err != nil {
		t.Fatalf("list company keys: %v", err)
	}
	defer rows.Close()
	keys := []string{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan company key: %v", err)
		}
		keys = append(keys, k)
	}
	return keys
}

// companyKeyOwning returns the company_key of the Company that owns the given
// Career Page URL, or "" when no such page exists.
func companyKeyOwning(t *testing.T, pool *pgxpool.Pool, url string) string {
	t.Helper()
	var key string
	err := pool.QueryRow(context.Background(), `
		SELECT c.company_key FROM career_page p JOIN company c ON c.id = p.company_id WHERE p.url = $1
	`, url).Scan(&key)
	if err != nil {
		return ""
	}
	return key
}

func pageExists(t *testing.T, pool *pgxpool.Pool, url string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM career_page WHERE url = $1`, url).Scan(&n); err != nil {
		t.Fatalf("count page %s: %v", url, err)
	}
	return n > 0
}

func equalKeys(got, want []string) bool {
	g := append([]string{}, got...)
	w := append([]string{}, want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}
