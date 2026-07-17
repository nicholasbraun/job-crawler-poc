package crawler

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// JobListing holds the structured data of a single job posting. It is populated
// by a JobListingExtractor (crawl lane) or an ATS BoardFetcher (ATS Fetch lane,
// ADR-0022) and persisted via JobListingRepository. JSON tags are used for LLM
// response unmarshaling, not for API serialization.
type JobListing struct {
	// URL is the source page this listing was extracted from. Not populated
	// by JSON unmarshaling — set explicitly after extraction.
	URL         string
	Title       string `json:"title"`
	Description string `json:"description"`
	Company     string `json:"company"`
	// CompanyKey is the Owner CompanyKey (ADR-0021) the saved listing is attributed
	// to. The processor sets it from the source URL's Owner at save time; the
	// json:"-" tag keeps the extractor's LLM-response unmarshaling from ever
	// reaching it, so a hallucinated key can never leak in. Empty for a listing
	// with no resolved Owner.
	CompanyKey string `json:"-"`
	Location   string `json:"location"`
	// Remote indicates whether the position is available for remote work.
	Remote    bool     `json:"remote"`
	TechStack []string `json:"tech_stack"`
	// Department is the posting's department/team, taken from the provider board
	// API on an ATS Fetch (ADR-0022). Empty for a crawled-and-extracted listing.
	// The json:"-" tag keeps LLM-response unmarshaling from ever reaching it.
	Department string `json:"-"`
	// FirstPublished is when the provider board first published the posting, from
	// the board API on an ATS Fetch (ADR-0022). Zero for a crawled-and-extracted
	// listing or when the board omitted or malformed the timestamp. The json:"-"
	// tag keeps LLM-response unmarshaling from ever reaching it.
	FirstPublished time.Time `json:"-"`
}

// RawJobListing pairs a crawled URL with its parsed page content before
// any structured extraction (company, location, tech stack) has occurred.
type RawJobListing struct {
	URL     URL
	Content Content
}

// Extraction is the transient result of one extractor call: the structured
// JobListing plus the extractor's verdict on whether the page it was handed is a
// single job posting. IsJobPosting is NEVER persisted (ADR-0019) -- it drives the
// save decision and the Empty-Extraction Rate metric only. A false verdict is an
// Extractor Abstain: the Listing is discarded, not saved.
type Extraction struct {
	Listing      JobListing
	IsJobPosting bool
}

type JobListingRepository interface {
	// Save persists jobListing under the crawl definition identified by
	// definitionID. Listings are keyed (definitionID, URL) and upserted:
	// re-saving the same pair refreshes the record in place rather than
	// inserting a duplicate.
	Save(ctx context.Context, definitionID uuid.UUID, jobListing *JobListing) error
	Find(ctx context.Context) ([]*JobListing, error)
	// FindByDefinition returns the listings extracted under definitionID,
	// most-recently-seen first. When keyword is non-empty it is matched
	// case-insensitively against the listing title and description; an empty
	// keyword applies no such filter. It never returns nil; no matches yields
	// an empty slice.
	FindByDefinition(ctx context.Context, definitionID uuid.UUID, keyword string) ([]*JobListing, error)
}
