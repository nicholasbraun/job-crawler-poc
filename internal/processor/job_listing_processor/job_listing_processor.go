// Package joblistingprocessor converts raw job listings into structured
// JobListing records and persists them via the JobListingRepository.
package joblistingprocessor

import (
	"context"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// JobListingExtractor converts a RawJobListing (URL + parsed HTML content)
// into a structured JobListing with extracted fields like company, location,
// and tech stack. Implementations may call external services (e.g. an LLM API).
type JobListingExtractor interface {
	Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.JobListing, error)
}

type Config struct {
	JobListingRepository crawler.JobListingRepository
	JobListingExtractor  JobListingExtractor
}

// JobListingProcessor extracts structured job data from raw crawled pages
// and persists the results. It implements processor.Processor[crawler.RawJobListing].
type JobListingProcessor struct {
	jobListingRepository        crawler.JobListingRepository
	jobListingsProcessedCounter metric.Int64Counter
	jobListingExtractor         JobListingExtractor
}

func NewProcessor(cfg *Config) *JobListingProcessor {
	meter := otel.Meter("job_listing_processor")
	name := "crawler.job_listings.processed"
	jobListingsProcessedCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("job_listing_processor: error setting up metrics", "err", err, "name", name)
	}

	return &JobListingProcessor{
		jobListingRepository:        cfg.JobListingRepository,
		jobListingsProcessedCounter: jobListingsProcessedCounter,
		jobListingExtractor:         cfg.JobListingExtractor,
	}
}

// Process extracts structured job listing fields from the raw page content
// via the configured JobListingExtractor, saves the result, and increments
// the processed counter. Returns an error if extraction or persistence fails.
func (w *JobListingProcessor) Process(ctx context.Context, workload *crawler.RawJobListing) error {
	slog.Info("process job listing", "url", workload.URL.RawURL)
	jobListing, err := w.jobListingExtractor.Extract(ctx, *workload)
	if err != nil {
		return fmt.Errorf("job_listing_processor: error extracting job listing %v: %w", *workload, err)
	}

	err = w.jobListingRepository.Save(ctx, &jobListing)
	if err != nil {
		return fmt.Errorf("job_listing_processor: error saving processed job listing %v: %w", *workload, err)
	}

	w.jobListingsProcessedCounter.Add(ctx, 1)
	return nil
}
