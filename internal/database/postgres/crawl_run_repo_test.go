package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

// TestPausingStatusRoundTrip drives the `pausing` status through the repository
// to prove goose migration 0006 widened the crawl_run status CHECK to admit it:
// a constraint that still rejected `pausing` would surface here as an UpdateStatus
// error. It then reads the value back via GetStatus and ListByStatus.
func TestPausingStatusRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	runs := postgres.NewCrawlRunRepository(pool)
	defID := createDefinition(t, pool, "pausing-round-trip-test")

	id := uuid.New()
	if err := runs.Create(t.Context(), &crawler.CrawlRun{
		ID:           id,
		DefinitionID: defID,
		Status:       crawler.RunStatusRunning,
		StartedAt:    time.Now(),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A non-terminal transition to pausing: nil finishedAt, no error message.
	if err := runs.UpdateStatus(t.Context(), id, crawler.RunStatusPausing, nil, ""); err != nil {
		t.Fatalf("UpdateStatus to pausing (CHECK constraint should admit it): %v", err)
	}

	status, err := runs.GetStatus(t.Context(), id)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status != crawler.RunStatusPausing {
		t.Errorf("GetStatus: got %q, want pausing", status)
	}

	got, err := runs.ListByStatus(t.Context(), crawler.RunStatusPausing)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByStatus(pausing) count: got %d, want 1", len(got))
	}
	if got[0].ID != id {
		t.Errorf("ListByStatus(pausing): got run %s, want %s", got[0].ID, id)
	}
}

func TestListByStatus(t *testing.T) {
	pool := newTestPool(t)
	runs := postgres.NewCrawlRunRepository(pool)
	defID := createDefinition(t, pool, "list-by-status-test")

	// Seed one run per status; only running and stopping should come back.
	seed := map[crawler.RunStatus]uuid.UUID{
		crawler.RunStatusRunning:   {},
		crawler.RunStatusStopping:  {},
		crawler.RunStatusStopped:   {},
		crawler.RunStatusCompleted: {},
		crawler.RunStatusFailed:    {},
	}
	for status := range seed {
		id := uuid.New()
		seed[status] = id
		if err := runs.Create(t.Context(), &crawler.CrawlRun{
			ID:           id,
			DefinitionID: defID,
			Status:       status,
			StartedAt:    time.Now(),
		}); err != nil {
			t.Fatalf("seeding %s run: %v", status, err)
		}
	}

	got, err := runs.ListByStatus(t.Context(), crawler.RunStatusRunning, crawler.RunStatusStopping)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListByStatus count: got %d, want 2 (running + stopping)", len(got))
	}
	want := map[uuid.UUID]bool{
		seed[crawler.RunStatusRunning]:  true,
		seed[crawler.RunStatusStopping]: true,
	}
	for _, run := range got {
		if !want[run.ID] {
			t.Errorf("unexpected run %s with status %s", run.ID, run.Status)
		}
	}
}
