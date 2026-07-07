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
// signals ErrDone. It records how many times Next was called.
type fakeFrontier struct {
	queue     []crawler.URL
	nextCalls int
}

func (f *fakeFrontier) AddURL(ctx context.Context, url crawler.URL) error {
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

func (f *fakeFrontier) MarkDone(ctx context.Context) error { return nil }

// fakeURLRepository treats every URL as new and never errors.
type fakeURLRepository struct{}

func (fakeURLRepository) Save(ctx context.Context, url string) (bool, error) { return true, nil }
func (fakeURLRepository) Visited(ctx context.Context, url string) (bool, error) {
	return false, nil
}

func TestRunStopRequested(t *testing.T) {
	f := &fakeFrontier{}
	dispatched := 0

	o := orchestrator.NewOrchestrator(orchestrator.Config{
		Frontier:      f,
		URLRepository: fakeURLRepository{},
		OnNextURL: func(ctx context.Context, nextURL *crawler.URL) error {
			dispatched++
			return nil
		},
		ShouldStop: func(ctx context.Context) bool { return true },
	})

	err := o.Run(t.Context(), []string{"https://example.com"})

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
		Frontier:      f,
		URLRepository: fakeURLRepository{},
		OnNextURL: func(ctx context.Context, nextURL *crawler.URL) error {
			dispatched++
			return nil
		},
		// ShouldStop nil: the CLI's behavior — run to completion.
	})

	err := o.Run(t.Context(), []string{"https://example.com"})

	if err != nil {
		t.Fatalf("expected nil (ErrDone maps to nil), got %v", err)
	}
	if dispatched != 1 {
		t.Errorf("expected the single seed URL dispatched, got %d", dispatched)
	}
}
