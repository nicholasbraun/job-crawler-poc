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
