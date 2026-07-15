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

// companyFields reads the mutable fields of a company row. ats_provider is
// nullable (self-hosted stores NULL), so it is returned as a *string.
func companyFields(t *testing.T, pool *pgxpool.Pool, key string) (atsProvider *string, name, displayDomain string) {
	t.Helper()
	err := pool.QueryRow(context.Background(),
		`SELECT ats_provider, name, display_domain FROM company WHERE company_key = $1`, key,
	).Scan(&atsProvider, &name, &displayDomain)
	if err != nil {
		t.Fatalf("read company fields: %v", err)
	}
	return
}

// companyWebsite reads the nullable website column: nil == SQL NULL (unknown /
// self-hosted-no-homepage), matching how MergeImport encodes an empty Website.
func companyWebsite(t *testing.T, pool *pgxpool.Pool, key string) *string {
	t.Helper()
	var website *string
	if err := pool.QueryRow(context.Background(),
		`SELECT website FROM company WHERE company_key = $1`, key,
	).Scan(&website); err != nil {
		t.Fatalf("read company website: %v", err)
	}
	return website
}

func TestCompanyMergeImport(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)

	early := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("inserts a new company and writes back its id", func(t *testing.T) {
		m := &crawler.CompanyMerge{
			CompanyKey:           "acme.com",
			Name:                 "Acme",
			NamePresent:          true,
			DisplayDomain:        "acme.com",
			DisplayDomainPresent: true,
			FirstSeen:            &mid,
			LastSeen:             &mid,
		}
		if err := repo.MergeImport(t.Context(), m); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if m.ID == uuid.Nil {
			t.Error("MergeImport should write back a generated id")
		}
		if countCompanies(t, pool) != 1 {
			t.Errorf("want 1 company, got %d", countCompanies(t, pool))
		}
		firstSeen, lastSeen := companyTimestamps(t, pool, "acme.com")
		if !firstSeen.Equal(mid) || !lastSeen.Equal(mid) {
			t.Errorf("timestamps: first=%v last=%v, want both %v", firstSeen, lastSeen, mid)
		}
		_, name, displayDomain := companyFields(t, pool, "acme.com")
		if name != "Acme" || displayDomain != "acme.com" {
			t.Errorf("fields: name=%q displayDomain=%q, want Acme / acme.com", name, displayDomain)
		}
	})

	t.Run("absent timestamps default to now() on first insert, first_seen == last_seen", func(t *testing.T) {
		m := &crawler.CompanyMerge{CompanyKey: "globex.com", Name: "Globex", NamePresent: true}
		if err := repo.MergeImport(t.Context(), m); err != nil {
			t.Fatalf("merge: %v", err)
		}
		firstSeen, lastSeen := companyTimestamps(t, pool, "globex.com")
		if !firstSeen.Equal(lastSeen) {
			t.Errorf("on insert first_seen (%v) and last_seen (%v) should match", firstSeen, lastSeen)
		}
		if firstSeen.IsZero() {
			t.Error("absent timestamps should default to now(), got zero")
		}
	})

	t.Run("insert with only a past lastSeen clamps first_seen to it (no inverted interval)", func(t *testing.T) {
		m := &crawler.CompanyMerge{CompanyKey: "clamp.com", Name: "Clamp", NamePresent: true, LastSeen: &early}
		if err := repo.MergeImport(t.Context(), m); err != nil {
			t.Fatalf("merge: %v", err)
		}
		firstSeen, lastSeen := companyTimestamps(t, pool, "clamp.com")
		if !lastSeen.Equal(early) {
			t.Errorf("last_seen: got %v, want the file value %v", lastSeen, early)
		}
		if !firstSeen.Equal(early) {
			t.Errorf("first_seen should clamp to last_seen %v (not default to now()), got %v", early, firstSeen)
		}
	})

	t.Run("first_seen = LEAST(existing, file)", func(t *testing.T) {
		key := "least.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, Name: "L", NamePresent: true, FirstSeen: &mid, LastSeen: &mid}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// An earlier file first_seen pulls it back.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, FirstSeen: &early}); err != nil {
			t.Fatalf("merge earlier: %v", err)
		}
		if firstSeen, _ := companyTimestamps(t, pool, key); !firstSeen.Equal(early) {
			t.Errorf("first_seen after earlier merge: got %v, want %v", firstSeen, early)
		}

		// A later file first_seen does not push it forward.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, FirstSeen: &late}); err != nil {
			t.Fatalf("merge later: %v", err)
		}
		if firstSeen, _ := companyTimestamps(t, pool, key); !firstSeen.Equal(early) {
			t.Errorf("first_seen should stay at the earliest %v, got %v", early, firstSeen)
		}
	})

	t.Run("last_seen = GREATEST(existing, file)", func(t *testing.T) {
		key := "greatest.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, Name: "G", NamePresent: true, FirstSeen: &mid, LastSeen: &mid}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// A later file last_seen advances it.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, LastSeen: &late}); err != nil {
			t.Fatalf("merge later: %v", err)
		}
		if _, lastSeen := companyTimestamps(t, pool, key); !lastSeen.Equal(late) {
			t.Errorf("last_seen after later merge: got %v, want %v", lastSeen, late)
		}

		// An earlier file last_seen does not pull it back.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, LastSeen: &early}); err != nil {
			t.Fatalf("merge earlier: %v", err)
		}
		if _, lastSeen := companyTimestamps(t, pool, key); !lastSeen.Equal(late) {
			t.Errorf("last_seen should stay at the latest %v, got %v", late, lastSeen)
		}
	})

	t.Run("import is not a Sighting: absent file timestamps never advance last_seen to now", func(t *testing.T) {
		// Unlike Upsert, which stamps last_seen = now() on conflict, a merge with
		// absent (nil) timestamps must leave both first_seen and last_seen exactly
		// where the seed put them — an imported-but-dead page stays as stale as it is.
		key := "notasighting.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, Name: "N", NamePresent: true, FirstSeen: &early, LastSeen: &early}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key}); err != nil {
			t.Fatalf("re-merge with absent timestamps: %v", err)
		}
		firstSeen, lastSeen := companyTimestamps(t, pool, key)
		if !firstSeen.Equal(early) {
			t.Errorf("first_seen should stay %v, got %v", early, firstSeen)
		}
		if !lastSeen.Equal(early) {
			t.Errorf("last_seen should stay %v (NOT advance to now), got %v", early, lastSeen)
		}
	})

	t.Run("presence-wins: a sparse record never blanks existing fields", func(t *testing.T) {
		key := "sparse.com"
		seed := &crawler.CompanyMerge{
			CompanyKey:           key,
			Name:                 "Acme Corp",
			NamePresent:          true,
			DisplayDomain:        "acme.com",
			DisplayDomainPresent: true,
			ATSProvider:          "greenhouse",
			ATSProviderPresent:   true,
		}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A merge with every field absent must leave the catalogued values intact.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key}); err != nil {
			t.Fatalf("sparse merge: %v", err)
		}
		ats, name, displayDomain := companyFields(t, pool, key)
		if name != "Acme Corp" || displayDomain != "acme.com" {
			t.Errorf("sparse merge blanked fields: name=%q displayDomain=%q", name, displayDomain)
		}
		if ats == nil || *ats != "greenhouse" {
			t.Errorf("sparse merge blanked ats_provider: got %v", ats)
		}
	})

	t.Run("present mutable fields update", func(t *testing.T) {
		key := "renamed.com"
		seed := &crawler.CompanyMerge{
			CompanyKey:           key,
			Name:                 "Old",
			NamePresent:          true,
			DisplayDomain:        "old.com",
			DisplayDomainPresent: true,
		}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, Name: "Renamed", NamePresent: true}); err != nil {
			t.Fatalf("update: %v", err)
		}
		_, name, displayDomain := companyFields(t, pool, key)
		if name != "Renamed" {
			t.Errorf("name should update to Renamed, got %q", name)
		}
		if displayDomain != "old.com" {
			t.Errorf("absent displayDomain should be preserved, got %q", displayDomain)
		}
	})

	t.Run("explicit empty atsProvider sets self-hosted (NULL)", func(t *testing.T) {
		key := "selfhosted.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, ATSProvider: "greenhouse", ATSProviderPresent: true}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// An explicit "" is a definite value: self-hosted, stored as NULL.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, ATSProvider: "", ATSProviderPresent: true}); err != nil {
			t.Fatalf("clear ats: %v", err)
		}
		ats, _, _ := companyFields(t, pool, key)
		if ats != nil {
			t.Errorf("explicit empty ats_provider should be NULL, got %q", *ats)
		}
	})

	t.Run("idempotent: merging the same instruction twice changes no row and no data", func(t *testing.T) {
		key := "idempotent.com"
		instr := func() *crawler.CompanyMerge {
			return &crawler.CompanyMerge{
				CompanyKey:           key,
				Name:                 "Idem",
				NamePresent:          true,
				DisplayDomain:        "idem.com",
				DisplayDomainPresent: true,
				ATSProvider:          "lever",
				ATSProviderPresent:   true,
				FirstSeen:            &mid,
				LastSeen:             &late,
			}
		}
		if err := repo.MergeImport(t.Context(), instr()); err != nil {
			t.Fatalf("first merge: %v", err)
		}
		count := countCompanies(t, pool)
		firstSeen, lastSeen := companyTimestamps(t, pool, key)
		ats0, name0, disp0 := companyFields(t, pool, key)

		if err := repo.MergeImport(t.Context(), instr()); err != nil {
			t.Fatalf("second merge: %v", err)
		}
		if got := countCompanies(t, pool); got != count {
			t.Errorf("re-merge changed row count: was %d, now %d", count, got)
		}
		newFirst, newLast := companyTimestamps(t, pool, key)
		if !newFirst.Equal(firstSeen) || !newLast.Equal(lastSeen) {
			t.Errorf("re-merge changed timestamps: was (%v,%v), now (%v,%v)", firstSeen, lastSeen, newFirst, newLast)
		}
		ats1, name1, disp1 := companyFields(t, pool, key)
		if name1 != name0 || disp1 != disp0 || (ats0 == nil) != (ats1 == nil) || (ats0 != nil && *ats0 != *ats1) {
			t.Errorf("re-merge changed fields: was (%v,%q,%q), now (%v,%q,%q)", ats0, name0, disp0, ats1, name1, disp1)
		}
	})
}

