package crawler

import (
	"context"
)

type Job struct {
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

type JobRepository interface {
	Save(ctx context.Context, job *Job) error
	Find(ctx context.Context) ([]*Job, error)
}
