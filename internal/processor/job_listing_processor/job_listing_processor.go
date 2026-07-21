// Package joblistingprocessor converts raw job listings into structured
// JobListing records and persists them via the JobListingRepository.
package joblistingprocessor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// JobListingExtractor converts a RawJobListing (URL + parsed HTML content)
// into a structured JobListing with extracted fields like company, location,
// and tech stack. Implementations may call external services (e.g. an LLM API).
// The returned Extraction also carries the extractor's verdict on whether the
// page is a single job posting; a false verdict is an Extractor Abstain.
type JobListingExtractor interface {
	Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.Extraction, error)
}

type Config struct {
	JobListingRepository crawler.JobListingRepository
	JobListingExtractor  JobListingExtractor
	// DefinitionID identifies the crawl definition whose run produced these
	// listings; it is passed through to Save as the listing key's first half.
	DefinitionID uuid.UUID
	// Recorder instruments the LLM extractor (calls, content dedup) for the
	// ADR-0007 measurement. Optional: a nil Recorder records nothing.
	Recorder llmobs.Recorder
	// CompanyNames is the per-run CompanyKey → Company-name snapshot (ADR-0021),
	// resolved at run start. A saved Job Listing's Company is taken from here via the
	// source URL's Owner, discarding the extractor's own company. A nil map or a
	// missing Owner leaves Company empty.
	CompanyNames map[string]string
	// Countries is the definition's Country Constraint (ADR-0028): the set of target
	// ISO alpha-2 codes to keep listings for. Empty (or nil) keeps every Country.
	Countries []string
	// OnSaved is called once per listing actually persisted -- after a successful
	// Save, so an Extractor Abstain or an extraction/save error never fires it
	// (e.g. the run's saved-listings counter tap, #119). Optional: nil is a no-op.
	OnSaved func(ctx context.Context)
}

// JobListingProcessor extracts structured job data from raw crawled pages
// and persists the results. It implements processor.Processor[crawler.RawJobListing].
type JobListingProcessor struct {
	jobListingRepository        crawler.JobListingRepository
	jobListingsProcessedCounter metric.Int64Counter
	droppedByCountryCounter     metric.Int64Counter
	jobListingExtractor         JobListingExtractor
	recorder                    llmobs.Recorder
	definitionID                uuid.UUID
	companyNames                map[string]string
	countries                   []string
	onSaved                     func(ctx context.Context)
}

func NewProcessor(cfg *Config) *JobListingProcessor {
	meter := otel.Meter("job_listing_processor")
	name := "crawler.job_listings.processed"
	jobListingsProcessedCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("job_listing_processor: error setting up metrics", "err", err, "name", name)
	}
	droppedName := "crawler.job_listings.dropped_by_country"
	droppedByCountryCounter, err := meter.Int64Counter(droppedName)
	if err != nil {
		slog.Error("job_listing_processor: error setting up metrics", "err", err, "name", droppedName)
	}

	recorder := cfg.Recorder
	if recorder == nil {
		recorder = llmobs.Nop()
	}

	return &JobListingProcessor{
		jobListingRepository:        cfg.JobListingRepository,
		jobListingsProcessedCounter: jobListingsProcessedCounter,
		droppedByCountryCounter:     droppedByCountryCounter,
		jobListingExtractor:         cfg.JobListingExtractor,
		recorder:                    recorder,
		definitionID:                cfg.DefinitionID,
		companyNames:                cfg.CompanyNames,
		countries:                   cfg.Countries,
		onSaved:                     cfg.OnSaved,
	}
}

// Process extracts structured job listing fields from the raw page content via
// the configured JobListingExtractor, saves the result, increments the processed
// counter, and fires OnSaved. When the extractor abstains (the page is not a single
// job posting) the extraction is discarded, not saved, OnSaved does not fire, and
// the call is recorded as an abstain; Process still returns nil so the durable
// extract stream acks the task (an abstain is a completed decision, not a failure
// to retry). Returns an error only when extraction or persistence fails.
func (w *JobListingProcessor) Process(ctx context.Context, workload *crawler.RawJobListing) error {
	slog.Info("process job listing", "url", workload.URL.RawURL)
	w.recorder.Content(ctx, llmobs.KindExtract, workload.Content.MainContent)
	start := time.Now()
	extraction, err := w.jobListingExtractor.Extract(ctx, *workload)
	// Classify err first: on the error path extraction is the zero value
	// (IsJobPosting=false), so the err==nil guard keeps a failed call out of the
	// abstain bucket.
	outcome := llmobs.Classify(err)
	if err == nil && !extraction.IsJobPosting {
		outcome = llmobs.OutcomeAbstain
	}
	w.recorder.Call(ctx, llmobs.KindExtract, outcome, time.Since(start))
	if err != nil {
		return fmt.Errorf("job_listing_processor: error extracting job listing %v: %w", *workload, err)
	}

	if !extraction.IsJobPosting {
		slog.Info("extractor abstained: page is not a single job posting", "url", workload.URL.RawURL)
		return nil // an abstain is a completed decision -- ack, do not retry or dead-letter
	}

	// Attribute the listing to its Owner Company (ADR-0021): overwrite the
	// extractor's company guess with the Catalog name looked up via the source
	// URL's Owner, and persist the Owner as the durable CompanyKey. A nil snapshot
	// or a missing Owner yields "" -- the extractor's guess is discarded either way.
	owner := workload.URL.Owner
	extraction.Listing.CompanyKey = owner
	extraction.Listing.Company = w.companyNames[owner]

	// Resolve the Country from the LLM's free-text location at save (ADR-0029): the
	// resolver is the sole authority on the ISO code, and Location is left verbatim.
	// An unresolvable location yields the empty Country, which is kept (ADR-0028).
	extraction.Listing.Country = geo.Resolve(extraction.Listing.Location)

	// Country Constraint gate (ADR-0028): discard the listing before persistence
	// unless it passes the definition's target Countries. A discard is a completed
	// decision -- no Save, no processed/OnSaved counter -- so return nil (parity with
	// the Extractor Abstain above), never an error the durable stream would retry.
	if !crawler.KeepForCountry(w.countries, extraction.Listing.Country, extraction.Listing.WorkArrangement) {
		slog.Info("dropped by country constraint", "url", workload.URL.RawURL, "country", extraction.Listing.Country)
		w.droppedByCountryCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("lane", "crawl")))
		return nil
	}

	if err := w.jobListingRepository.Save(ctx, w.definitionID, &extraction.Listing); err != nil {
		return fmt.Errorf("job_listing_processor: error saving processed job listing %v: %w", *workload, err)
	}

	w.jobListingsProcessedCounter.Add(ctx, 1)
	if w.onSaved != nil {
		w.onSaved(ctx)
	}
	return nil
}
