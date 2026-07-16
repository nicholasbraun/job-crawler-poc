package postgres_test

import (
	"errors"
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
		// Each run gets its own definition: running and stopping are both
		// non-terminal, so under a shared definition_id they would collide on the
		// one-active-run index (migration 0010).
		defID := createDefinition(t, pool, "list-by-status-"+string(status))
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

// TestOneActiveRunPerDefinition drives the one-active-run partial unique index
// (migration 0010) through the repository: a definition may carry at most one
// non-terminal run, while terminal runs accumulate freely. The repository
// translates the unique violation into crawler.ErrActiveRunExists (ADR-0017).
func TestOneActiveRunPerDefinition(t *testing.T) {
	pool := newTestPool(t)
	runs := postgres.NewCrawlRunRepository(pool)

	createRun := func(t *testing.T, defID uuid.UUID, status crawler.RunStatus) error {
		t.Helper()
		return runs.Create(t.Context(), &crawler.CrawlRun{
			ID:           uuid.New(),
			DefinitionID: defID,
			Status:       status,
			StartedAt:    time.Now(),
		})
	}

	t.Run("second active run rejected", func(t *testing.T) {
		defID := createDefinition(t, pool, "one-active-basic")
		if err := createRun(t, defID, crawler.RunStatusRunning); err != nil {
			t.Fatalf("first running run should insert: %v", err)
		}
		err := createRun(t, defID, crawler.RunStatusRunning)
		if !errors.Is(err, crawler.ErrActiveRunExists) {
			t.Fatalf("second running run: got %v, want ErrActiveRunExists", err)
		}
	})

	// Every non-terminal status collides with an existing running run, pinning the
	// explicit predicate list running/stopping/pausing/paused.
	for _, second := range []crawler.RunStatus{
		crawler.RunStatusStopping,
		crawler.RunStatusPausing,
		crawler.RunStatusPaused,
	} {
		t.Run("running blocks a second "+string(second), func(t *testing.T) {
			defID := createDefinition(t, pool, "one-active-"+string(second))
			if err := createRun(t, defID, crawler.RunStatusRunning); err != nil {
				t.Fatalf("first running run should insert: %v", err)
			}
			err := createRun(t, defID, second)
			if !errors.Is(err, crawler.ErrActiveRunExists) {
				t.Fatalf("second %s run: got %v, want ErrActiveRunExists", second, err)
			}
		})
	}

	t.Run("terminal runs accumulate", func(t *testing.T) {
		defID := createDefinition(t, pool, "terminal-accumulate")

		// Retire the sole active run, then a fresh active run is admitted again.
		first := &crawler.CrawlRun{
			ID:           uuid.New(),
			DefinitionID: defID,
			Status:       crawler.RunStatusRunning,
			StartedAt:    time.Now(),
		}
		if err := runs.Create(t.Context(), first); err != nil {
			t.Fatalf("first running run should insert: %v", err)
		}
		finishedAt := time.Now()
		if err := runs.UpdateStatus(t.Context(), first.ID, crawler.RunStatusStopped, &finishedAt, ""); err != nil {
			t.Fatalf("retiring the first run: %v", err)
		}
		if err := createRun(t, defID, crawler.RunStatusRunning); err != nil {
			t.Fatalf("a fresh active run should insert once the prior one is terminal: %v", err)
		}

		// A directly-terminal run inserts even alongside the live active run:
		// terminal rows are outside the index predicate.
		if err := createRun(t, defID, crawler.RunStatusStopped); err != nil {
			t.Fatalf("a directly-stopped run should insert alongside the active run: %v", err)
		}
	})

	t.Run("resume-style flip does not trip", func(t *testing.T) {
		defID := createDefinition(t, pool, "resume-flip")
		run := &crawler.CrawlRun{
			ID:           uuid.New(),
			DefinitionID: defID,
			Status:       crawler.RunStatusRunning,
			StartedAt:    time.Now(),
		}
		if err := runs.Create(t.Context(), run); err != nil {
			t.Fatalf("running run should insert: %v", err)
		}
		// A pause then resume is an in-place UPDATE on the one non-terminal row, so
		// it never re-inserts and never trips the index (Reconcile/Resume path).
		if err := runs.UpdateStatus(t.Context(), run.ID, crawler.RunStatusPaused, nil, ""); err != nil {
			t.Fatalf("pausing the run: %v", err)
		}
		if err := runs.UpdateStatus(t.Context(), run.ID, crawler.RunStatusRunning, nil, ""); err != nil {
			t.Fatalf("resuming the run should not trip the index: %v", err)
		}
	})
}
