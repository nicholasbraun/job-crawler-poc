package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

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
