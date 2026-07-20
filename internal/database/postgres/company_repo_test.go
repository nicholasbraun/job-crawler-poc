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

func TestCompanyRepository(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)

	company := &crawler.Company{
		CompanyKey:    "greenhouse:acme",
		ATSProvider:   "greenhouse",
		DisplayDomain: "boards.greenhouse.io",
		Name:          "Acme",
		NameSource:    crawler.NameSourceTitle,
	}

	t.Run("Upsert inserts and returns a generated id", func(t *testing.T) {
		if err := repo.Upsert(t.Context(), company); err != nil {
			t.Fatalf("error upserting company: %v", err)
		}
		if company.ID == uuid.Nil {
			t.Fatal("Upsert should write back a generated id")
		}
	})

	t.Run("re-upserting same company_key updates in place, preserving first_seen", func(t *testing.T) {
		firstID := company.ID
		firstSeen, lastSeen := companyTimestamps(t, pool, "greenhouse:acme")
		if !firstSeen.Equal(lastSeen) {
			t.Fatalf("on insert first_seen (%v) and last_seen (%v) should match", firstSeen, lastSeen)
		}

		time.Sleep(10 * time.Millisecond)

		updated := *company
		updated.Name = "Acme Corporation"
		if err := repo.Upsert(t.Context(), &updated); err != nil {
			t.Fatalf("error re-upserting company: %v", err)
		}
		if updated.ID != firstID {
			t.Errorf("upsert should return the existing id: was %v, now %v", firstID, updated.ID)
		}
		if countCompanies(t, pool) != 1 {
			t.Errorf("upsert should not create a duplicate; want 1 company, got %d", countCompanies(t, pool))
		}

		newFirstSeen, newLastSeen := companyTimestamps(t, pool, "greenhouse:acme")
		if !newFirstSeen.Equal(firstSeen) {
			t.Errorf("first_seen should be preserved: was %v, now %v", firstSeen, newFirstSeen)
		}
		if !newLastSeen.After(firstSeen) {
			t.Errorf("last_seen (%v) should advance past first_seen (%v)", newLastSeen, firstSeen)
		}
	})

	t.Run("distinct tenants on the same ATS host are distinct rows", func(t *testing.T) {
		globex := &crawler.Company{
			CompanyKey:    "greenhouse:globex",
			ATSProvider:   "greenhouse",
			DisplayDomain: "boards.greenhouse.io",
			Name:          "Globex",
		}
		if err := repo.Upsert(t.Context(), globex); err != nil {
			t.Fatalf("error upserting second tenant: %v", err)
		}
		if countCompanies(t, pool) != 2 {
			t.Errorf("distinct provider-qualified keys should be 2 rows, got %d", countCompanies(t, pool))
		}
	})

	t.Run("self-hosted company stores NULL ats_provider", func(t *testing.T) {
		selfHosted := &crawler.Company{
			CompanyKey:    "initech.com",
			ATSProvider:   "",
			DisplayDomain: "careers.initech.com",
			Name:          "Initech",
		}
		if err := repo.Upsert(t.Context(), selfHosted); err != nil {
			t.Fatalf("error upserting self-hosted company: %v", err)
		}

		var atsProvider *string
		err := pool.QueryRow(context.Background(),
			`SELECT ats_provider FROM company WHERE company_key = $1`, "initech.com",
		).Scan(&atsProvider)
		if err != nil {
			t.Fatalf("error reading ats_provider: %v", err)
		}
		if atsProvider != nil {
			t.Errorf("self-hosted ats_provider should be NULL, got %q", *atsProvider)
		}
	})

	t.Run("List returns every company and surfaces NULL ats_provider as empty", func(t *testing.T) {
		companies, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("error listing companies: %v", err)
		}
		// Prior subtests inserted acme (updated), globex, and initech.
		if len(companies) != 3 {
			t.Fatalf("want 3 companies, got %d", len(companies))
		}

		byKey := map[string]*crawler.Company{}
		for _, c := range companies {
			byKey[c.CompanyKey] = c
		}
		if selfHosted := byKey["initech.com"]; selfHosted == nil {
			t.Fatal("initech.com missing from list")
		} else if selfHosted.ATSProvider != "" {
			t.Errorf("self-hosted ats_provider should be empty, got %q", selfHosted.ATSProvider)
		}
		if acme := byKey["greenhouse:acme"]; acme == nil || acme.ATSProvider != "greenhouse" {
			t.Errorf("greenhouse:acme should carry its provider, got %+v", acme)
		}
		// The Name Ladder rung set at insert round-trips through List.
		if acme := byKey["greenhouse:acme"]; acme == nil || acme.NameSource != crawler.NameSourceTitle {
			t.Errorf("greenhouse:acme NameSource should round-trip as title, got %+v", acme)
		}
		// initech was inserted with a zero-value NameSource, stored as NULL, and
		// surfaced as "".
		if selfHosted := byKey["initech.com"]; selfHosted == nil || selfHosted.NameSource != "" {
			t.Errorf("initech.com NameSource should surface NULL as empty, got %+v", selfHosted)
		}
	})

	t.Run("List returns an empty non-nil slice on an empty catalog", func(t *testing.T) {
		emptyRepo := postgres.NewCompanyRepository(newTestPool(t))
		companies, err := emptyRepo.List(t.Context())
		if err != nil {
			t.Fatalf("error listing companies: %v", err)
		}
		if companies == nil {
			t.Error("want empty non-nil slice, got nil")
		}
		if len(companies) != 0 {
			t.Errorf("want 0 companies, got %d", len(companies))
		}
	})
}

