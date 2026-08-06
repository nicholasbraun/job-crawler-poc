// Package joblistingprocessor converts raw job listings into structured
// JobListing records and persists them into the Corpus (CorpusRepository).
package joblistingprocessor

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
	"github.com/nicholasbraun/job-crawler-poc/internal/listingid"
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
	// Corpus persists the extracted listings, upserted on their canonical URL
	// identity (ADR-0034).
	Corpus              crawler.CorpusRepository
	JobListingExtractor JobListingExtractor
	// Recorder instruments the LLM extractor (calls, content dedup) for the
	// ADR-0007 measurement. Optional: a nil Recorder records nothing.
	Recorder llmobs.Recorder
	// CompanyNames is the per-run CompanyKey → Company-name snapshot (ADR-0021),
	// resolved at run start. A saved Job Listing's Company is taken from here via the
	// source URL's Owner, discarding the extractor's own company. A nil map or a
	// missing Owner leaves Company empty.
	CompanyNames map[string]string
	// AttributeCareerPage best-matches a crawled posting to the Career Page it was
	// collected under (ADR-0035), keyed on the source URL's Owner (CompanyKey) and the
	// posting URL, so the saved listing carries career_page_id for the crawl-lane
	// refetch and dormancy scope. Optional: nil leaves CareerPageID uuid.Nil.
	AttributeCareerPage func(companyKey, postingURL string) uuid.UUID
	// OnSaved is called once per listing actually persisted -- after a successful
	// Save, so an Extractor Abstain or an extraction/save error never fires it
	// (e.g. the run's saved-listings counter tap, #119). Optional: nil is a no-op.
	OnSaved func(ctx context.Context)
	// DescriptionMaxChars caps the stored Posting Body in runes (DESCRIPTION_MAX_CHARS,
	// default crawler.DefaultDescriptionMaxChars). Deliberately NOT the extractor's
	// prompt window (ADR-0041): that dial tunes local-model latency and must never
	// decide what the Corpus stores. Zero or negative falls back to the default.
	DescriptionMaxChars int
	// SourceHash computes the extraction-cache key over a page's main content
	// (ADR-0035). REQUIRED, and it must be the SAME closure the refetch lane is given
	// — bound to crawler.SourceHash with the extractor's prompt-window cap — so the
	// stamped key is byte-identical to the one a refetch recomputes and an unchanged
	// page skips the model. Deliberately without a fallback: a substituted default
	// would silently re-extract the whole Corpus every Cycle.
	SourceHash func(mainContent string) string
	// CaptureDecision, if non-nil, is called once per completed extraction with the
	// source URL, the extractor's verdict (true = a single job posting was
	// extracted, false = an abstain), and the parsed page Content the extractor and
	// Extract Gate saw. It taps the extract stream to harvest a verdict-tagged page
	// sample for the Extract Gold Set (#116) -- capturing Content lets the gate be
	// replayed against the exact bytes with no re-fetch. It fires for both accepts
	// and abstains but never on an extraction error (no verdict exists). Optional:
	// nil is a no-op.
	CaptureDecision func(ctx context.Context, url string, isJobPosting bool, content any)
}

// JobListingProcessor extracts structured job data from raw crawled pages
// and persists the results. It implements processor.Processor[crawler.RawJobListing].
type JobListingProcessor struct {
	corpus                      crawler.CorpusRepository
	jobListingsProcessedCounter metric.Int64Counter
	jobListingExtractor         JobListingExtractor
	recorder                    llmobs.Recorder
	companyNames                map[string]string
	attributeCareerPage         func(companyKey, postingURL string) uuid.UUID
	onSaved                     func(ctx context.Context)
	descriptionMaxChars         int
	sourceHash                  func(mainContent string) string
	captureDecision             func(ctx context.Context, url string, isJobPosting bool, content any)
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
		corpus:                      cfg.Corpus,
		jobListingsProcessedCounter: jobListingsProcessedCounter,
		jobListingExtractor:         cfg.JobListingExtractor,
		recorder:                    recorder,
		companyNames:                cfg.CompanyNames,
		attributeCareerPage:         cfg.AttributeCareerPage,
		onSaved:                     cfg.OnSaved,
		descriptionMaxChars:         cfg.DescriptionMaxChars,
		sourceHash:                  cfg.SourceHash,
		captureDecision:             cfg.CaptureDecision,
	}
}

