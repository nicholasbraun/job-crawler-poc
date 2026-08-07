// Package freeextraction turns a page that publishes its own posting as structured
// data into a Job Listing with NO model call -- a Free Extraction (ADR-0042 /
// CONTEXT "Free Extraction").
//
// It is a decorator over the job-listing extractor port: it fires only where the
// page's structured data is UNAMBIGUOUS -- exactly one JobPosting node, no openings
// index, and a non-empty title -- and hands every other page to the extractor it
// wraps, unchanged. That condition is the exact complement of the openings-index
// shape the Extract Gate rejects, and it is read through the one shared domain
// function (crawler.LonePosting), so the gate, the Posting Body derivation and this
// package cannot drift into three answers about one page.
//
// Presence of structured data is NOT the signal; unambiguity is. That is what
// separates this from the #45/#46 career-page regression, which keyed on presence
// and cost that pipeline ~45% precision.
//
// Pure: no model, no network, no database.
package freeextraction

import (
	"context"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	joblistingprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/job_listing_processor"
)

var _ joblistingprocessor.JobListingExtractor = (*Extractor)(nil)

// Extractor satisfies the job-listing extractor port and wraps the model-backed
// one, so the save processor is unaware of which path produced an extraction beyond
// the Free marker it reads for accounting.
type Extractor struct {
	inner joblistingprocessor.JobListingExtractor
}

// NewExtractor wraps inner -- the model-backed extractor -- with the Free
// Extraction. Wiring inner directly instead restores the model-only path exactly;
// that is what the EXTRACT_FROM_JSONLD kill switch does.
func NewExtractor(inner joblistingprocessor.JobListingExtractor) *Extractor {
	return &Extractor{inner: inner}
}

// Extract reads the Job Listing straight from the page's lone structured-data
// posting -- no model call -- and delegates to the wrapped extractor for every other
// page: no structured data, an openings index (two or more posting nodes, or an
// ItemList wrapping one), an unparseable block, or a posting node with no title. The
// title-less node delegates on purpose: the model may find a title in the prose,
// where a Free Extraction would only ever produce a nameless listing.
//
// It returns JUDGMENT FIELDS ONLY -- that the page is a single posting, plus title,
// location and work arrangement. Description, Description Source and the
// extraction-cache key are stamped by the save processor for BOTH paths (ADR-0041 /
// ADR-0035), which is what makes the two paths' cache keys identical by construction
// and leaves the refetch lane's unchanged-page check unaffected. Company is left
// empty for the same reason: the save processor attributes it from the run's Catalog
// snapshot (ADR-0021).
//
// Work arrangement is whatever the page positively declared; a posting stating
// nothing stays Unspecified and is never guessed Onsite (ADR-0030). Location is the
// posting's composed address, left verbatim for the save-time Country Resolver to
// read (ADR-0029).
//
// A Free Extraction never fails: the only error returned is the wrapped extractor's,
// verbatim.
func (e *Extractor) Extract(ctx context.Context, raw crawler.RawJobListing) (crawler.Extraction, error) {
	posting, ok := crawler.LonePosting(&raw.Content)
	if !ok || posting.Title == "" {
		return e.inner.Extract(ctx, raw)
	}
	slog.Debug("freeextraction: page extracted from its own structured data, no model call",
		"url", raw.URL.RawURL, "title", posting.Title)
	return crawler.Extraction{
		Listing: crawler.JobListing{
			URL:             raw.URL.RawURL,
			Title:           posting.Title,
			Location:        posting.Location,
			WorkArrangement: posting.WorkArrangement,
		},
		IsJobPosting: true,
		Free:         true,
	}, nil
}
