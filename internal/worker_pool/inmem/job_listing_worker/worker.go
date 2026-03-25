package joblistingworker

import (
	"context"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	workerpool "github.com/nicholasbraun/job-crawler-poc/internal/worker_pool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

type Config struct {
	JobRepository crawler.JobRepository
}

type JobListingWorker struct {
	jobRepository               crawler.JobRepository
	jobListingsProcessedCounter metric.Int64Counter
}

var _ workerpool.Worker[crawler.RawJobListing] = &JobListingWorker{}

func NewWorker(cfg *Config) *JobListingWorker {
	meter := otel.Meter("job_listing_worker")
	name := "crawler.job_listings.processed"
	jobListingsProcessedCounter, err := meter.Int64Counter(name)
	if err != nil {
		slog.Error("job_listings_worker: error setting up metrics", "err", err, "name", name)
	}

	return &JobListingWorker{
		jobRepository:               cfg.JobRepository,
		jobListingsProcessedCounter: jobListingsProcessedCounter,
	}
}

func (w *JobListingWorker) Process(ctx context.Context, workload *crawler.RawJobListing) error {
	slog.Info("process workload", "workload", workload)
	job := &crawler.Job{Title: workload.Content.Title, URL: workload.URL.RawURL, Company: "", Location: "", TechStack: nil}
	err := w.jobRepository.Save(ctx, job)
	if err != nil {
		return fmt.Errorf("job_listing_worker: error saving processed job %v: %v", *workload, err)
	}

	w.jobListingsProcessedCounter.Add(ctx, 1)
	return nil
}
