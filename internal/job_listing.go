package crawler

import (
	"context"
)

// JobListing holds the structured data extracted from a single job posting page.
// It is populated by a JobListingExtractor and persisted via JobListingRepository.
// JSON tags are used for LLM response unmarshaling, not for API serialization.
type JobListing struct {
	// URL is the source page this listing was extracted from. Not populated
	// by JSON unmarshaling — set explicitly after extraction.
	URL         string
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Company     string   `json:"company"`
	Location    string   `json:"location"`
	// Remote indicates whether the position is available for remote work.
	Remote      bool     `json:"remote"`
	TechStack   []string `json:"tech_stack"`
}

// RawJobListing pairs a crawled URL with its parsed page content before
// any structured extraction (company, location, tech stack) has occurred.
type RawJobListing struct {
	URL     URL
	Content Content
}

type JobListingRepository interface {
	Save(ctx context.Context, jobListing *JobListing) error
	Find(ctx context.Context) ([]*JobListing, error)
}
