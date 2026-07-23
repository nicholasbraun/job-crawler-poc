package atsingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
	"github.com/nicholasbraun/job-crawler-poc/internal/listingid"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// DormancyRecorder folds one board-reach probe into a Career Page's dormancy
// counters, cascading a close of the page's Open listings when the probe tips it
// dormant (ADR-0035). Satisfied by postgres.CareerPageRepository.RecordProbe.
type DormancyRecorder interface {
	RecordProbe(ctx context.Context, careerPageID uuid.UUID, outcome crawler.ProbeOutcome, threshold int) (crawler.DormancyResult, error)
}

// ProcessorConfig groups the ATS ingest processor's dependencies.
type ProcessorConfig struct {
	// ResolveFetcher resolves an ATS provider family to its BoardFetcher, or
	// ok=false when the provider has no board-API client. main.go passes the
	// Registry's Fetcher method.
	ResolveFetcher func(provider string) (ats.BoardFetcher, bool)
	// Repository persists the fetched Job Listings into the Corpus, upserted on
	// their canonical identity (ADR-0034) so re-fetching a tenant refreshes rather
	// than duplicates. A Collection Cycle saves EVERY fetched posting — there is no
	// keyword pruning (ADR-0038).
	Repository crawler.CorpusRepository
	// Liveness runs the ATS absence-sweep after a complete fetch (ADR-0035): any
	// Open ATS listing under the board not re-seen this Cycle is Closed. Optional:
	// nil skips the sweep (an embed-discovered board with no owning page).
	Liveness crawler.CorpusLivenessRepository
	// Dormancy records the board-reach probe into the Career Page's dormancy
	// counters (ADR-0035). Optional: nil skips dormancy.
	Dormancy DormancyRecorder
	// DormancyThreshold is the consecutive hard-dead Cycle count at which a Career
	// Page goes dormant (crawler.DefaultPageDormancyThreshold).
	DormancyThreshold int
	// CycleStart is the absence-sweep watermark: any Open ATS listing under the
	// board whose last_seen predates it (not re-seen this Cycle) is closed on a
	// complete fetch.
	CycleStart time.Time
	// CompanyNames is the per-run CompanyKey → Company-name snapshot (ADR-0021). A
	// saved listing's Company is looked up here via the task's Owner; a nil map or a
	// missing Owner yields "".
	CompanyNames map[string]string
	// RateLimiter paces board-API calls per provider. Optional: nil disables pacing.
	RateLimiter *HostLimiter
	// OnSaved is called once per saved listing (e.g. a counter tap). Optional: nil
	// is a no-op.
	OnSaved func(ctx context.Context)
	// OnBoardFetched is called once per reachable board fetch (metric tap). Optional.
	OnBoardFetched func(ctx context.Context)
	// OnBoardIncomplete is called once per incomplete (presence-only) fetch. Optional.
	OnBoardIncomplete func(ctx context.Context)
	// OnClosed is called with the number of listings closed by the absence-sweep or
	// the dormancy cascade (metric tap). Optional.
	OnClosed func(ctx context.Context, n int)
}

// Processor fetches one ATS tenant's board and persists its postings, attributing
// each to the task's Owner Company (ADR-0021/ADR-0022) and keeping the board's
// Liveness current (ADR-0035): it saves presence, runs the absence-sweep on a
// complete fetch, and folds the board-reach outcome into Career-Page dormancy. It
// makes NO LLM call — the board API supplies every field, so the processor holds no
// extractor. It implements processor.Processor[FetchTask].
type Processor struct {
	resolveFetcher    func(provider string) (ats.BoardFetcher, bool)
	repository        crawler.CorpusRepository
	liveness          crawler.CorpusLivenessRepository
	dormancy          DormancyRecorder
	dormancyThreshold int
	cycleStart        time.Time
	companyNames      map[string]string
	limiter           *HostLimiter
	onSaved           func(ctx context.Context)
	onBoardFetched    func(ctx context.Context)
	onBoardIncomplete func(ctx context.Context)
	onClosed          func(ctx context.Context, n int)
}

