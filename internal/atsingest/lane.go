// Package atsingest implements the ATS Fetch lane: the LLM-free path that pulls a
// recognized ATS tenant's postings straight from the provider's board API and
// saves them, instead of crawling and extracting the tenant's pages (ADR-0022).
//
// The lane owns a parallel worker pool of Processors, a per-run tenant dedup set
// (a tenant is fetched at most once a run), and a per-provider rate limiter. Its
// FetchTasks come from two sources that share the Lane.Submit dedup point: the
// Collection Cycle's seed routing (collection.RouteSeeds), primed via PrimeAsync,
// and boards embedded on crawled pages (#129). Closing the lane waits for priming
// to finish and drains the pool, so a run completes only once every in-flight
// fetch has finished.
package atsingest

import (
	"context"
	"sync"

	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
)

// Config configures a Lane.
type Config struct {
	// NewWorker builds one pool worker (a Processor). Called MaxWorkers times.
	NewWorker func() processor.Processor[FetchTask]
	// MaxWorkers is the ATS ingest pool size — how many tenants are fetched in
	// parallel. The per-provider rate limiter still serializes calls to a single
	// provider, so this mainly parallelizes across providers.
	MaxWorkers int
}

// Lane is the ATS Fetch subsystem for one run: a worker pool fed by a deduped
// task stream. Build it with NewLane, feed it with PrimeAsync (and, in #129,
// Submit), and end it with Close.
type Lane struct {
	pool    *pool.Pool[FetchTask]
	tenants *tenantSet
	priming sync.WaitGroup
}

// NewLane starts the lane's worker pool on ctx; the workers run until Close.
func NewLane(ctx context.Context, cfg Config) *Lane {
	return &Lane{
		pool: pool.NewPool(ctx, "ats_ingest_pool", cfg.NewWorker,
			pool.WithMaxWorkers[FetchTask](cfg.MaxWorkers)),
		tenants: newTenantSet(),
	}
}

// Submit enqueues t unless its tenant was already submitted this run, in which
// case it is silently skipped (returns nil). It is the single dedup point shared
// by PrimeAsync and #129's embed trigger.
//
// The first submission for a (Provider, TenantSlug) wins: its Owner becomes the
// attribution the tenant's postings are saved under, and any later submission for
// the same tenant — including one carrying a different Owner — is dropped. The
// spec only requires that a tenant be fetched once, not which Owner wins; this
// pins the tie-break to first-submit-wins. PrimeAsync enqueues the routed seed
// tasks before the crawl can discover an embed, so a tenant reached by both a
// Seed and an embed is attributed to its Seed Owner.
//
// It returns the pool's error (e.g. pool.ErrPoolClosed after Close, or ctx.Err()
// on cancel) when the enqueue itself fails.
func (l *Lane) Submit(ctx context.Context, t FetchTask) error {
	if !l.tenants.Add(t.Provider + ":" + t.TenantSlug) {
		return nil
	}
	return l.pool.Enqueue(ctx, &t)
}

// PrimeAsync submits the routed seed tasks from a background goroutine so a run's
// start path is never blocked by pool backpressure. The goroutine is tracked so
// Close waits for it to finish enqueuing; it stops early if a Submit fails (ctx
// cancelled or pool closed).
func (l *Lane) PrimeAsync(ctx context.Context, tasks []FetchTask) {
	l.priming.Add(1)
	go func() {
		defer l.priming.Done()
		for _, t := range tasks {
			if err := l.Submit(ctx, t); err != nil {
				return
			}
		}
	}()
}

// Close waits for priming to finish enqueuing, then drains the pool, blocking
// until every in-flight fetch completes. This is what makes a run complete only
// after the ATS pool has drained.
func (l *Lane) Close() {
	l.priming.Wait()
	l.pool.Close()
}
