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
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"go.opentelemetry.io/otel"
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
}

// JobListingProcessor extracts structured job data from raw crawled pages
// and persists the results. It implements processor.Processor[crawler.RawJobListing].
type JobListingProcessor struct {
	jobListingRepository        crawler.JobListingRepository
	jobListingsProcessedCounter metric.Int64Counter
	jobListingExtractor         JobListingExtractor
	recorder                    llmobs.Recorder
	definitionID                uuid.UUID
}

func NewProcessor(cfg *Config) *JobListingProcessor {
	meter := otel.Meter("job_listing_processor")
	name := "crawler.job_listings.processed"
	jobListingsProcessedCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("job_listing_processor: error setting up metrics", "err", err, "name", name)
	}

	recorder := cfg.Recorder
	if recorder == nil {
		recorder = llmobs.Nop()
	}

	return &JobListingProcessor{
		jobListingRepository:        cfg.JobListingRepository,
		jobListingsProcessedCounter: jobListingsProcessedCounter,
		jobListingExtractor:         cfg.JobListingExtractor,
		recorder:                    recorder,
		definitionID:                cfg.DefinitionID,
	}
}

// Process extracts structured job listing fields from the raw page content via
// the configured JobListingExtractor, saves the result, and increments the
// processed counter. When the extractor abstains (the page is not a single job
// posting) the extraction is discarded, not saved, and the call is recorded as an
// abstain; Process still returns nil so the durable extract stream acks the task
// (an abstain is a completed decision, not a failure to retry). Returns an error
// only when extraction or persistence fails.
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

	if err := w.jobListingRepository.Save(ctx, w.definitionID, &extraction.Listing); err != nil {
		return fmt.Errorf("job_listing_processor: error saving processed job listing %v: %w", *workload, err)
	}

	w.jobListingsProcessedCounter.Add(ctx, 1)
	return nil
}
