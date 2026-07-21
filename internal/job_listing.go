package crawler

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// WorkArrangement is a Job Listing's working mode (ADR-0030), replacing the
// former Remote boolean. Unspecified is the honest default: a source that does
// not positively state the mode is never Onsite.
type WorkArrangement string

const (
	WorkArrangementUnspecified WorkArrangement = "unspecified"
	WorkArrangementRemote      WorkArrangement = "remote"
	WorkArrangementOnsite      WorkArrangement = "onsite"
	WorkArrangementHybrid      WorkArrangement = "hybrid"
)

// workArrangementFolder drops the separators providers vary on ("on-site",
// "On_site", "on site"), so NormalizeWorkArrangement folds them all to onsite.
var workArrangementFolder = strings.NewReplacer("-", "", "_", "", " ", "")

// NormalizeWorkArrangement maps a free-form provider/LLM value to a known
// WorkArrangement, folding case and separators ("on-site", "On_site" -> onsite).
// Any unrecognized or empty value degrades to Unspecified — a source that does
// not positively state the mode is never Onsite (ADR-0030).
func NormalizeWorkArrangement(s string) WorkArrangement {
	switch strings.ToLower(workArrangementFolder.Replace(strings.TrimSpace(s))) {
	case "remote":
		return WorkArrangementRemote
	case "onsite":
		return WorkArrangementOnsite
	case "hybrid":
		return WorkArrangementHybrid
	default:
		return WorkArrangementUnspecified
	}
}

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
	// WorkArrangement is the posting's working mode (ADR-0030). On the crawl lane
	// the LLM emits it (validated to the enum); on the ATS lane each provider mapper
	// derives it from its structured signal. The json tag drives LLM-response
	// unmarshaling; API serialization uses the listing DTO, not this tag.
	WorkArrangement WorkArrangement `json:"work_arrangement"`
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
// any structured extraction (company, location) has occurred.
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
