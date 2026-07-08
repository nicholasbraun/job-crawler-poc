package careerpageprocessor_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
)

// --- inline test doubles ---

type spyCompanyRepo struct {
	upserted []*crawler.Company
	assignID uuid.UUID
}

func (r *spyCompanyRepo) Upsert(ctx context.Context, c *crawler.Company) error {
	c.ID = r.assignID
	// Store a copy so later mutations by the caller don't corrupt the record.
	saved := *c
	r.upserted = append(r.upserted, &saved)
	return nil
}

func (r *spyCompanyRepo) List(ctx context.Context) ([]*crawler.Company, error) {
	return r.upserted, nil
}

type spyCareerPageRepo struct {
	upserted []*crawler.CareerPage
}

func (r *spyCareerPageRepo) Upsert(ctx context.Context, p *crawler.CareerPage) error {
	saved := *p
	r.upserted = append(r.upserted, &saved)
	return nil
}

func (r *spyCareerPageRepo) ListURLs(ctx context.Context) ([]string, error) {
	urls := []string{}
	for _, p := range r.upserted {
		urls = append(urls, p.URL)
	}
	return urls, nil
}

func (r *spyCareerPageRepo) List(ctx context.Context) ([]*crawler.CareerPage, error) {
	return r.upserted, nil
}

type spyConfirmer struct {
	calls  int
	result bool
}

func (c *spyConfirmer) Confirm(ctx context.Context, url string, content *crawler.Content) (bool, error) {
	c.calls++
	return c.result, nil
}

func newURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("error building url: %v", err)
	}
	return u
}

func TestCareerPageProcessorCertainSkipsLLM(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}
	confirmer := &spyConfirmer{result: false} // would reject if consulted

	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            confirmer,
	})

	raw := &crawler.RawCareerPage{
		URL:     newURL(t, "https://boards.greenhouse.io/acme"),
		Content: crawler.Content{Title: "Jobs at Acme"},
		Certain: true,
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if confirmer.calls != 0 {
		t.Errorf("a certain (ATS) career page should skip the LLM, but Confirm was called %d times", confirmer.calls)
	}
	if len(companyRepo.upserted) != 1 {
		t.Fatalf("want 1 company upserted, got %d", len(companyRepo.upserted))
	}
	company := companyRepo.upserted[0]
	if company.CompanyKey != "greenhouse:acme" {
		t.Errorf("company key = %q, want greenhouse:acme", company.CompanyKey)
	}
	if company.Name != "Acme" {
		t.Errorf("company name = %q, want Acme (stripped from title)", company.Name)
	}
	if company.DisplayDomain != "" {
		t.Errorf("ATS company with no Organization JSON-LD should have empty DisplayDomain, got %q", company.DisplayDomain)
	}
	if len(careerPageRepo.upserted) != 1 {
		t.Fatalf("want 1 career page upserted, got %d", len(careerPageRepo.upserted))
	}
	cp := careerPageRepo.upserted[0]
	if cp.CompanyID != companyRepo.assignID {
		t.Errorf("career page CompanyID = %v, want %v", cp.CompanyID, companyRepo.assignID)
	}
	if cp.URL != "https://boards.greenhouse.io/acme" {
		t.Errorf("career page URL = %q, want the canonical board root", cp.URL)
	}
	if cp.PolitenessDomain != "boards.greenhouse.io" {
		t.Errorf("career page PolitenessDomain = %q, want boards.greenhouse.io", cp.PolitenessDomain)
	}
}

func TestCareerPageProcessorJobPostingJSONLDSkipsLLM(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}
	confirmer := &spyConfirmer{result: false} // would reject if consulted

	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            confirmer,
	})

	// Not structurally certain (self-hosted host), but the page carries a
	// schema.org JobPosting JSON-LD block -- the strongest signal, which must
	// bypass the LLM Confirmer and catalogue directly.
	raw := &crawler.RawCareerPage{
		URL:     newURL(t, "https://careers.acme.com/jobs"),
		Content: crawler.Content{Title: "Careers at Acme", JSONLD: []string{`{"@type":"JobPosting"}`}},
		Certain: false,
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if confirmer.calls != 0 {
		t.Errorf("a page carrying JobPosting JSON-LD should skip the LLM, but Confirm was called %d times", confirmer.calls)
	}
	if len(companyRepo.upserted) != 1 || len(careerPageRepo.upserted) != 1 {
		t.Fatalf("want the candidate catalogued: companies=%d careerPages=%d",
			len(companyRepo.upserted), len(careerPageRepo.upserted))
	}
}

func TestCareerPageProcessorCanonicalizesPostingToBoardRoot(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}

	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            &spyConfirmer{result: true},
	})

	// Even if a deeper ATS URL reaches the pool, its Career Page is the board root.
	raw := &crawler.RawCareerPage{
		URL:     newURL(t, "https://boards.greenhouse.io/acme/jobs/1"),
		Content: crawler.Content{Title: "Jobs at Acme"},
		Certain: true,
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if got := careerPageRepo.upserted[0].URL; got != "https://boards.greenhouse.io/acme" {
		t.Errorf("career page URL = %q, want canonical board root https://boards.greenhouse.io/acme", got)
	}
}

func TestCareerPageProcessorConfirmRejectDrops(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}
	confirmer := &spyConfirmer{result: false}

	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            confirmer,
	})

	raw := &crawler.RawCareerPage{
		URL:     newURL(t, "https://acme.com/blog"),
		Content: crawler.Content{Title: "Blog"},
		Certain: false,
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if confirmer.calls != 1 {
		t.Errorf("want Confirm consulted once, got %d", confirmer.calls)
	}
	if len(companyRepo.upserted) != 0 || len(careerPageRepo.upserted) != 0 {
		t.Errorf("rejected candidate must not be persisted: companies=%d careerPages=%d",
			len(companyRepo.upserted), len(careerPageRepo.upserted))
	}
}

func TestCareerPageProcessorConfirmAcceptPersists(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}
	confirmer := &spyConfirmer{result: true}

	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            confirmer,
	})

	raw := &crawler.RawCareerPage{
		URL:     newURL(t, "https://careers.acme.com/jobs"),
		Content: crawler.Content{Title: "Careers at Acme"},
		Certain: false,
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if confirmer.calls != 1 {
		t.Errorf("want Confirm consulted once, got %d", confirmer.calls)
	}
	if len(companyRepo.upserted) != 1 {
		t.Fatalf("want 1 company upserted, got %d", len(companyRepo.upserted))
	}
	company := companyRepo.upserted[0]
	if company.CompanyKey != "acme.com" {
		t.Errorf("self-hosted company key = %q, want acme.com", company.CompanyKey)
	}
	if company.ATSProvider != "" {
		t.Errorf("self-hosted ATSProvider should be empty, got %q", company.ATSProvider)
	}
	if company.DisplayDomain != "acme.com" {
		t.Errorf("self-hosted DisplayDomain = %q, want eTLD+1 acme.com", company.DisplayDomain)
	}
	if company.Name != "Acme" {
		t.Errorf("company name = %q, want Acme", company.Name)
	}
	if len(careerPageRepo.upserted) != 1 {
		t.Fatalf("want 1 career page upserted, got %d", len(careerPageRepo.upserted))
	}
	// Self-hosted keeps its own index URL (no ATS canonicalization).
	if got := careerPageRepo.upserted[0].URL; got != "https://careers.acme.com/jobs" {
		t.Errorf("self-hosted career page URL = %q, want the index URL", got)
	}
}