// Process extracts structured job listing fields from the raw page content via the
// configured JobListingExtractor, stamps the save-time fields the model does not
// decide — company attribution, career page, country, Corpus identity, discovered
// depth, and (ADR-0041) the Posting Body with its Description Source plus the
// extraction-cache key, both derived from the page itself — saves the result,
// increments the processed counter, and fires OnSaved. When the extractor abstains
// (the page is not a single job posting) the extraction is discarded, not saved,
// OnSaved does not fire, and the call is recorded as an abstain; Process still
// returns nil so the durable extract stream acks the task (an abstain is a completed
// decision, not a failure to retry). Returns an error only when extraction or
// persistence fails.
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

	// Capture tap (#116): emit the source URL + verdict for the Extract Gold Set
	// harvest. Placed after the error return so only completed extractions (a real
	// verdict) are sampled, and before the abstain short-circuit so abstains are
	// captured too -- the negative stratum the gate's precision is measured against.
	if w.captureDecision != nil {
		w.captureDecision(ctx, workload.URL.RawURL, extraction.IsJobPosting, &workload.Content)
	}

	if !extraction.IsJobPosting {
		slog.Info("extractor abstained: page is not a single job posting", "url", workload.URL.RawURL)
		return nil // an abstain is a completed decision -- ack, do not retry or dead-letter
	}

	// Attribute the listing to its Owner Company (ADR-0021): overwrite the
	// extractor's company guess with the Catalog name looked up via the source
	// URL's Owner, and persist the Owner as the durable CompanyKey. A nil snapshot
	// or a missing Owner yields "" -- the extractor's guess is discarded either way.
	owner := workload.URL.Owner
	extraction.Listing.CompanyKey = owner
	extraction.Listing.Company = w.companyNames[owner]

	// Attribute the posting to its owning Career Page (ADR-0035) so the saved listing
	// carries career_page_id for the crawl-lane refetch and dormancy scope. A nil hook
	// (Discovery, or an un-scoped run) leaves CareerPageID uuid.Nil.
	if w.attributeCareerPage != nil {
		extraction.Listing.CareerPageID = w.attributeCareerPage(owner, workload.URL.RawURL)
	}

	// Resolve the Country from the LLM's free-text location at save (ADR-0029): the
	// resolver is the sole authority on the ISO code, and Location is left verbatim.
	// An unresolvable location yields the empty Country. The Country is recorded for
	// downstream querying; it no longer gates the save (the Country Constraint died
	// with the Keyword Crawl lane, ADR-0038).
	extraction.Listing.Country = geo.Resolve(extraction.Listing.Location)

	// Stamp the Corpus identity (ADR-0034): the crawl lane keys on the canonicalized
	// source URL. SourceID stays empty (crawl lane).
	extraction.Listing.Source = crawler.SourceLaneCrawl
	extraction.Listing.CanonicalURL = listingid.FromURL(workload.URL.RawURL)

	// Record the crawl depth of the source page (instrumentation only, migration 0024):
	// lets us tune the frontier maxDepth cap from where postings are actually found.
	extraction.Listing.DiscoveredDepth = workload.URL.Depth

	// Store the posting's OWN text (ADR-0041): the Posting Body derived from the page
	// the crawler already downloaded — its lone structured-data posting's description,
	// else its main content — capped by DESCRIPTION_MAX_CHARS, with the marker saying
	// which branch produced it. Both overwrite whatever the extractor returned:
	// transcription is not a judgment task, and provenance is a save-time fact about
	// the pipeline, never something the model may assert.
	extraction.Listing.Description, extraction.Listing.DescriptionSource =
		crawler.PostingBody(&workload.Content, w.descriptionMaxChars)

	// Stamp the extraction-cache key (ADR-0035) from the page's main content, capped
	// by the same prompt window the refetch lane hashes with, so an unchanged page is
	// confirmed alive with no model call. SourceHash caps internally, so this is
	// byte-identical to the extractor's historical stamp over the already-capped text.
	extraction.Listing.SourceHash = w.sourceHash(workload.Content.MainContent)

	if err := w.corpus.Save(ctx, &extraction.Listing); err != nil {
		return fmt.Errorf("job_listing_processor: error saving processed job listing %v: %w", *workload, err)
	}

	w.jobListingsProcessedCounter.Add(ctx, 1)
	if w.onSaved != nil {
		w.onSaved(ctx)
	}
	return nil
}
