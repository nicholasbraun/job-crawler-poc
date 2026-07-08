// Package careerpageprocessor is the discovery crawl's Catalog writer: for each
// RawCareerPage candidate it confirms (via LLM only when no JobPosting JSON-LD
// is present), derives ATS-aware Company identity, and upserts the Company and
// its Career Page into the Catalog.
package careerpageprocessor

import (
	"context"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Confirmer decides whether a gate-passing candidate is really a Career Page.
// Implementations may call an external service (e.g. an LLM API); it is
// consulted only for candidates lacking a JobPosting JSON-LD.
type Confirmer interface {
	Confirm(ctx context.Context, url string, content *crawler.Content) (bool, error)
}

type Config struct {
	CompanyRepository    crawler.CompanyRepository
	CareerPageRepository crawler.CareerPageRepository
	Confirmer            Confirmer
}

// CareerPageProcessor confirms and catalogues career-page candidates. It
// implements processor.Processor[crawler.RawCareerPage].
type CareerPageProcessor struct {
	companyRepository       crawler.CompanyRepository
	careerPageRepository    crawler.CareerPageRepository
	confirmer               Confirmer
	careerPagesFoundCounter metric.Int64Counter
}

func NewProcessor(cfg *Config) *CareerPageProcessor {
	meter := otel.Meter("career_page_processor")
	name := "crawler.career_pages.cataloged"
	careerPagesFoundCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("career_page_processor: error setting up metrics", "err", err, "name", name)
	}

	return &CareerPageProcessor{
		companyRepository:       cfg.CompanyRepository,
		careerPageRepository:    cfg.CareerPageRepository,
		confirmer:               cfg.Confirmer,
		careerPagesFoundCounter: careerPagesFoundCounter,
	}
}

// Process confirms the candidate is a career page (skipping the LLM when the
// page already carries a JobPosting JSON-LD), then upserts the owning Company
// and the Career Page. A candidate the Confirmer rejects is dropped.
func (w *CareerPageProcessor) Process(ctx context.Context, raw *crawler.RawCareerPage) error {
	// Two arms bypass the LLM Confirmer: a structurally-confirmed ATS board root
	// (raw.Certain), and any page already carrying a schema.org JobPosting
	// JSON-LD block (the strongest possible signal). Only a content-heuristic
	// match on an unrecognized host with no such structured data must clear the
	// LLM first, bounding LLM cost at perpetual discovery scale.
	if !raw.Certain && !hasJobPostingJSONLD(raw.Content.JSONLD) {
		ok, err := w.confirmer.Confirm(ctx, raw.URL.RawURL, &raw.Content)
		if err != nil {
			return fmt.Errorf("career_page_processor: error confirming career page %s: %w", raw.URL.RawURL, err)
		}
		if !ok {
			slog.Info("career_page_processor: candidate rejected by confirmer", "url", raw.URL.RawURL)
			return nil
		}
	}

	identity := catalog.Identify(raw.URL)

	company := &crawler.Company{
		CompanyKey:    identity.CompanyKey,
		ATSProvider:   identity.ATSProvider,
		DisplayDomain: companyDomain(identity, &raw.Content),
		Name:          companyName(raw.Content.Title, nameFallback(identity)),
	}
	if err := w.companyRepository.Upsert(ctx, company); err != nil {
		return fmt.Errorf("career_page_processor: error upserting company %s: %w", identity.CompanyKey, err)
	}

	// Collapse pagination and posting variants to one canonical Career Page per
	// Company on a known ATS; self-hosted pages keep their own index URL.
	careerURL := raw.URL.RawURL
	if canonical, ok := catalog.CareerPageURL(raw.URL); ok {
		careerURL = canonical
	}

	careerPage := &crawler.CareerPage{
		CompanyID:        company.ID,
		URL:              careerURL,
		PolitenessDomain: identity.PolitenessDomain,
	}
	if err := w.careerPageRepository.Upsert(ctx, careerPage); err != nil {
		return fmt.Errorf("career_page_processor: error upserting career page %s: %w", raw.URL.RawURL, err)
	}

	w.careerPagesFoundCounter.Add(ctx, 1)
	return nil
}
