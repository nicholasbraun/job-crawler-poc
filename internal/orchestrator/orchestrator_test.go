package orchestrator_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
)

// fakeFrontier is a minimal Frontier that hands out queued URLs and then
// signals ErrDone. It records how many times Next was called and every URL
// added (so a test can assert the provenance seeded onto each). A non-nil addErr
// makes AddURL reject without queueing, so a test can drive the seed loop's
// rejection handling.
type fakeFrontier struct {
	queue     []crawler.URL
	added     []crawler.URL
	nextCalls int
	addErr    error
}

func (f *fakeFrontier) AddURL(ctx context.Context, url crawler.URL) error {
	if f.addErr != nil {
		return f.addErr
	}
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

func captureLogs(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
}

func hasErrorLevel(t *testing.T, buf *bytes.Buffer) bool {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("could not parse log line %q: %v", line, err)
		}
		if entry["level"] == "ERROR" {
			return true
		}
	}
	return false
}

// A Scope Budget rejection when re-seeding a resumed Cycle is an expected
// client-side drop (ADR-0053), so the seed loop must not log it at ERROR; any
// other AddURL error still does. Mirrors the processors'
// TestProcessAddURLRejections.
func TestRunSeedAddURLRejections(t *testing.T) {
	tests := []struct {
		name         string
		addErr       error
		wantErrorLog bool
	}{
		{name: "scope budget is not an error", addErr: frontier.ErrScopeBudget, wantErrorLog: false},
		{name: "unexpected error is logged at error", addErr: errors.New("boom"), wantErrorLog: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeFrontier{addErr: tt.addErr}
			o := orchestrator.NewOrchestrator(orchestrator.Config{
				Frontier:  f,
				OnNextURL: func(ctx context.Context, nextURL *crawler.URL) error { return nil },
				// ShouldStop nil: nothing was queued, so Next returns ErrDone at once.
			})

			var buf bytes.Buffer
			captureLogs(t, &buf, func() {
				seeds := []crawler.Seed{{URL: "https://example.com", Scope: "example.com"}}
				if err := o.Run(t.Context(), seeds); err != nil {
					t.Fatalf("Run returned error: %v", err)
				}
			})

			if got := hasErrorLevel(t, &buf); got != tt.wantErrorLog {
				t.Errorf("error-level log present = %v, want %v; logs:\n%s", got, tt.wantErrorLog, buf.String())
			}
		})
	}
}
