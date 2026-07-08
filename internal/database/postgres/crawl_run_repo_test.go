package postgres_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

func TestFailInterrupted(t *testing.T) {
	pool := newTestPool(t)
	runs := postgres.NewCrawlRunRepository(pool)
	defID := createDefinition(t, pool, "reconcile-test")

	// Seed one run per status. Only running and stopping should be reconciled;
	// the terminal ones must be left untouched.
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
		run := &crawler.CrawlRun{
			ID:           id,
			DefinitionID: defID,
			Status:       status,
			StartedAt:    time.Now(),
		}
		if err := runs.Create(t.Context(), run); err != nil {
			t.Fatalf("error seeding %s run: %v", status, err)
		}
	}

	ids, err := runs.FailInterrupted(t.Context(), "interrupted by server restart")
	if err != nil {
		t.Fatalf("FailInterrupted: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("reconciled count: got %d, want 2 (running + stopping)", len(ids))
	}
	// The returned IDs must be exactly the two non-terminal runs.
	wantIDs := map[uuid.UUID]bool{
		seed[crawler.RunStatusRunning]:  true,
		seed[crawler.RunStatusStopping]: true,
	}
	for _, id := range ids {
		if !wantIDs[id] {
			t.Errorf("unexpected reconciled id %s", id)
		}
	}

	assertStatus := func(status crawler.RunStatus, wantStatus crawler.RunStatus, wantErr bool) {
		t.Helper()
		got, err := runs.Get(t.Context(), seed[status])
		if err != nil {
			t.Fatalf("Get %s run: %v", status, err)
		}
		if got.Status != wantStatus {
			t.Errorf("run seeded as %s: status got %s, want %s", status, got.Status, wantStatus)
		}
		if wantErr {
			if got.Error == "" {
				t.Errorf("run seeded as %s: expected an error message", status)
			}
			if got.FinishedAt == nil {
				t.Errorf("run seeded as %s: expected finished_at to be set", status)
			}
		}
	}

	// Non-terminal runs are now failed with a reason and a finish time.
	assertStatus(crawler.RunStatusRunning, crawler.RunStatusFailed, true)
	assertStatus(crawler.RunStatusStopping, crawler.RunStatusFailed, true)
	// Terminal runs are left exactly as they were.
	assertStatus(crawler.RunStatusStopped, crawler.RunStatusStopped, false)
	assertStatus(crawler.RunStatusCompleted, crawler.RunStatusCompleted, false)
	assertStatus(crawler.RunStatusFailed, crawler.RunStatusFailed, false)
}
