package redis_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
)

// TestMarkVisited exercises the crawl-lane visited-seeding op (ADR-0035): a seeded
// URL is skipped by a later AddURL (DUP), an empty slice is a no-op, and the FIFO
// cap still bounds the visited ZSET.
func TestMarkVisited(t *testing.T) {
	client := newTestClient(t)

	t.Run("a marked url is skipped by a later AddURL", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID)

		known := "http://acme.com/j/known"
		if err := f.MarkVisited(t.Context(), []string{known}); err != nil {
			t.Fatalf("MarkVisited: %v", err)
		}
		// AddURL of the same URL short-circuits as DUP (nothing enqueued).
		if err := f.AddURL(t.Context(), url("acme.com", known, 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		if _, err := f.Next(t.Context()); !errors.Is(err, frontier.ErrDone) {
			t.Errorf("Next after re-adding a marked url: got %v, want ErrDone (it must not enqueue)", err)
		}

		// A brand-new url is still crawlable.
		fresh := "http://acme.com/j/fresh"
		if err := f.AddURL(t.Context(), url("acme.com", fresh, 0)); err != nil {
			t.Fatalf("AddURL fresh: %v", err)
		}
		got, err := f.Next(t.Context())
		if err != nil {
			t.Fatalf("Next fresh: %v", err)
		}
		if got.RawURL != fresh {
			t.Errorf("Next = %q, want %q", got.RawURL, fresh)
		}
	})

	t.Run("empty slice is a no-op", func(t *testing.T) {
		f := redisfrontier.New(client, uuid.New())
		if err := f.MarkVisited(t.Context(), nil); err != nil {
			t.Errorf("MarkVisited(nil): %v", err)
		}
	})

	t.Run("cardinality respects the visited cap", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID, redisfrontier.WithVisitedCap(2))

		if err := f.MarkVisited(t.Context(), []string{
			"http://a.com/1", "http://a.com/2", "http://a.com/3",
		}); err != nil {
			t.Fatalf("MarkVisited: %v", err)
		}
		card, err := client.ZCard(t.Context(), "frontier:"+runID.String()+":visited").Result()
		if err != nil {
			t.Fatalf("ZCard: %v", err)
		}
		if card != 2 {
			t.Errorf("visited cardinality = %d, want 2 (FIFO-capped)", card)
		}
	})
}