func TestCompanyRepositoryDelete(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)
	careerPageRepo := postgres.NewCareerPageRepository(pool)

	t.Run("deletes a company with no career pages", func(t *testing.T) {
		company := &crawler.Company{CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Acme"}
		if err := repo.Upsert(t.Context(), company); err != nil {
			t.Fatalf("error seeding company: %v", err)
		}
		if countCompanies(t, pool) != 1 {
			t.Fatalf("want 1 company before delete, got %d", countCompanies(t, pool))
		}

		if err := repo.Delete(t.Context(), company.ID); err != nil {
			t.Fatalf("error deleting company: %v", err)
		}
		if countCompanies(t, pool) != 0 {
			t.Errorf("want 0 companies after delete, got %d", countCompanies(t, pool))
		}

		// Idempotency: re-deleting and deleting an unknown id are no-ops.
		if err := repo.Delete(t.Context(), company.ID); err != nil {
			t.Fatalf("re-deleting should be a no-op, got error: %v", err)
		}
		if err := repo.Delete(t.Context(), uuid.New()); err != nil {
			t.Fatalf("deleting an unknown id should be a no-op, got error: %v", err)
		}
		if countCompanies(t, pool) != 0 {
			t.Errorf("want 0 companies, got %d", countCompanies(t, pool))
		}
	})

	t.Run("deleting a company that still owns career pages is rejected, then succeeds once they are removed", func(t *testing.T) {
		company := &crawler.Company{CompanyKey: "greenhouse:globex", ATSProvider: "greenhouse", DisplayDomain: "boards.greenhouse.io", Name: "Globex"}
		if err := repo.Upsert(t.Context(), company); err != nil {
			t.Fatalf("error seeding company: %v", err)
		}
		const url = "https://boards.greenhouse.io/globex/jobs/1"
		if err := careerPageRepo.Upsert(t.Context(), &crawler.CareerPage{
			CompanyID:        company.ID,
			URL:              url,
			PolitenessDomain: "boards.greenhouse.io",
		}); err != nil {
			t.Fatalf("error seeding career page: %v", err)
		}

		// The FK has no ON DELETE CASCADE, so the owning Company cannot be swept
		// while it still owns a Career Page.
		if err := repo.Delete(t.Context(), company.ID); err == nil {
			t.Error("deleting a company that still owns career pages should violate the FK, got nil error")
		}
		if countCompanies(t, pool) != 1 {
			t.Errorf("company should remain after the rejected delete, got %d", countCompanies(t, pool))
		}

		// Remove the Career Page first, then the orphaned Company sweeps cleanly.
		id := careerPageIDByURL(t, careerPageRepo, url)
		if err := careerPageRepo.Delete(t.Context(), id); err != nil {
			t.Fatalf("error deleting career page: %v", err)
		}
		if err := repo.Delete(t.Context(), company.ID); err != nil {
			t.Fatalf("error deleting company after its career pages were removed: %v", err)
		}
		if countCompanies(t, pool) != 0 {
			t.Errorf("want 0 companies after the orphan sweep, got %d", countCompanies(t, pool))
		}
	})
}

