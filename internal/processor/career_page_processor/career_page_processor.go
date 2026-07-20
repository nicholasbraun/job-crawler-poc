// Package careerpageprocessor is the discovery crawl's Catalog writer: for each
// RawCareerPage candidate it confirms (via LLM unless the candidate is a
// structurally-certain ATS board root), derives ATS-aware Company identity, and
// upserts the Company and its Career Page into the Catalog.
package careerpageprocessor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// Verdict is the Confirmer's combined answer: whether the candidate is a Career
// Page, plus the employer name the LLM read from the page. CompanyName is "" when
// the page does not unambiguously name its employer; the Name Ladder treats "" as
// abstain (ADR-0025). It is a display-only label and never affects Company identity.
type Verdict struct {
	IsCareerPage bool
	CompanyName  string
}

// Confirmer decides whether a gate-passing candidate is really a Career Page.
// Implementations may call an external service (e.g. an LLM API); it is
// consulted for every candidate that is not a structurally-certain ATS board
// root.
type Confirmer interface {
	Confirm(ctx context.Context, url string, content *crawler.Content) (Verdict, error)
}

type Config struct {
	CompanyRepository    crawler.CompanyRepository
	CareerPageRepository crawler.CareerPageRepository
	Confirmer            Confirmer
	// Recorder instruments the LLM stage (calls, gate skips, content dedup) for
	// the ADR-0007 measurement. Optional: a nil Recorder records nothing.
	Recorder llmobs.Recorder
}

// CareerPageProcessor confirms and catalogues career-page candidates. It
// implements processor.Processor[crawler.RawCareerPage].
type CareerPageProcessor struct {
	companyRepository       crawler.CompanyRepository
	careerPageRepository    crawler.CareerPageRepository
	confirmer               Confirmer
	recorder                llmobs.Recorder
	careerPagesFoundCounter metric.Int64Counter
}

func NewProcessor(cfg *Config) *CareerPageProcessor {
	meter := otel.Meter("career_page_processor")
	name := "crawler.career_pages.cataloged"
	careerPagesFoundCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("career_page_processor: error setting up metrics", "err", err, "name", name)
	}

	recorder := cfg.Recorder
	if recorder == nil {
		recorder = llmobs.Nop()
	}

	return &CareerPageProcessor{
		companyRepository:       cfg.CompanyRepository,
		careerPageRepository:    cfg.CareerPageRepository,
		confirmer:               cfg.Confirmer,
		recorder:                recorder,
		careerPagesFoundCounter: careerPagesFoundCounter,
	}
}

// Process confirms the candidate is a career page (skipping the LLM only for a
// structurally-certain ATS board root), then upserts the owning Company and the
// Career Page. A candidate the Confirmer rejects is dropped.
func (w *CareerPageProcessor) Process(ctx context.Context, raw *crawler.RawCareerPage) error {
	// Only a structurally-confirmed ATS board root (raw.Certain) bypasses the LLM
	// Confirmer. Everything else -- including a page carrying a schema.org
	// JobPosting JSON-LD, which marks a single posting, not a hub -- must clear
	// the Confirmer first, keeping single postings and non-openings sub-pages out
	// of the Catalog. The pre-LLM gate already sheds aggregator hosts and reject
	// paths, bounding LLM cost at perpetual discovery scale.
	var llmName string
	if raw.Certain {
		w.recorder.Gated(ctx, llmobs.KindClassify, llmobs.ReasonCertain)
	} else {
		w.recorder.Content(ctx, llmobs.KindClassify, raw.Content.MainContent)
		start := time.Now()
		verdict, err := w.confirmer.Confirm(ctx, raw.URL.RawURL, &raw.Content)
		w.recorder.Call(ctx, llmobs.KindClassify, llmobs.Classify(err), time.Since(start))
		if err != nil {
			return fmt.Errorf("career_page_processor: error confirming career page %s: %w", raw.URL.RawURL, err)
		}
		if !verdict.IsCareerPage {
			slog.Info("career_page_processor: candidate rejected by confirmer", "url", raw.URL.RawURL)
			return nil
		}
		llmName = verdict.CompanyName
	}

	identity := catalog.Identify(raw.URL)
	name, source := deriveName(&raw.Content, identity, llmName)

	company := &crawler.Company{
		CompanyKey:    identity.CompanyKey,
		ATSProvider:   identity.ATSProvider,
		DisplayDomain: companyDomain(identity, &raw.Content),
		Name:          name,
		NameSource:    source,
	}
	if err := w.companyRepository.Upsert(ctx, company); err != nil {
		return fmt.Errorf("career_page_processor: error upserting company %s: %w", identity.CompanyKey, err)
	}

	// Pagination and posting variants fold to one stored row per Company: ATS
	// URLs collapse to the tenant board root, self-hosted pages keep their own
	// index URL, and the result is canonicalised (https, no query, no trailing
	// slash) so twins meet under UNIQUE(company_id, url) and fuzzer query
	// strings never persist.
	careerURL := catalog.StoredCareerPageURL(raw.URL)

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
