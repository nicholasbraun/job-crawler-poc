package postgres_test

import (
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

func TestCareerPageMergeImport(t *testing.T) {
	pool := newTestPool(t)
	companyRepo := postgres.NewCompanyRepository(pool)
	repo := postgres.NewCareerPageRepository(pool)

	early := time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	late := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// A Career Page's company_id FK requires an existing Company.
	company := &crawler.Company{CompanyKey: "acme.com", DisplayDomain: "acme.com", Name: "Acme"}
	if err := companyRepo.Upsert(t.Context(), company); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	companyID := company.ID

	t.Run("inserts a new page with file timestamps", func(t *testing.T) {
		const url = "https://acme.com/careers"
		m := &crawler.CareerPageMerge{
			CompanyID:        companyID,
			URL:              url,
			PolitenessDomain: "acme.com",
			FirstSeen:        &mid,
			LastSeen:         &mid,
		}
		if err := repo.MergeImport(t.Context(), m); err != nil {
			t.Fatalf("merge: %v", err)
		}
		if countCareerPages(t, pool) != 1 {
			t.Errorf("want 1 career page, got %d", countCareerPages(t, pool))
		}
		firstSeen, lastSeen := careerPageTimestamps(t, pool, companyID, url)
		if !firstSeen.Equal(mid) || !lastSeen.Equal(mid) {
			t.Errorf("timestamps: first=%v last=%v, want both %v", firstSeen, lastSeen, mid)
		}
	})

	t.Run("absent timestamps default to now() on insert", func(t *testing.T) {
		const url = "https://acme.com/jobs"
		m := &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com"}
		if err := repo.MergeImport(t.Context(), m); err != nil {
			t.Fatalf("merge: %v", err)
		}
		firstSeen, lastSeen := careerPageTimestamps(t, pool, companyID, url)
		if !firstSeen.Equal(lastSeen) {
			t.Errorf("on insert first_seen (%v) and last_seen (%v) should match", firstSeen, lastSeen)
		}
		if firstSeen.IsZero() {
			t.Error("absent timestamps should default to now(), got zero")
		}
	})

	t.Run("first_seen = LEAST, last_seen = GREATEST", func(t *testing.T) {
		const url = "https://acme.com/monotone"
		seed := &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com", FirstSeen: &mid, LastSeen: &mid}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// Earlier first_seen and later last_seen both widen the window.
		if err := repo.MergeImport(t.Context(), &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com", FirstSeen: &early, LastSeen: &late}); err != nil {
			t.Fatalf("widen: %v", err)
		}
		firstSeen, lastSeen := careerPageTimestamps(t, pool, companyID, url)
		if !firstSeen.Equal(early) {
			t.Errorf("first_seen: got %v, want %v", firstSeen, early)
		}
		if !lastSeen.Equal(late) {
			t.Errorf("last_seen: got %v, want %v", lastSeen, late)
		}

		// A narrower window neither pushes first_seen forward nor last_seen back.
		if err := repo.MergeImport(t.Context(), &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com", FirstSeen: &mid, LastSeen: &mid}); err != nil {
			t.Fatalf("narrow: %v", err)
		}
		firstSeen, lastSeen = careerPageTimestamps(t, pool, companyID, url)
		if !firstSeen.Equal(early) || !lastSeen.Equal(late) {
			t.Errorf("window should stay (%v,%v), got (%v,%v)", early, late, firstSeen, lastSeen)
		}
	})

	t.Run("not a Sighting: absent timestamps never advance last_seen", func(t *testing.T) {
		const url = "https://acme.com/dead"
		seed := &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com", FirstSeen: &early, LastSeen: &early}
		if err := repo.MergeImport(t.Context(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := repo.MergeImport(t.Context(), &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com"}); err != nil {
			t.Fatalf("re-merge with absent timestamps: %v", err)
		}
		firstSeen, lastSeen := careerPageTimestamps(t, pool, companyID, url)
		if !firstSeen.Equal(early) {
			t.Errorf("first_seen should stay %v, got %v", early, firstSeen)
		}
		if !lastSeen.Equal(early) {
			t.Errorf("last_seen should stay %v (NOT advance to now), got %v", early, lastSeen)
		}
	})

	t.Run("politeness_domain refreshes on re-merge", func(t *testing.T) {
		const url = "https://acme.com/domainrefresh"
		if err := repo.MergeImport(t.Context(), &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "host-a"}); err != nil {
			t.Fatalf("first: %v", err)
		}
		before := countCareerPages(t, pool)
		if err := repo.MergeImport(t.Context(), &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "host-b"}); err != nil {
			t.Fatalf("second: %v", err)
		}
		if got := countCareerPages(t, pool); got != before {
			t.Errorf("re-merge should not insert a new row: was %d, now %d", before, got)
		}
		var domain string
		if err := pool.QueryRow(t.Context(),
			`SELECT politeness_domain FROM career_page WHERE company_id = $1 AND url = $2`, companyID, url,
		).Scan(&domain); err != nil {
			t.Fatalf("read politeness_domain: %v", err)
		}
		if domain != "host-b" {
			t.Errorf("politeness_domain should refresh to host-b, got %q", domain)
		}
	})

	t.Run("idempotent: same instruction twice changes nothing", func(t *testing.T) {
		const url = "https://acme.com/idem"
		instr := func() *crawler.CareerPageMerge {
			return &crawler.CareerPageMerge{CompanyID: companyID, URL: url, PolitenessDomain: "acme.com", FirstSeen: &mid, LastSeen: &late}
		}
		if err := repo.MergeImport(t.Context(), instr()); err != nil {
			t.Fatalf("first merge: %v", err)
		}
		count := countCareerPages(t, pool)
		firstSeen, lastSeen := careerPageTimestamps(t, pool, companyID, url)

		if err := repo.MergeImport(t.Context(), instr()); err != nil {
			t.Fatalf("second merge: %v", err)
		}
		if got := countCareerPages(t, pool); got != count {
			t.Errorf("re-merge changed row count: was %d, now %d", count, got)
		}
		newFirst, newLast := careerPageTimestamps(t, pool, companyID, url)
		if !newFirst.Equal(firstSeen) || !newLast.Equal(lastSeen) {
			t.Errorf("re-merge changed timestamps: was (%v,%v), now (%v,%v)", firstSeen, lastSeen, newFirst, newLast)
		}
	})
}
