package atsingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// ProcessorConfig groups the ATS ingest processor's dependencies.
type ProcessorConfig struct {
	// ResolveFetcher resolves an ATS provider family to its BoardFetcher, or
	// ok=false when the provider has no board-API client. main.go passes the
	// Registry's Fetcher method.
	ResolveFetcher func(provider string) (ats.BoardFetcher, bool)
	// Repository persists the fetched Job Listings, keyed and upserted on
	// (DefinitionID, URL) so re-fetching a tenant refreshes rather than duplicates.
	Repository crawler.JobListingRepository
	// DefinitionID is the crawl definition the fetched listings are saved under.
	DefinitionID uuid.UUID
	// Keywords is the crawl's relevance keyword set; a fetched posting is saved
	// only when its title or description matches one (ADR-0004, the same matcher the
	// crawl lane uses). Empty keywords save nothing.
	Keywords []string
	// CompanyNames is the per-run CompanyKey → Company-name snapshot (ADR-0021). A
	// saved listing's Company is looked up here via the task's Owner; a nil map or a
	// missing Owner yields "".
	CompanyNames map[string]string
	// RateLimiter paces board-API calls per provider. Optional: nil disables pacing.
	RateLimiter *HostLimiter
	// OnSaved is called once per saved listing (e.g. a counter tap). Optional: nil
	// is a no-op.
	OnSaved func(ctx context.Context)
}

// Processor fetches one ATS tenant's board and persists its keyword-matching
// postings, attributing each to the task's Owner Company (ADR-0021/ADR-0022). It
// makes NO LLM call — a structural guarantee, not a runtime check: the board API
// supplies every field, so the processor holds no extractor. It implements
// processor.Processor[FetchTask].
type Processor struct {
	resolveFetcher func(provider string) (ats.BoardFetcher, bool)
	repository     crawler.JobListingRepository
	definitionID   uuid.UUID
	keywordCheck   filter.CheckFn[string]
	companyNames   map[string]string
	limiter        *HostLimiter
	onSaved        func(ctx context.Context)
}

var _ processor.Processor[FetchTask] = (*Processor)(nil)

// NewProcessor builds an ATS ingest processor. The keyword matcher is compiled
// once here (word-boundary, case-insensitive; ADR-0004) and reused across tasks.
func NewProcessor(cfg *ProcessorConfig) *Processor {
	return &Processor{
		resolveFetcher: cfg.ResolveFetcher,
		repository:     cfg.Repository,
		definitionID:   cfg.DefinitionID,
		keywordCheck:   filter.Contains(cfg.Keywords...),
		companyNames:   cfg.CompanyNames,
		limiter:        cfg.RateLimiter,
		onSaved:        cfg.OnSaved,
	}
}

// Process fetches task's tenant board and saves each keyword-matching posting
// under the Owner Company. An unregistered provider is a no-op (routing already
// gated it, but a clientless provider must never error the pool); a fetch error is
// wrapped and returned; per-posting save errors are joined and returned so one bad
// posting neither drops the rest of the tenant nor aborts the pool (the next run's
// upsert is idempotent).
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
	if err != nil {
		return fmt.Errorf("atsingest: fetching %s tenant %q: %w", task.Provider, task.TenantSlug, err)
	}

	var saveErr error
	for _, jl := range listings {
		if !p.matchesKeywords(jl) {
			continue
		}
		// Owner attribution (ADR-0021): stamp the Catalog Company looked up via the
		// task's Owner and persist the Owner as the durable CompanyKey, discarding
		// the provider board's own company field. A nil snapshot or a missing Owner
		// yields "".
		jl.CompanyKey = task.Owner
		jl.Company = p.companyNames[task.Owner]
		// Resolve the Country at save (ADR-0029): prefer the provider's structured
		// country hint, else the composed Location. Unresolvable -> the empty Country,
		// kept (ADR-0028).
		jl.Country = resolveCountry(jl)
		if err := p.repository.Save(ctx, p.definitionID, jl); err != nil {
			saveErr = errors.Join(saveErr, fmt.Errorf("atsingest: saving %s tenant %q listing %q: %w", task.Provider, task.TenantSlug, jl.URL, err))
			continue
		}
		if p.onSaved != nil {
			p.onSaved(ctx)
		}
	}
	return saveErr
}

// resolveCountry feeds the Country Resolver the provider's structured country hint
// when present (an ISO code like Recruitee's country_code, or a name like
// SmartRecruiters/Workable country / Ashby addressCountry), else the composed
// Location string (ADR-0029). A hint that is already a valid ISO code is used
// directly (uppercased); a name-shaped hint is resolved; when the hint is empty or
// unresolvable it falls back to the Location. Unresolvable throughout -> the empty
// Country, which the Country Constraint keeps (ADR-0028).
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

// matchesKeywords reports whether the listing's title or description contains a
// relevance keyword. Title-OR-description mirrors the crawl lane's
// title-OR-content relevance filter; empty keywords match nothing.
func (p *Processor) matchesKeywords(jl *crawler.JobListing) bool {
	return errors.Is(p.keywordCheck(jl.Title), filter.ErrPass) ||
		errors.Is(p.keywordCheck(jl.Description), filter.ErrPass)
}
