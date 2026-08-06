package collection

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
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
	// Classifier re-classifies a reachable Career Page on the dormancy probe: a
	// 200-OK page that no longer lists open positions (redesigned into a
	// marketing/landing page) is whole-page death (ProbeDead) and accrues dormancy
	// like a 404 board (ADR-0035). It is the same career-page classifier Discovery
	// runs, gated behind the 200 branch so it fires at most once per page per Cycle
	// (bounded by Catalog size, not listing count); only its IsCareerPage verdict is
	// read here (the employer name Discovery reads is irrelevant to liveness).
	Classifier careerpageprocessor.Confirmer
	// GateConfig is the pre-LLM gate config. The dormancy re-classification consults
	// pagegate.CareerPage first (parity with Discovery's acceptance rule): a
	// structurally-certain verdict is taken WITHOUT the LLM, so a page discovery
	// certain-accepted on structure alone can never be deterministically false-closed
	// by an LLM blip, and the LLM runs only for a structurally-ambiguous page.
	GateConfig crawler.LLMGateConfig
	// SourceHash computes the extraction-cache key over a page's main content —
	// bound to crawler.SourceHash with the extractor's ExtractMaxChars so the key
	// is byte-identical to the one the extractor stored (ADR-0035).
	SourceHash func(mainContent string) string
	// Descriptions rewrites a legacy, model-authored description in place on the
	// unchanged-content branch (ADR-0041), from the page this refetch has ALREADY
	// downloaded and parsed — no model call and no extra fetch. Deliberately not the
	// full CorpusRepository: a Save would advance last_seen, clear closed_at and reset
	// the inconclusive streak. REQUIRED, like EnqueueExtract and SourceHash: a
	// nil-tolerant fallback would silently leave the Corpus on legacy summaries
	// forever with nothing to notice it.
	Descriptions crawler.CorpusDescriptionRepository
	// DescriptionMaxChars caps a healed Posting Body in runes — the SAME
	// DESCRIPTION_MAX_CHARS knob the save processor uses (ADR-0041), so a healed body
	// and a freshly-extracted one are bounded identically. Zero or negative falls back
	// to crawler.DefaultDescriptionMaxChars.
	DescriptionMaxChars int
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
// keeps it open with no LLM call (source_hash cache) and heals a legacy,
// model-authored description from the page already in hand (ADR-0041), a changed 200
// re-extracts. Only listed-open URLs are touched, so a down collector closes nothing
// (attempt-gated by construction). It implements
// processor.Processor[crawler.CollectionSeed].
//
// Soft-404 (a 200 whose body no longer describes a posting) re-extracts and the
// extractor abstains, so the listing stays open on a stale last_seen: an accepted
// v1 gap (deterministic soft-404 detection is out of this ticket's scope).
type RefetchProcessor struct {
	downloader          downloader.Downloader
	parser              parser.Parser
	liveness            crawler.CorpusLivenessRepository
	dormancy            DormancyRecorder
	classifier          careerpageprocessor.Confirmer
	gateConfig          crawler.LLMGateConfig
	sourceHash          func(mainContent string) string
	descriptions        crawler.CorpusDescriptionRepository
	descriptionMaxChars int
	enqueueExtract      func(ctx context.Context, raw *crawler.RawJobListing) error
	staleThreshold      int
	dormancyThreshold   int
	onRefreshed         func(ctx context.Context)
	onClosed            func(ctx context.Context, n int)
}

var _ processor.Processor[crawler.CollectionSeed] = (*RefetchProcessor)(nil)

// NewRefetchProcessor builds a crawl-lane refetch processor.
func NewRefetchProcessor(cfg *RefetchConfig) *RefetchProcessor {
	return &RefetchProcessor{
		downloader:          cfg.Downloader,
		parser:              cfg.Parser,
		liveness:            cfg.Liveness,
		dormancy:            cfg.Dormancy,
		classifier:          cfg.Classifier,
		gateConfig:          cfg.GateConfig,
		sourceHash:          cfg.SourceHash,
		descriptions:        cfg.Descriptions,
		descriptionMaxChars: cfg.DescriptionMaxChars,
		enqueueExtract:      cfg.EnqueueExtract,
		staleThreshold:      cfg.StaleThreshold,
		dormancyThreshold:   cfg.DormancyThreshold,
		onRefreshed:         cfg.OnRefreshed,
		onClosed:            cfg.OnClosed,
	}
}

