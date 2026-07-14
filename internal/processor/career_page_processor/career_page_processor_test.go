package careerpageprocessor_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
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

func (r *spyCareerPageRepo) FirstSeenByDay(ctx context.Context) ([]crawler.DayCount, error) {
	return nil, nil
}

type spyConfirmer struct {
	calls  int
	result bool
}

func (c *spyConfirmer) Confirm(ctx context.Context, url string, content *crawler.Content) (bool, error) {
	c.calls++
	return c.result, nil
}

type recordedCall struct {
	kind    llmobs.Kind
	outcome llmobs.Outcome
}

type recordedGate struct {
	kind   llmobs.Kind
	reason llmobs.Reason
}

// spyRecorder captures which LLM-stage events a processor records.
type spyRecorder struct {
	calls   []recordedCall
	gates   []recordedGate
	content int
}

func (s *spyRecorder) Call(_ context.Context, k llmobs.Kind, o llmobs.Outcome, _ time.Duration) {
	s.calls = append(s.calls, recordedCall{k, o})
}
func (s *spyRecorder) Gated(_ context.Context, k llmobs.Kind, r llmobs.Reason) {
	s.gates = append(s.gates, recordedGate{k, r})
}
func (s *spyRecorder) Content(_ context.Context, _ llmobs.Kind, _ string) { s.content++ }
func (s *spyRecorder) Retry(context.Context, llmobs.Kind)                 {}
func (s *spyRecorder) DeadLetter(context.Context, llmobs.Kind)            {}
func (s *spyRecorder) QueueDepth(context.Context, llmobs.Kind, int64, int64) {
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

func TestCareerPageProcessorJobPostingJSONLDStillConsultsLLM(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}
	// A JobPosting JSON-LD marks a single posting, not a hub, so the confirmer
	// rejects it -- and the candidate must be dropped, not catalogued.
	confirmer := &spyConfirmer{result: false}

	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            confirmer,
	})

	// Not structurally certain (self-hosted host) but carrying a schema.org
	// JobPosting JSON-LD. This used to bypass the LLM and catalogue directly --
	// the #45 root cause -- so the JSON-LD must no longer short-circuit the
	// confirmer.
	raw := &crawler.RawCareerPage{
		URL:     newURL(t, "https://careers.acme.com/o/senior-go"),
		Content: crawler.Content{Title: "Senior Go Engineer", JSONLD: []string{`{"@type":"JobPosting"}`}},
		Certain: false,
	}
	if err := proc.Process(t.Context(), raw); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if confirmer.calls != 1 {
		t.Errorf("a page carrying JobPosting JSON-LD must still reach the LLM, but Confirm was called %d times", confirmer.calls)
	}
	if len(companyRepo.upserted) != 0 || len(careerPageRepo.upserted) != 0 {
		t.Fatalf("a confirmer-rejected JSON-LD posting must not be catalogued: companies=%d careerPages=%d",
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

func TestCareerPageProcessorCanonicalizesStoredURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"http twin coerced to https", "http://careers.acme.com/jobs", "https://careers.acme.com/jobs"},
		{"query string stripped", "https://careers.acme.com/careers?p=6774", "https://careers.acme.com/careers"},
		{"root slash stripped", "https://careers.acme.com/", "https://careers.acme.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			companyRepo := &spyCompanyRepo{assignID: uuid.New()}
			careerPageRepo := &spyCareerPageRepo{}
			proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
				CompanyRepository:    companyRepo,
				CareerPageRepository: careerPageRepo,
				Confirmer:            &spyConfirmer{result: true},
			})
			raw := &crawler.RawCareerPage{
				URL:     newURL(t, tt.raw),
				Content: crawler.Content{Title: "Careers at Acme"},
				Certain: false,
			}
			if err := proc.Process(t.Context(), raw); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}
			if len(careerPageRepo.upserted) != 1 {
				t.Fatalf("want 1 career page upserted, got %d", len(careerPageRepo.upserted))
			}
			if got := careerPageRepo.upserted[0].URL; got != tt.want {
				t.Errorf("stored career page URL = %q, want canonical %q", got, tt.want)
			}
		})
	}
}