func TestCompanyMergeImportWebsite(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewCompanyRepository(pool)

	t.Run("new company stores the website", func(t *testing.T) {
		key := "acme.com"
		m := &crawler.CompanyMerge{CompanyKey: key, Website: "https://acme.com", WebsitePresent: true}
		if err := repo.MergeImport(t.Context(), m); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if got := companyWebsite(t, pool, key); got == nil || *got != "https://acme.com" {
			t.Errorf("website: got %v, want https://acme.com", got)
		}
	})

	t.Run("sparse merge preserves an existing website", func(t *testing.T) {
		key := "sparse-web.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, Website: "https://sparse.com", WebsitePresent: true}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// A merge that does not mention website must leave the catalogued one intact.
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, Name: "S", NamePresent: true}); err != nil {
			t.Fatalf("sparse merge: %v", err)
		}
		if got := companyWebsite(t, pool, key); got == nil || *got != "https://sparse.com" {
			t.Errorf("sparse merge changed website: got %v, want https://sparse.com", got)
		}
	})

	t.Run("present website updates", func(t *testing.T) {
		key := "updated-web.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, Website: "https://a.com", WebsitePresent: true}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, Website: "https://b.com", WebsitePresent: true}); err != nil {
			t.Fatalf("update: %v", err)
		}
		if got := companyWebsite(t, pool, key); got == nil || *got != "https://b.com" {
			t.Errorf("website should update to https://b.com, got %v", got)
		}
	})

	t.Run("explicit empty website clears to NULL", func(t *testing.T) {
		key := "cleared-web.com"
		seed := &crawler.CompanyMerge{CompanyKey: key, Website: "https://c.com", WebsitePresent: true}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		// An explicit "" is a deliberate clear, stored as NULL (the ats_provider idiom).
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, Website: "", WebsitePresent: true}); err != nil {
			t.Fatalf("clear website: %v", err)
		}
		if got := companyWebsite(t, pool, key); got != nil {
			t.Errorf("explicit empty website should be NULL, got %q", *got)
		}
	})

	t.Run("website round-trips through List", func(t *testing.T) {
		key := "roundtrip.com"
		if err := repo.MergeImport(t.Context(), &crawler.CompanyMerge{CompanyKey: key, Website: "https://roundtrip.com", WebsitePresent: true}); err != nil {
			t.Fatalf("merge: %v", err)
		}
		companies, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		var found *crawler.Company
		for _, c := range companies {
			if c.CompanyKey == key {
				found = c
				break
			}
		}
		if found == nil {
			t.Fatalf("company %q missing from List", key)
		}
		if found.Website != "https://roundtrip.com" {
			t.Errorf("List website: got %q, want https://roundtrip.com", found.Website)
		}
	})
}