var _ processor.Processor[FetchTask] = (*Processor)(nil)

// NewProcessor builds an ATS ingest processor.
func NewProcessor(cfg *ProcessorConfig) *Processor {
	return &Processor{
		resolveFetcher:    cfg.ResolveFetcher,
		repository:        cfg.Repository,
		liveness:          cfg.Liveness,
		dormancy:          cfg.Dormancy,
		dormancyThreshold: cfg.DormancyThreshold,
		cycleStart:        cfg.CycleStart,
		companyNames:      cfg.CompanyNames,
		limiter:           cfg.RateLimiter,
		onSaved:           cfg.OnSaved,
		onBoardFetched:    cfg.OnBoardFetched,
		onBoardIncomplete: cfg.OnBoardIncomplete,
		onClosed:          cfg.OnClosed,
	}
}

// Process fetches task's tenant board, saves every posting under the Owner Company,
// then keeps the board's Liveness current (ADR-0035): the absence-sweep closes Open
// listings not re-seen on a complete fetch, and the board-reach outcome folds into
// Career-Page dormancy. An unregistered provider is a no-op (routing already gated
// it). A hard fetch failure records a dormancy probe (Dead on a board-status error,
// Inconclusive otherwise), then propagates so the pool logs it and the tenant is
// retried next Cycle; per-posting save errors are joined so one bad posting neither
// drops the rest nor aborts the pool.
func (p *Processor) Process(ctx context.Context, task *FetchTask) error {
	fetcher, ok := p.resolveFetcher(task.Provider)
	if !ok {
		slog.Warn("atsingest: no board fetcher for provider, skipping task",
			"provider", task.Provider, "tenant", task.TenantSlug)
		return nil
	}

	// Nil-receiver-safe: a nil limiter's Wait returns immediately.
	if err := p.limiter.Wait(ctx, task.Provider); err != nil {
		return err
	}

	listings, err := fetcher.Fetch(ctx, task.TenantSlug)
	if err != nil && !errors.Is(err, ats.ErrBoardIncomplete) {
		// Hard failure: classify for dormancy (a board-status error is hard-dead; a
		// decode/context/network error is inconclusive), record it, then propagate.
		p.recordDormancy(ctx, task, classifyBoard(err))
		return fmt.Errorf("atsingest: fetching %s tenant %q: %w", task.Provider, task.TenantSlug, err)
	}

	boardComplete := err == nil
	if errors.Is(err, ats.ErrBoardIncomplete) {
		// Save-presence / skip-sweep (ADR-0035): persist the partial slice so those
		// postings refresh, but the absence-sweep must not run on an incomplete fetch.
		slog.Warn("atsingest: incomplete board fetch, saving presence only",
			"provider", task.Provider, "tenant", task.TenantSlug, "listings", len(listings))
		if p.onBoardIncomplete != nil {
			p.onBoardIncomplete(ctx)
		}
	}
	if p.onBoardFetched != nil {
		p.onBoardFetched(ctx)
	}

	var saveErr error
	for _, jl := range listings {
		// Owner attribution (ADR-0021): stamp the Catalog Company looked up via the
		// task's Owner and persist the Owner as the durable CompanyKey, discarding the
		// provider board's own company field. A nil snapshot or missing Owner yields "".
		jl.CompanyKey = task.Owner
		jl.Company = p.companyNames[task.Owner]
		// Resolve the Country at save (ADR-0029): prefer the provider's structured
		// country hint, else the composed Location. Unresolvable -> the empty Country.
		jl.Country = resolveCountry(jl)
		// Stamp the Corpus identity + lane + owning Career Page (ADR-0034/0035). The ATS
		// lane keys on the provider's stable posting id so a URL re-slug never forges a
		// new posting; a posting the fetcher could not id falls back to its canonicalized
		// URL rather than collapsing the whole tenant to one key.
		jl.Source = crawler.SourceLaneATS
		jl.CareerPageID = task.CareerPageID
		if jl.SourceID != "" {
			jl.CanonicalURL = listingid.FromATS(task.Provider, task.TenantSlug, jl.SourceID)
		} else {
			jl.CanonicalURL = listingid.FromURL(jl.URL)
		}
		if err := p.repository.Save(ctx, jl); err != nil {
			saveErr = errors.Join(saveErr, fmt.Errorf("atsingest: saving %s tenant %q listing %q: %w", task.Provider, task.TenantSlug, jl.URL, err))
			continue
		}
		if p.onSaved != nil {
			p.onSaved(ctx)
		}
	}

	// Liveness (ADR-0035) is scoped to a real owning Career Page: an embed-discovered
	// board (CareerPageID == Nil) is saved-only, no sweep and no dormancy.
	if task.CareerPageID != uuid.Nil {
		if p.liveness != nil {
			closed, cerr := p.liveness.CloseAbsent(ctx, task.CareerPageID, p.cycleStart, boardComplete)
			if cerr != nil {
				saveErr = errors.Join(saveErr, fmt.Errorf("atsingest: closing absent listings for %s tenant %q: %w", task.Provider, task.TenantSlug, cerr))
			} else if closed > 0 && p.onClosed != nil {
				p.onClosed(ctx, closed)
			}
		}
		// The board was reachable (err == nil or ErrBoardIncomplete) -> Alive resets
		// the dormancy counters.
		p.recordDormancy(ctx, task, crawler.ProbeAlive)
	}

	return saveErr
}