func TestCareerPageProcessorTwinsCollapseToOneStoredURL(t *testing.T) {
	companyRepo := &spyCompanyRepo{assignID: uuid.New()}
	careerPageRepo := &spyCareerPageRepo{}
	proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
		CompanyRepository:    companyRepo,
		CareerPageRepository: careerPageRepo,
		Confirmer:            &spyConfirmer{result: true},
	})
	// http/https, trailing-slash, and query-string twins of the same hub. The
	// spy repo does not enforce the DB UNIQUE(company_id, url) constraint, so we
	// assert the equivalent invariant: every twin yields the same (CompanyID,
	// URL) pair, so the real constraint would fold them to a single row.
	twins := []string{
		"http://careers.acme.com/careers/",
		"https://careers.acme.com/careers",
		"https://careers.acme.com/careers?p=6774",
	}
	for _, u := range twins {
		raw := &crawler.RawCareerPage{
			URL:     newURL(t, u),
			Content: crawler.Content{Title: "Careers at Acme"},
			Certain: false,
		}
		if err := proc.Process(t.Context(), raw); err != nil {
			t.Fatalf("Process(%q) returned error: %v", u, err)
		}
	}
	first := careerPageRepo.upserted[0]
	for _, cp := range careerPageRepo.upserted {
		if cp.URL != first.URL {
			t.Errorf("twin stored as %q, want all twins as %q", cp.URL, first.URL)
		}
		if cp.CompanyID != first.CompanyID {
			t.Errorf("twin attributed to %v, want %v", cp.CompanyID, first.CompanyID)
		}
	}
	if first.URL != "https://careers.acme.com/careers" {
		t.Errorf("canonical stored URL = %q, want https://careers.acme.com/careers", first.URL)
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

func TestCareerPageProcessorRecordsGateAndCallDecisions(t *testing.T) {
	tests := []struct {
		name        string
		raw         *crawler.RawCareerPage
		confirm     bool
		wantCalls   []recordedCall
		wantGates   []recordedGate
		wantContent int
	}{
		{
			name: "certain gates without an LLM call",
			raw: &crawler.RawCareerPage{
				URL:     newURL(t, "https://boards.greenhouse.io/acme"),
				Content: crawler.Content{Title: "Jobs at Acme", MainContent: "roles"},
				Certain: true,
			},
			wantGates:   []recordedGate{{llmobs.KindClassify, llmobs.ReasonCertain}},
			wantContent: 0,
		},
		{
			name: "job-posting json-ld no longer gates: it reaches the LLM",
			raw: &crawler.RawCareerPage{
				URL:     newURL(t, "https://careers.acme.com/o/senior-go"),
				Content: crawler.Content{Title: "Senior Go Engineer", MainContent: "roles", JSONLD: []string{`{"@type":"JobPosting"}`}},
				Certain: false,
			},
			confirm:     true,
			wantCalls:   []recordedCall{{llmobs.KindClassify, llmobs.OutcomeOK}},
			wantContent: 1,
		},
		{
			name: "uncertain page reaches the LLM and records the call + content",
			raw: &crawler.RawCareerPage{
				URL:     newURL(t, "https://careers.acme.com/jobs"),
				Content: crawler.Content{Title: "Careers at Acme", MainContent: "roles"},
				Certain: false,
			},
			confirm:     true,
			wantCalls:   []recordedCall{{llmobs.KindClassify, llmobs.OutcomeOK}},
			wantContent: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &spyRecorder{}
			proc := careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
				CompanyRepository:    &spyCompanyRepo{assignID: uuid.New()},
				CareerPageRepository: &spyCareerPageRepo{},
				Confirmer:            &spyConfirmer{result: tt.confirm},
				Recorder:             rec,
			})

			if err := proc.Process(t.Context(), tt.raw); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			if len(rec.calls) != len(tt.wantCalls) {
				t.Fatalf("recorded calls = %v, want %v", rec.calls, tt.wantCalls)
			}
			for i, want := range tt.wantCalls {
				if rec.calls[i] != want {
					t.Errorf("call[%d] = %v, want %v", i, rec.calls[i], want)
				}
			}
			if len(rec.gates) != len(tt.wantGates) {
				t.Fatalf("recorded gates = %v, want %v", rec.gates, tt.wantGates)
			}
			for i, want := range tt.wantGates {
				if rec.gates[i] != want {
					t.Errorf("gate[%d] = %v, want %v", i, rec.gates[i], want)
				}
			}
			if rec.content != tt.wantContent {
				t.Errorf("content probes = %d, want %d", rec.content, tt.wantContent)
			}
		})
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