// Process probes page for dormancy, then refetches each of its Open listings. A page
// that tips dormant on this probe closes its listings (via the cascade) and is not
// refetched. Per-listing refetch errors are joined so one bad posting neither drops
// the rest nor aborts the pool.
func (p *RefetchProcessor) Process(ctx context.Context, page *crawler.CollectionSeed) error {
	// 1. Dormancy probe of the page URL. Whole-page death is hard-dead — a 404/410
	//    board OR a reachable 200 that no longer classifies as a careers page (a
	//    redesign into a marketing/landing page listing no openings) — and both accrue
	//    dormancy. A transient GET, or a parse/classify blip on a reachable page, is
	//    Inconclusive and never counts.
	res, derr := p.dormancy.RecordProbe(ctx, page.CareerPageID, p.probePage(ctx, page), p.dormancyThreshold)
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

// probePage classifies one Career-Page dormancy probe (ADR-0035): it GETs the page
// and, only on a reachable 200, re-classifies it to catch whole-page death — a
// redesign into a page that no longer lists openings. The re-classification is the
// LLM career-page classifier Discovery runs; gating it behind the 200 branch keeps it
// to one classify per crawled Career Page per Cycle (bounded by Catalog size, not
// listing count) — the added cost ADR-0035 accepts by naming "no-longer-classifies" a
// dormancy trigger. A 404/410/transient GET skips classification entirely; a parse or
// classify failure on a reached page folds to Inconclusive so a blip never counts.
func (p *RefetchProcessor) probePage(ctx context.Context, page *crawler.CollectionSeed) crawler.ProbeOutcome {
	resp, getErr := p.downloader.Get(ctx, page.URL)
	if getErr != nil {
		return classifyPageProbe(getErr, false, nil)
	}
	still, classifyErr := p.stillCareerPage(ctx, page.URL, resp)
	if classifyErr != nil {
		slog.Warn("collection: page reclassification failed; dormancy probe inconclusive",
			"url", page.URL, "err", classifyErr)
	}
	return classifyPageProbe(nil, still, classifyErr)
}

// stillCareerPage parses a reached page and decides whether it STILL reads as a
// Career Page, mirroring Discovery's acceptance rule so the two never disagree: the
// structural pre-gate (pagegate.CareerPage) runs first, and a CERTAIN verdict is
// taken without the LLM — a structurally-obvious hub stays a Career Page (Alive), a
// structurally-definite non-page is death (Dead). Only a structurally-ambiguous page
// consults the LLM classifier. This closes the deterministic false-close class: a
// page discovery certain-accepted on structure alone is never dormant-closed by a
// repeatable LLM false-negative. A parse or classifier error returns a non-nil error,
// which probePage folds into an Inconclusive probe (never counts toward dormancy);
// the returned bool is meaningful only when err is nil.
func (p *RefetchProcessor) stillCareerPage(ctx context.Context, url string, resp *downloader.Response) (bool, error) {
	content, perr := p.parser.Parse(resp.Content)
	if perr != nil {
		return false, fmt.Errorf("collection: parsing page %q for reclassification: %w", url, perr)
	}
	if u, uerr := crawler.NewURL(url); uerr == nil {
		if accept, certain := pagegate.CareerPage(u, content, p.gateConfig); certain {
			return accept, nil // structurally definitive — no LLM call, matches Discovery
		}
	}
	verdict, cerr := p.classifier.Confirm(ctx, url, content)
	if cerr != nil {
		return false, fmt.Errorf("collection: reclassifying page %q: %w", url, cerr)
	}
	return verdict.IsCareerPage, nil
}

// refetchOne refetches one open posting and applies its Liveness Outcome (ADR-0035):
// a 404/410 closes it (Dead); a transient error is Inconclusive; an unchanged 200
// keeps it open with no LLM call (source_hash matches) and heals a legacy description
// from the page in hand (ADR-0041); a changed 200 is re-enqueued for re-extraction
// (which re-Saves, reopening/advancing it and stamping a real body and marker) with no
// probe.
func (p *RefetchProcessor) refetchOne(ctx context.Context, listing *crawler.JobListing) error {
	// Re-gate: a known listing whose URL the extract gate now rejects on structure
	// alone -- a bare/locale root or a terminal jobs-index segment -- is a stale false
	// positive saved before the gate tightened. Close it (Dead) instead of refetching,
	// so the Corpus self-heals each Cycle with no network call and any future gate
	// tightening cleans up retroactively. This also forecloses the changed-content
	// re-extract path below from re-creating it.
	if pagegate.IsHubOrRootURL(crawler.URL{RawURL: listing.URL}) {
		if _, aerr := p.liveness.ApplyCrawlProbe(ctx, listing.CanonicalURL, crawler.ProbeDead, p.staleThreshold); aerr != nil {
			return fmt.Errorf("collection: closing re-gated listing %q: %w", listing.CanonicalURL, aerr)
		}
		return nil
	}

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
		// Unchanged source content: confirmed alive with NO LLM call. The probe is
		// applied first and independently of the heal — liveness is this lane's job and
		// must never hinge on a description rewrite — and the two errors are joined so
		// one failure neither hides the other nor drops the remaining postings (Process
		// joins the per-listing errors already). errors.Join(nil, nil) is nil, so a
		// clean pass still returns nil.
		var errs error
		if _, aerr := p.liveness.ApplyCrawlProbe(ctx, listing.CanonicalURL, crawler.ProbeAlive, p.staleThreshold); aerr != nil {
			errs = errors.Join(errs, fmt.Errorf("collection: applying refetch probe for %q: %w", listing.CanonicalURL, aerr))
		}
		return errors.Join(errs, p.healLegacyDescription(ctx, listing, content))
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

// healLegacyDescription rewrites a listing still carrying a LEGACY, model-authored
// summary with the Posting Body derived from the page this refetch already downloaded
// and parsed (ADR-0041): no model call, no extra fetch, one write per listing. It is a
// no-op for every other Description Source, and that marker guard is the whole exclusion
// rule: the refetch loop iterates every Open listing under a crawl seed with NO
// source-lane filter, so nothing structural in THIS function keeps board text safe.
//
// No ATS-lane listing actually arrives here today, by three independent accidents rather
// than by design: an ATS Embed board is submitted with a Nil CareerPageID, so its
// listings are saved with career_page_id NULL and can never match ListOpen's
// career_page_id filter; an ATS-routed seed is diverted to the fetch lane before
// refetchPages is built (collection.RouteSeeds); and an ATS row's source_hash is empty,
// which no sha256 digest equals, so it would take the changed-content branch regardless.
// The ats_board marker is defence in depth against any of those three changing.
//
// Writing the marker alongside the body is what makes this one write per listing rather
// than one per Cycle: next Cycle the row is no longer legacy.
//
// Only the refetch lane can heal: SeedVisited (ADR-0035) seeds every Open listing's URL
// into the visited set at Cycle start, so the walk only ever surfaces new postings.
func (p *RefetchProcessor) healLegacyDescription(ctx context.Context, listing *crawler.JobListing, content *crawler.Content) error {
	if listing.DescriptionSource != crawler.DescriptionSourceLLMSummary {
		return nil
	}
	body, source := crawler.PostingBody(content, p.descriptionMaxChars)
	if body == "" {
		// A page that parsed to nothing has no body to heal with. Keep the legacy text
		// and marker so a later Cycle with a real parse still heals it, rather than
		// trading a stale summary for an empty description (and an empty weight-B index
		// entry).
		return nil
	}
	if err := p.descriptions.UpdateDescription(ctx, listing.CanonicalURL, body, source); err != nil {
		return fmt.Errorf("collection: healing legacy description for %q: %w", listing.CanonicalURL, err)
	}
	return nil
}
