package processor

import (
	"context"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type WorkLoad struct {
	Content crawler.Content
	URL     crawler.URL
}

type Processor interface {
	Process(ctx context.Context, workload WorkLoad) error
	Close()
}