// TestCompanyUpsertWebsite proves Upsert never touches the website column
// (ADR-0013): a Discovery-created row leaves it NULL, and a Discovery re-sight of
// an imported company must not blank the website the import wrote.
func TestCompanyUpsertWebsite(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)

	t.Run("discovery Upsert leaves website NULL", func(t *testing.T) {
		c := &crawler.Company{CompanyKey: "discovered.com", DisplayDomain: "discovered.com", Name: "Discovered"}
		if err := repo.Upsert(t.Context(), c); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got := companyWebsite(t, pool, "discovered.com"); got != nil {
			t.Errorf("discovery-created website should be NULL, got %q", *got)
		}
	})

	t.Run("Upsert preserves an imported website", func(t *testing.T) {
		key := "imported.com"
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, Website: "https://imported.com", WebsitePresent: true}); err != nil {
			t.Fatalf("import: %v", err)
		}
		// Discovery re-sights the company later; its Upsert carries no website ("").
		if err := repo.Upsert(t.Context(), &crawler.Company{CompanyKey: key, DisplayDomain: "imported.com", Name: "Imported"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got := companyWebsite(t, pool, key); got == nil || *got != "https://imported.com" {
			t.Errorf("Upsert must not blank the imported website, got %v", got)
		}
	})
}

// TestCompanyUpsertNameSource proves the Name Ladder rung (ADR-0025) is stored,
// that a zero-value Source lands as SQL NULL (the legacy/unknown path), and that
// a re-crawl overwrites the Source in place.
func TestCompanyUpsertNameSource(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)

	nameSource := func(key string) *string {
		t.Helper()
		var s *string
		if err := pool.QueryRow(context.Background(),
			`SELECT name_source FROM company WHERE company_key = $1`, key,
		).Scan(&s); err != nil {
			t.Fatalf("read company name_source: %v", err)
		}
		return s
	}

	t.Run("zero-value NameSource is stored as NULL and round-trips as empty", func(t *testing.T) {
		c := &crawler.Company{CompanyKey: "legacy.com", DisplayDomain: "legacy.com", Name: "Legacy"}
		if err := repo.Upsert(t.Context(), c); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got := nameSource("legacy.com"); got != nil {
			t.Errorf("zero-value NameSource should be NULL, got %q", *got)
		}
	})

	t.Run("a set NameSource is stored and a re-crawl overwrites it in place", func(t *testing.T) {
		key := "acme.com"
		if err := repo.Upsert(t.Context(), &crawler.Company{
			CompanyKey: key, DisplayDomain: key, Name: "Acme", NameSource: crawler.NameSourceDomain,
		}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if got := nameSource(key); got == nil || *got != string(crawler.NameSourceDomain) {
			t.Errorf("NameSource should be stored as domain, got %v", got)
		}
		// A later crawl reads a JSON-LD name: the Source is upgraded in place.
		if err := repo.Upsert(t.Context(), &crawler.Company{
			CompanyKey: key, DisplayDomain: key, Name: "Acme Inc", NameSource: crawler.NameSourceJSONLD,
		}); err != nil {
			t.Fatalf("re-upsert: %v", err)
		}
		if got := nameSource(key); got == nil || *got != string(crawler.NameSourceJSONLD) {
			t.Errorf("re-crawl should overwrite NameSource to jsonld, got %v", got)
		}
	})
}

