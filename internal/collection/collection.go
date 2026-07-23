// Package collection wires the Collection Crawl engine (ADR-0035/0036): the
// periodic, whole-Catalog Cycle that fills and keeps-live the global Corpus. It
// revives the preserved keyword-lane machinery — the URL walk, the LLM extract
// stage, and the ATS Fetch lane — as a collection Factory branch, dropping the
// keyword/country pruning (ADR-0038) and adding three capabilities on top:
// per-listing liveness (a direct refetch of each known-open posting), Career-Page
// dormancy, and a source-hash extraction cache that skips the LLM on unchanged
// pages.
//
// A Cycle runs two lanes over the Catalog seeds: RouteSeeds partitions them into
// crawl seeds (walked + extracted, then refetched for liveness) and ATS FetchTasks
// (pulled straight from the provider board API). The pure, testable pieces —
// RouteSeeds, the Career-Page Attributor, the status classifier, and the refetch
// processor — live here; cmd/server composes them into a runner.Engine.
package collection

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// DormancyRecorder folds one Career-Page reach probe into its dormancy counters,
// cascading a close of the page's Open listings when the probe tips it dormant
// (ADR-0035). Satisfied by postgres.CareerPageRepository.RecordProbe.
type DormancyRecorder interface {
	RecordProbe(ctx context.Context, careerPageID uuid.UUID, outcome crawler.ProbeOutcome, threshold int) (crawler.DormancyResult, error)
}

// VisitedSeeder seeds already-known posting URLs into a run's visited set so the
// discovery walk skips them (ADR-0035): the refetch pass owns liveness of known
// postings, the walk only surfaces new ones. Satisfied by the redis Frontier's
// MarkVisited.
type VisitedSeeder interface {
	MarkVisited(ctx context.Context, rawURLs []string) error
}

// SeedVisited pre-seeds every Open listing under each refetch page into seeder's
// visited set (ADR-0035), so a Cycle's walk surfaces only NEW postings. It is
// idempotent (MarkVisited is ZADD NX), so a resumed Cycle re-runs it harmlessly.
// Per-page errors are joined and returned so the caller can log them; a partial
// failure never aborts the pre-pass. A page with no Open listings is skipped.
func SeedVisited(ctx context.Context, seeder VisitedSeeder, open crawler.CorpusLivenessRepository, pages []crawler.CollectionSeed) error {
	var errs error
	for _, page := range pages {
		listings, err := open.ListOpen(ctx, page.CareerPageID)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("collection: listing open for visited-seed of page %q: %w", page.CareerPageID, err))
			continue
		}
		if len(listings) == 0 {
			continue
		}
		urls := make([]string, 0, len(listings))
		for _, jl := range listings {
			urls = append(urls, jl.URL)
		}
		if err := seeder.MarkVisited(ctx, urls); err != nil {
			errs = errors.Join(errs, fmt.Errorf("collection: marking visited for page %q: %w", page.CareerPageID, err))
		}
	}
	return errs
}
