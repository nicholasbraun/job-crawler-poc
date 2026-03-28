package crawler

import (
	"context"
)

type JobListing struct {
	URL       string
	Title     string
	Company   string
	Location  string
	TechStack []string
}

type RawJobListing struct {
	URL     URL
	Content Content
}

type JobListingRepository interface {
	Save(ctx context.Context, jobListing *JobListing) error
	Find(ctx context.Context) ([]*JobListing, error)
}
