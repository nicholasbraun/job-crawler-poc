package collection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// RefetchConfig groups the crawl-lane refetch processor's dependencies (ADR-0035).
type RefetchConfig struct {
	Downloader downloader.Downloader
	Parser     parser.Parser
	// Liveness applies each per-listing refetch Outcome and is the source of the
	// board's Open listings.
	Liveness crawler.CorpusLivenessRepository
	// Dormancy records the Career-Page reach probe and cascades a close when the
	// page tips dormant.
	Dormancy DormancyRecorder
	// SourceHash computes the extraction-cache key over a page's main content —
	// bound to openrouter.SourceHash with the extractor's ExtractMaxChars so the key
	// is byte-identical to the one the extractor stored (ADR-0035).
	SourceHash func(mainContent string) string
	// EnqueueExtract hands a changed page to the shared extract stage for
	// re-extraction (which re-Saves, reopening and advancing the listing).
	EnqueueExtract func(ctx context.Context, raw *crawler.RawJobListing) error
	// StaleThreshold is the crawl-lane inconclusive-streak backstop
	// (crawler.DefaultCrawlStaleThreshold).
	StaleThreshold int
	// DormancyThreshold is the Career-Page dormancy threshold
	// (crawler.DefaultPageDormancyThreshold).
	DormancyThreshold int
	// OnRefreshed is called once per changed page enqueued for re-extraction. Optional.
	OnRefreshed func(ctx context.Context)
	// OnClosed is called with the listings closed by a dormancy cascade. Optional.
	OnClosed func(ctx context.Context, n int)
}

// RefetchProcessor is the crawl-lane liveness worker (ADR-0035): for one crawled
// Career Page it probes the page for dormancy, then refetches each known-open
// posting to judge its liveness directly — 404/410 closes it, an unchanged 200
// keeps it open with no LLM call (source_hash cache), a changed 200 re-extracts.
// Only listed-open URLs are touched, so a down collector closes nothing
// (attempt-gated by construction). It implements
// processor.Processor[crawler.CollectionSeed].
//
// Soft-404 (a 200 whose body no longer describes a posting) re-extracts and the
// extractor abstains, so the listing stays open on a stale last_seen: an accepted
// v1 gap (deterministic soft-404 detection is out of this ticket's scope).
type RefetchProcessor struct {
	downloader        downloader.Downloader
	parser            parser.Parser
	liveness          crawler.CorpusLivenessRepository
	dormancy          DormancyRecorder
	sourceHash        func(mainContent string) string
	enqueueExtract    func(ctx context.Context, raw *crawler.RawJobListing) error
	staleThreshold    int
	dormancyThreshold int
	onRefreshed       func(ctx context.Context)
	onClosed          func(ctx context.Context, n int)
}

var _ processor.Processor[crawler.CollectionSeed] = (*RefetchProcessor)(nil)

// NewRefetchProcessor builds a crawl-lane refetch processor.
func NewRefetchProcessor(cfg *RefetchConfig) *RefetchProcessor {
	return &RefetchProcessor{
		downloader:        cfg.Downloader,
		parser:            cfg.Parser,
		liveness:          cfg.Liveness,
		dormancy:          cfg.Dormancy,
		sourceHash:        cfg.SourceHash,
		enqueueExtract:    cfg.EnqueueExtract,
		staleThreshold:    cfg.StaleThreshold,
		dormancyThreshold: cfg.DormancyThreshold,
		onRefreshed:       cfg.OnRefreshed,
		onClosed:          cfg.OnClosed,
	}
}

// Process probes page for dormancy, then refetches each of its Open listings. A page
// that tips dormant on this probe closes its listings (via the cascade) and is not
// refetched. Per-listing refetch errors are joined so one bad posting neither drops
// the rest nor aborts the pool.
func (p *RefetchProcessor) Process(ctx context.Context, page *crawler.CollectionSeed) error {
	// 1. Dormancy probe of the page URL. A reachable page is Alive; a 404/410 is Dead;
	//    a transient failure is Inconclusive (never counts toward dormancy).
	_, perr := p.downloader.Get(ctx, page.URL)
	res, derr := p.dormancy.RecordProbe(ctx, page.CareerPageID, classifyStatus(perr), p.dormancyThreshold)
	if derr != nil {
		return fmt.Errorf("collection: recording dormancy probe for %q: %w", page.URL, derr)
	}
	if res.BecameDormant {
		if res.ClosedListings > 0 && p.onClosed != nil {
			p.onClosed(ctx, res.ClosedListings)
		}
		// A page that just went dormant is no longer refetched — its listings are
		// already closed by the cascade.
		return nil
	}

	// 2. Refetch each known-open posting for its own liveness. (Their URLs were seeded
	//    into the walk's visited set at Cycle start, so the walk only surfaces new ones.)
	open, err := p.liveness.ListOpen(ctx, page.CareerPageID)
	if err != nil {
		return fmt.Errorf("collection: listing open for refetch of page %q: %w", page.CareerPageID, err)
	}
	var errs error
	for _, listing := range open {
		if err := p.refetchOne(ctx, listing); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
}

// refetchOne refetches one open posting and applies its Liveness Outcome (ADR-0035):
// a 404/410 closes it (Dead); a transient error is Inconclusive; an unchanged 200
// keeps it open with no LLM call (source_hash matches); a changed 200 is re-enqueued
// for re-extraction (which re-Saves, reopening/advancing it) with no probe.
func (p *RefetchProcessor) refetchOne(ctx context.Context, listing *crawler.JobListing) error {
	resp, err := p.downloader.Get(ctx, listing.URL)
	if err != nil {
		outcome := classifyStatus(err) // Dead (404/410) or Inconclusive
		if _, aerr := p.liveness.ApplyCrawlProbe(ctx, listing.CanonicalURL, outcome, p.staleThreshold); aerr != nil {
			return fmt.Errorf("collection: applying refetch probe for %q: %w", listing.CanonicalURL, aerr)
		}
		return nil
	}

	content, perr := p.parser.Parse(resp.Content)
	if perr != nil {
		// A 200 we cannot parse is inconclusive, not dead.
		slog.Error("collection: refetch parse failed", "url", listing.URL, "err", perr)
		if _, aerr := p.liveness.ApplyCrawlProbe(ctx, listing.CanonicalURL, crawler.ProbeInconclusive, p.staleThreshold); aerr != nil {
			return fmt.Errorf("collection: applying refetch probe for %q: %w", listing.CanonicalURL, aerr)
		}
		return nil
	}

	if p.sourceHash(content.MainContent) == listing.SourceHash {
		// Unchanged source content: confirmed alive with NO LLM call.
		if _, aerr := p.liveness.ApplyCrawlProbe(ctx, listing.CanonicalURL, crawler.ProbeAlive, p.staleThreshold); aerr != nil {
			return fmt.Errorf("collection: applying refetch probe for %q: %w", listing.CanonicalURL, aerr)
		}
		return nil
	}

	// Changed content: re-extract. The re-Save reopens/advances the listing and
	// re-stamps its hash and career_page_id; no probe is applied here.
	raw := &crawler.RawJobListing{
		URL:     crawler.URL{RawURL: listing.URL, Owner: listing.CompanyKey},
		Content: *content,
	}
	if err := p.enqueueExtract(ctx, raw); err != nil {
		return fmt.Errorf("collection: enqueueing changed page %q for re-extraction: %w", listing.URL, err)
	}
	if p.onRefreshed != nil {
		p.onRefreshed(ctx)
	}
	return nil
}
