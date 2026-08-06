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

// SourceLane is which of the two acquisition paths collected a Job Listing
// (ADR-0034/CONTEXT "Source Lane"): an ATS Fetch or a Crawl. It decides how the
// listing's Liveness is later judged and is stored in job_listing.source.
type SourceLane string

const (
	SourceLaneATS   SourceLane = "ats"
	SourceLaneCrawl SourceLane = "crawl"
)

// JobListing holds the structured data of a single job posting. It is populated
// by a JobListingExtractor (crawl lane) or an ATS BoardFetcher (ATS Fetch lane,
// ADR-0022) and persisted via CorpusRepository. JSON tags are used for LLM
// response unmarshaling, not for API serialization.
type JobListing struct {
	// URL is the source page this listing was extracted from. Not populated
	// by JSON unmarshaling — set explicitly after extraction.
	URL   string
	Title string `json:"title"`
	// Description is the Posting Body (ADR-0041 / CONTEXT "Posting Body"): the
	// posting's OWN text, never model-authored. The save processor derives it from
	// the page the crawler already downloaded (crawler.PostingBody) on the crawl
	// lane, and each provider mapper takes it from the board API on the ATS lane.
	// The json:"-" tag keeps LLM-response unmarshaling from ever reaching it, so a
	// model that still emits a description field cannot leak a summary in.
	Description string `json:"-"`
	// DescriptionSource records where Description came from (ADR-0041 / CONTEXT
	// "Description Source"): the closed enum in posting_body.go, stamped at save by
	// whichever lane produced the text — structured_data or page_content from
	// crawler.PostingBody on the crawl lane, ats_board on an ATS Fetch. The legacy
	// llm_summary value marks a body written before ADR-0041; no code path writes it
	// any more. The json:"-" tag keeps LLM-response unmarshaling from ever reaching
	// it, so a hallucinated marker can never create a silent third state. Stored in
	// job_listing.description_source.
	DescriptionSource DescriptionSource `json:"-"`
	Company           string            `json:"company"`
	// CompanyKey is the Owner CompanyKey (ADR-0021) the saved listing is attributed
	// to. The processor sets it from the source URL's Owner at save time; the
	// json:"-" tag keeps the extractor's LLM-response unmarshaling from ever
	// reaching it, so a hallucinated key can never leak in. Empty for a listing
	// with no resolved Owner.
	CompanyKey string `json:"-"`
	Location   string `json:"location"`
	// Country is the ISO 3166-1 alpha-2 Country the listing's raw Location resolves
	// to via the deterministic Country Resolver (ADR-0029), uppercase and empty when
	// the location cannot be placed (kept, never dropped). Both lanes set it at save
	// from Location (crawl lane) or the provider hint (ATS lane); the LLM never emits
	// it, so no json tag drives unmarshaling into it. Raw Location is left unchanged.
	Country string `json:"-"`
	// CountryHint is the provider's structured country signal on the ATS lane — an
	// ISO code (Recruitee country_code) or a country name (SmartRecruiters/Workable
	// country, Ashby addressCountry) — that the ingest processor feeds the Resolver
	// in preference to the composed Location (ADR-0029). Transient: never persisted,
	// and the json:"-" tag keeps LLM-response unmarshaling from ever reaching it
	// (mirrors Department/FirstPublished).
	CountryHint string `json:"-"`
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
	// CanonicalURL is the listing's stable Corpus identity (ADR-0034): the value
	// job_listing is keyed and deduplicated on. Set by the save processor via the
	// listingid package — listingid.FromURL on the crawl lane, listingid.FromATS on
	// the ATS lane. Never populated by the LLM.
	CanonicalURL string `json:"-"`
	// Source is the Source Lane that collected this listing (ADR-0034). Set by the
	// save processor. Stored in job_listing.source ('ats'|'crawl').
	Source SourceLane `json:"-"`
	// SourceID is the ATS provider's stable posting id, populated by the BoardFetcher
	// on the ATS lane and folded into the CanonicalURL via listingid.FromATS so a URL
	// re-slug never forges a new posting (ADR-0034). Empty on the crawl lane.
	SourceID string `json:"-"`
	// SourceHash is the SHA-256 (hex) of the page's MainContent capped to the
	// extractor's prompt window — the extraction-cache key the later crawl-lane
	// refetch pass compares against (ADR-0035), replacing the vestigial output
	// content_hash. Set by the SAVE PROCESSOR on the crawl lane, from the same cap the
	// refetch lane hashes with, so the stored key stays byte-identical to the one a
	// refetch recomputes; empty on the ATS lane (absence-from-board liveness).
	SourceHash string `json:"-"`
	// CareerPageID links the listing to the Career Page it was collected under (FK to
	// career_page). uuid.Nil when unknown (stored as SQL NULL). Not populated in #187 —
	// a later collection ticket threads it from the seed.
	CareerPageID uuid.UUID `json:"-"`
	// DiscoveredDepth is the crawl depth (hops from the seed) of the source page this
	// listing was extracted from — pure instrumentation for tuning the frontier maxDepth
	// cap from real save-depths. Set by the save processor from the source URL's Depth on
	// the crawl lane only; the ATS Fetch lane leaves it 0 and it persists as SQL NULL
	// there (no crawl depth applies). Captured once at first save and preserved across
	// re-saves. Never populated by the LLM.
	DiscoveredDepth int `json:"-"`
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

// CorpusRepository persists Job Listings into the global, deduplicated Corpus
// (ADR-0034 / CONTEXT "Corpus").
type CorpusRepository interface {
	// Save upserts jl into the Corpus keyed on jl.CanonicalURL: re-saving the same
	// posting refreshes its mutable fields in place, preserves first_seen, advances
	// last_seen, and reopens it (clears closed_at) if it had been closed (ADR-0035).
	// It stamps the Source Lane, source_id, source_hash, career_page_id, and
	// description_source. Every caller MUST set a DescriptionSource: the column's
	// CHECK rejects an unset marker rather than admitting a silent third state that
	// the later refetch heal would skip and the Corpus audit would not count.
	Save(ctx context.Context, jl *JobListing) error
}

// CorpusLivenessRepository is the set of Corpus operations a Collection Cycle drives
// to keep Job Listing Liveness current (ADR-0035): the ATS absence-sweep, the
// crawl-lane refetch application, and the query of a board's Open listings. It is
// implemented by the same store as CorpusRepository; kept separate so the extraction
// pipeline (which only Saves) depends on the narrower port.
type CorpusLivenessRepository interface {
	// ListOpen returns every currently-Open (closed_at IS NULL) Job Listing collected
	// under careerPageID, so a Cycle can refetch them for liveness. Each carries
	// CanonicalURL, URL, Source, SourceID, SourceHash, CompanyKey, DescriptionSource,
	// and CareerPageID (the fields a refetch needs — CompanyKey rebuilds the
	// re-extraction's Owner attribution, and the Description Source lets the refetch
	// heal decide which stored bodies are legacy without a second query per listing).
	// The description TEXT is deliberately not projected: the heal rewrites the body
	// from the page it already fetched, so reading every Open body would be a large
	// pointless read. Never returns nil (an empty board yields an empty slice).
	ListOpen(ctx context.Context, careerPageID uuid.UUID) ([]*JobListing, error)

	// CloseAbsent runs the ATS-lane absence-sweep for ONE board (ADR-0035): when
	// boardComplete, it Closes every Open ATS listing under careerPageID whose last_seen
	// predates notSeenSince (not seen in this Cycle's complete fetch); when boardComplete
	// is false it closes nothing (a partial/failed fetch must never mass-close a board).
	// Scoped strictly to careerPageID — never the whole Company. Returns the count closed.
	CloseAbsent(ctx context.Context, careerPageID uuid.UUID, notSeenSince time.Time, boardComplete bool) (int, error)

	// ApplyCrawlProbe applies one crawl-lane refetch Outcome to the listing keyed on
	// canonicalURL via the pure NextLiveness reducer: Alive advances last_seen and clears
	// the streak; Dead closes it; Inconclusive increments the streak and closes only once
	// it reaches staleThreshold. Attempt-gated by construction — only a probed listing is
	// touched, so a down collector closes nothing. Returns the resulting LifecycleState.
	ApplyCrawlProbe(ctx context.Context, canonicalURL string, outcome ProbeOutcome, staleThreshold int) (LifecycleState, error)
}
