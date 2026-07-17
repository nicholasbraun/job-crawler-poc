package orchestrator_test

import (
	"context"
	"errors"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
)

// fakeFrontier is a minimal Frontier that hands out queued URLs and then
// signals ErrDone. It records how many times Next was called and every URL
// added (so a test can assert the provenance seeded onto each).
type fakeFrontier struct {
	queue     []crawler.URL
	added     []crawler.URL
	nextCalls int
}

func (f *fakeFrontier) AddURL(ctx context.Context, url crawler.URL) error {
	f.added = append(f.added, url)
	f.queue = append(f.queue, url)
	return nil
}

func (f *fakeFrontier) Next(ctx context.Context) (crawler.URL, error) {
	f.nextCalls++
	if len(f.queue) == 0 {
		return crawler.URL{}, frontier.ErrDone
	}
	next := f.queue[0]
	f.queue = f.queue[1:]
	return next, nil
}

func (f *fakeFrontier) MarkDone(ctx context.Context, url string) error { return nil }

func TestRunStopRequested(t *testing.T) {
	f := &fakeFrontier{}
	dispatched := 0

	o := orchestrator.NewOrchestrator(orchestrator.Config{
		Frontier: f,
		OnNextURL: func(ctx context.Context, nextURL *crawler.URL) error {
			dispatched++
			return nil
		},
		ShouldStop: func(ctx context.Context) bool { return true },
	})

	err := o.Run(t.Context(), []crawler.Seed{{URL: "https://example.com"}})

	if !errors.Is(err, orchestrator.ErrStopRequested) {
		t.Fatalf("expected %v, got %v", orchestrator.ErrStopRequested, err)
	}
	if dispatched != 0 {
		t.Errorf("expected no URLs dispatched after stop, got %d", dispatched)
	}
	if f.nextCalls != 0 {
		t.Errorf("expected Next never called after stop, got %d", f.nextCalls)
	}
}

func TestRunCompletesWithNilShouldStop(t *testing.T) {
	f := &fakeFrontier{}
	dispatched := 0

	o := orchestrator.NewOrchestrator(orchestrator.Config{
		Frontier: f,
		OnNextURL: func(ctx context.Context, nextURL *crawler.URL) error {
			dispatched++
			return nil
		},
		// ShouldStop nil: run to completion.
	})

	err := o.Run(t.Context(), []crawler.Seed{{URL: "https://example.com"}})

	if err != nil {
		t.Fatalf("expected nil (ErrDone maps to nil), got %v", err)
	}
	if dispatched != 1 {
		t.Errorf("expected the single seed URL dispatched, got %d", dispatched)
	}
}

// TestRunSeedsCarryProvenance proves the orchestrator copies each Seed's
// ADR-0021 provenance (Scope, Owner) onto the URL it adds to the frontier: a
// Keyword seed carries its fence/attribution keys, and an empty-provenance
// Discovery seed stays empty (it roams). Seeding runs before the loop, so
// ShouldStop returning true lets us inspect only the seeded URLs.
func TestRunSeedsCarryProvenance(t *testing.T) {
	f := &fakeFrontier{}

	o := orchestrator.NewOrchestrator(orchestrator.Config{
		Frontier:   f,
		OnNextURL:  func(ctx context.Context, nextURL *crawler.URL) error { return nil },
		ShouldStop: func(ctx context.Context) bool { return true },
	})

	seeds := []crawler.Seed{
		{URL: "https://acme.com/careers", Scope: "acme.com", Owner: "acme-imported"},
		{URL: "https://discovery.example.com", Scope: "", Owner: ""},
	}
	if err := o.Run(t.Context(), seeds); !errors.Is(err, orchestrator.ErrStopRequested) {
		t.Fatalf("expected %v, got %v", orchestrator.ErrStopRequested, err)
	}

	if len(f.added) != len(seeds) {
		t.Fatalf("want %d seeded urls, got %d", len(seeds), len(f.added))
	}

	byScope := map[string]crawler.URL{}
	for _, u := range f.added {
		byScope[u.Scope] = u
	}

	scoped, ok := byScope["acme.com"]
	if !ok {
		t.Fatalf("scoped seed missing from frontier; added=%v", f.added)
	}
	if scoped.Owner != "acme-imported" {
		t.Errorf("scoped seed Owner: want %q, got %q", "acme-imported", scoped.Owner)
	}

	roam, ok := byScope[""]
	if !ok {
		t.Fatalf("empty-provenance seed missing from frontier; added=%v", f.added)
	}
	if roam.Owner != "" {
		t.Errorf("discovery seed Owner: want empty, got %q", roam.Owner)
	}
}