// recordDormancy folds one board-reach outcome into the task's Career Page dormancy
// counters, tallying any cascade-closed listings. A nil recorder or a Nil
// CareerPageID (embed-discovered board) is a no-op. A recorder error is logged, not
// propagated: dormancy bookkeeping must never fail an otherwise-good fetch.
func (p *Processor) recordDormancy(ctx context.Context, task *FetchTask, outcome crawler.ProbeOutcome) {
	if p.dormancy == nil || task.CareerPageID == uuid.Nil {
		return
	}
	res, err := p.dormancy.RecordProbe(ctx, task.CareerPageID, outcome, p.dormancyThreshold)
	if err != nil {
		slog.Error("atsingest: recording dormancy probe", "err", err,
			"provider", task.Provider, "tenant", task.TenantSlug, "career_page_id", task.CareerPageID)
		return
	}
	if res.BecameDormant && res.ClosedListings > 0 && p.onClosed != nil {
		p.onClosed(ctx, res.ClosedListings)
	}
}

// classifyBoard maps a hard (non-ErrBoardIncomplete) fetch error to a dormancy
// ProbeOutcome (ADR-0035): a board-status error (a 404/non-200 board) is hard-dead;
// any other error (a decode, context, or network failure) is inconclusive so a
// transient blip never counts toward dormancy. ErrBoardStatus carries no HTTP code,
// so a persistent 5xx counts as hard-dead — an accepted imprecision the high page
// threshold absorbs (the crawl lane demonstrates the 404 case precisely).
func classifyBoard(err error) crawler.ProbeOutcome {
	if errors.Is(err, ats.ErrBoardStatus) {
		return crawler.ProbeDead
	}
	return crawler.ProbeInconclusive
}

// resolveCountry feeds the Country Resolver the provider's structured country hint
// when present (an ISO code like Recruitee's country_code or SmartRecruiters' country,
// or a name like Workable's country / Ashby's addressCountry), else the composed
// Location string (ADR-0029). A hint that is already a valid ISO code is used
// directly (uppercased); a name-shaped hint is resolved; when the hint is empty or
// unresolvable it falls back to the Location. Unresolvable throughout -> the empty
// Country, which is kept (ADR-0028).
func resolveCountry(jl *crawler.JobListing) string {
	if h := strings.TrimSpace(jl.CountryHint); h != "" {
		if geo.Valid(h) {
			return strings.ToUpper(h)
		}
		if c := geo.Resolve(h); c != "" {
			return c
		}
	}
	return geo.Resolve(jl.Location)
}