// TestCompanyRepositoryListPagelessSeeds pins the pageless-seeds query that
// seeds Keyword Crawls from imported prospects with no known Career Page. It
// seeds four companies exercising every branch: two Pageless-with-Website
// (returned, each paired with its stored CompanyKey), one with a Website but an
// owned Career Page (excluded), and one Pageless-without-Website (excluded). The
// self-heal subtest then gives one of the returned companies a Career Page and
// proves its Website stops seeding.
func TestCompanyRepositoryListPagelessSeeds(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)
	pageRepo := postgres.NewCareerPageRepository(pool)

	// A, D: Pageless with a Website -- expected in the result set. MergeImport is
	// the only write path that stores a Website (Upsert never touches the column).
	a := &crawler.CompanyMerge{CompanyKey: "pageless.io", Website: "https://pageless.io", WebsitePresent: true}
	if err := repo.MergeImport(t.Context(), a); err != nil {
		t.Fatalf("error importing pageless company A: %v", err)
	}
	d := &crawler.CompanyMerge{CompanyKey: "second.io", Website: "https://second.io", WebsitePresent: true}
	if err := repo.MergeImport(t.Context(), d); err != nil {
		t.Fatalf("error importing pageless company D: %v", err)
	}

	// B: has a Website AND an owned Career Page -- excluded (with-pages exclusion).
	b := &crawler.CompanyMerge{CompanyKey: "greenhouse:acme", Website: "https://acme.com", WebsitePresent: true}
	if err := repo.MergeImport(t.Context(), b); err != nil {
		t.Fatalf("error importing company B: %v", err)
	}
	if err := pageRepo.Upsert(t.Context(), &crawler.CareerPage{
		CompanyID:        b.ID,
		URL:              "https://boards.greenhouse.io/acme",
		PolitenessDomain: "boards.greenhouse.io",
	}); err != nil {
		t.Fatalf("error seeding company B career page: %v", err)
	}

	// C: Pageless but without a Website (Upsert leaves website NULL) -- excluded
	// (without-website exclusion).
	if err := repo.Upsert(t.Context(), &crawler.Company{
		CompanyKey:    "nowebsite.com",
		DisplayDomain: "nowebsite.com",
		Name:          "NoWeb",
	}); err != nil {
		t.Fatalf("error upserting company C: %v", err)
	}

	t.Run("returns only pageless companies that declare a website, paired with their key", func(t *testing.T) {
		seeds, err := repo.ListPagelessSeeds(t.Context())
		if err != nil {
			t.Fatalf("error listing pageless company seeds: %v", err)
		}
		want := map[string]string{
			"https://pageless.io": "pageless.io",
			"https://second.io":   "second.io",
		}
		if len(seeds) != len(want) {
			t.Fatalf("want %d seeds, got %d: %v", len(want), len(seeds), seeds)
		}
		for _, s := range seeds {
			wantKey, ok := want[s.URL]
			if !ok {
				t.Errorf("unexpected website in result: %q", s.URL)
				continue
			}
			if s.CompanyKey != wantKey {
				t.Errorf("seed %q CompanyKey: want %q, got %q", s.URL, wantKey, s.CompanyKey)
			}
		}
	})

	// Runs after the assertion above: give A a Career Page so it is no longer
	// Pageless. Its Website must drop out; D (still pageless) must remain.
	t.Run("self-heals: a company that gains a career page stops seeding", func(t *testing.T) {
		if err := pageRepo.Upsert(t.Context(), &crawler.CareerPage{
			CompanyID:        a.ID,
			URL:              "https://pageless.io/careers",
			PolitenessDomain: "pageless.io",
		}); err != nil {
			t.Fatalf("error attaching career page to company A: %v", err)
		}

		seeds, err := repo.ListPagelessSeeds(t.Context())
		if err != nil {
			t.Fatalf("error listing pageless company seeds: %v", err)
		}
		want := map[string]string{"https://second.io": "second.io"}
		if len(seeds) != len(want) {
			t.Fatalf("after self-heal want %d seeds, got %d: %v", len(want), len(seeds), seeds)
		}
		for _, s := range seeds {
			wantKey, ok := want[s.URL]
			if !ok {
				t.Errorf("unexpected website after self-heal: %q", s.URL)
				continue
			}
			if s.CompanyKey != wantKey {
				t.Errorf("seed %q CompanyKey after self-heal: want %q, got %q", s.URL, wantKey, s.CompanyKey)
			}
		}
	})
}

func TestCompanyRepositoryListPagelessSeedsEmpty(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)

	got, err := repo.ListPagelessSeeds(t.Context())
	if err != nil {
		t.Fatalf("error listing pageless company seeds: %v", err)
	}
	if got == nil {
		t.Fatal("ListPagelessSeeds must return a non-nil slice, got nil")
	}
	if len(got) != 0 {
		t.Errorf("empty catalog should yield no seeds, got %v", got)
	}
}

func companyTimestamps(t *testing.T, pool *pgxpool.Pool, companyKey string) (firstSeen, lastSeen time.Time) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT first_seen, last_seen FROM company WHERE company_key = $1`, companyKey,
	).Scan(&firstSeen, &lastSeen)
	if err != nil {
		t.Fatalf("error reading company timestamps: %v", err)
	}
	return firstSeen, lastSeen
}

func countCompanies(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM company`).Scan(&n); err != nil {
		t.Fatalf("error counting companies: %v", err)
	}
	return n
}
