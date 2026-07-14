package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

func pendingJob(id uuid.UUID, at time.Time) *crawler.ImportJob {
	return &crawler.ImportJob{
		ID:        id,
		Status:    crawler.ImportJobStatusPending,
		DryRun:    true,
		Filename:  "catalog.ndjson",
		FileSize:  1234,
		CreatedAt: at,
		UpdatedAt: at,
	}
}

// keyedJob is a pending job carrying an Idempotency-Key and request fingerprint,
// for the CreateWithKey conflict-arbitration tests.
func keyedJob(id uuid.UUID, at time.Time, key, fingerprint string) *crawler.ImportJob {
	j := pendingJob(id, at)
	j.IdempotencyKey = key
	j.RequestFingerprint = fingerprint
	return j
}

// assertOneJobForKey fails unless exactly one import_job row carries key — the
// invariant CreateWithKey's unique-constraint arbitration must preserve.
func assertOneJobForKey(t *testing.T, pool *pgxpool.Pool, key string) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM import_job WHERE idempotency_key = $1`, key).Scan(&count); err != nil {
		t.Fatalf("count by key: %v", err)
	}
	if count != 1 {
		t.Errorf("rows for key %q: got %d, want exactly 1", key, count)
	}
}

func TestImportJobCreateAndGetRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.Create(t.Context(), pendingJob(id, now)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != crawler.ImportJobStatusPending {
		t.Errorf("status: got %q, want pending", got.Status)
	}
	if !got.DryRun {
		t.Error("dryRun: got false, want true")
	}
	if got.Filename != "catalog.ndjson" {
		t.Errorf("filename: got %q, want catalog.ndjson", got.Filename)
	}
	if got.FileSize != 1234 {
		t.Errorf("fileSize: got %d, want 1234", got.FileSize)
	}
	if got.Result != nil {
		t.Errorf("a pending job must have a nil result, got %+v", got.Result)
	}
	if !got.CreatedAt.Equal(now) {
		t.Errorf("createdAt: got %v, want %v", got.CreatedAt, now)
	}
}

func TestImportJobResultJSONRoundTrip(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	id := uuid.New()
	now := time.Now().UTC()
	if err := repo.Create(t.Context(), pendingJob(id, now)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	completed := pendingJob(id, now)
	completed.Status = crawler.ImportJobStatusCompleted
	completed.UpdatedAt = now.Add(time.Second)
	completed.Result = &crawler.ImportResult{
		CompaniesUpserted: 5,
		PagesUpserted:     9,
		Errors: []crawler.ImportError{
			{Line: 2, Message: "invalid json: unexpected end of input"},
			{Line: 7, Message: "career page url \"x\": must be an absolute http(s) url"},
		},
		// ErrorCount deliberately exceeds len(Errors): the report was capped.
		ErrorCount: 42,
	}
	if err := repo.Update(t.Context(), completed); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := repo.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Result == nil {
		t.Fatal("completed job must carry a result")
	}
	if got.Result.CompaniesUpserted != 5 || got.Result.PagesUpserted != 9 {
		t.Errorf("counters: got %d/%d, want 5/9", got.Result.CompaniesUpserted, got.Result.PagesUpserted)
	}
	if got.Result.ErrorCount != 42 {
		t.Errorf("errorCount: got %d, want 42", got.Result.ErrorCount)
	}
	if len(got.Result.Errors) != 2 {
		t.Fatalf("errors: got %d, want 2", len(got.Result.Errors))
	}
	if got.Result.Errors[0].Line != 2 || got.Result.Errors[0].Message != "invalid json: unexpected end of input" {
		t.Errorf("errors[0]: got %+v", got.Result.Errors[0])
	}
	if got.Result.Errors[1].Line != 7 {
		t.Errorf("errors[1].Line: got %d, want 7", got.Result.Errors[1].Line)
	}
}

func TestImportJobStatusTransitions(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	id := uuid.New()
	now := time.Now().UTC()
	if err := repo.Create(t.Context(), pendingJob(id, now)); err != nil {
		t.Fatalf("Create: %v", err)
	}

	// pending -> running: still no result.
	running := pendingJob(id, now)
	running.Status = crawler.ImportJobStatusRunning
	running.UpdatedAt = now.Add(time.Second)
	if err := repo.Update(t.Context(), running); err != nil {
		t.Fatalf("Update to running: %v", err)
	}
	got, err := repo.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get after running: %v", err)
	}
	if got.Status != crawler.ImportJobStatusRunning {
		t.Errorf("status: got %q, want running", got.Status)
	}
	if got.Result != nil {
		t.Errorf("running job must have no result, got %+v", got.Result)
	}

	// running -> completed: result set.
	done := pendingJob(id, now)
	done.Status = crawler.ImportJobStatusCompleted
	done.UpdatedAt = now.Add(2 * time.Second)
	done.Result = &crawler.ImportResult{CompaniesUpserted: 1, Errors: []crawler.ImportError{}}
	if err := repo.Update(t.Context(), done); err != nil {
		t.Fatalf("Update to completed: %v", err)
	}
	got, err = repo.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get after completed: %v", err)
	}
	if got.Status != crawler.ImportJobStatusCompleted {
		t.Errorf("status: got %q, want completed", got.Status)
	}
	if got.Result == nil || got.Result.CompaniesUpserted != 1 {
		t.Errorf("result: got %+v, want 1 company", got.Result)
	}
}

func TestImportJobListNewestFirst(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	base := time.Now().UTC().Truncate(time.Microsecond)
	oldID, midID, newID := uuid.New(), uuid.New(), uuid.New()
	// Insert out of chronological order to prove List sorts, not insertion order.
	if err := repo.Create(t.Context(), pendingJob(midID, base.Add(1*time.Hour))); err != nil {
		t.Fatalf("Create mid: %v", err)
	}
	if err := repo.Create(t.Context(), pendingJob(oldID, base)); err != nil {
		t.Fatalf("Create old: %v", err)
	}
	if err := repo.Create(t.Context(), pendingJob(newID, base.Add(2*time.Hour))); err != nil {
		t.Fatalf("Create new: %v", err)
	}

	jobs, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 3 {
		t.Fatalf("List count: got %d, want 3", len(jobs))
	}
	wantOrder := []uuid.UUID{newID, midID, oldID}
	for i, want := range wantOrder {
		if jobs[i].ID != want {
			t.Errorf("List[%d]: got %s, want %s (newest first)", i, jobs[i].ID, want)
		}
	}
}

func TestImportJobGetUnknownReturnsNotFound(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	_, err := repo.Get(t.Context(), uuid.New())
	if !errors.Is(err, crawler.ErrNotFound) {
		t.Fatalf("Get unknown: got %v, want ErrNotFound", err)
	}
}

func TestImportJobSweepInterrupted(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	base := time.Now().UTC()
	seed := map[crawler.ImportJobStatus]uuid.UUID{
		crawler.ImportJobStatusPending:   uuid.New(),
		crawler.ImportJobStatusRunning:   uuid.New(),
		crawler.ImportJobStatusCompleted: uuid.New(),
		crawler.ImportJobStatusFailed:    uuid.New(),
	}
	for status, id := range seed {
		job := pendingJob(id, base)
		job.Status = status
		if status == crawler.ImportJobStatusFailed {
			job.Error = "original failure"
		}
		if status == crawler.ImportJobStatusCompleted {
			job.Result = &crawler.ImportResult{CompaniesUpserted: 3, Errors: []crawler.ImportError{}}
		}
		if err := repo.Create(t.Context(), job); err != nil {
			t.Fatalf("seeding %s: %v", status, err)
		}
	}

	const msg = "interrupted by server restart; re-upload the file"
	sweptAt := base.Add(time.Hour).Truncate(time.Microsecond)
	n, err := repo.SweepInterrupted(t.Context(), msg, sweptAt)
	if err != nil {
		t.Fatalf("SweepInterrupted: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows swept: got %d, want 2 (pending + running)", n)
	}

	// The former pending and running are now failed with the sweep message.
	for _, status := range []crawler.ImportJobStatus{crawler.ImportJobStatusPending, crawler.ImportJobStatusRunning} {
		got, err := repo.Get(t.Context(), seed[status])
		if err != nil {
			t.Fatalf("Get swept %s: %v", status, err)
		}
		if got.Status != crawler.ImportJobStatusFailed {
			t.Errorf("swept %s status: got %q, want failed", status, got.Status)
		}
		if got.Error != msg {
			t.Errorf("swept %s error: got %q, want %q", status, got.Error, msg)
		}
		if !got.UpdatedAt.Equal(sweptAt) {
			t.Errorf("swept %s updatedAt: got %v, want %v", status, got.UpdatedAt, sweptAt)
		}
	}

	// completed and failed are untouched.
	completed, err := repo.Get(t.Context(), seed[crawler.ImportJobStatusCompleted])
	if err != nil {
		t.Fatalf("Get completed: %v", err)
	}
	if completed.Status != crawler.ImportJobStatusCompleted {
		t.Errorf("completed job must be untouched, got status %q", completed.Status)
	}
	failed, err := repo.Get(t.Context(), seed[crawler.ImportJobStatusFailed])
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if failed.Error != "original failure" {
		t.Errorf("pre-failed job's error must be untouched, got %q", failed.Error)
	}
}

func TestImportJobCreateWithKeyFreshInsert(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	id := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	stored, replay, err := repo.CreateWithKey(t.Context(), keyedJob(id, now, "key-1", "fp-1"))
	if err != nil {
		t.Fatalf("CreateWithKey: %v", err)
	}
	if replay {
		t.Error("a fresh insert must not report a replay")
	}
	if stored.ID != id {
		t.Errorf("returned id: got %s, want %s", stored.ID, id)
	}

	// The idempotency columns must round-trip through Get -> scanImportJob.
	got, err := repo.Get(t.Context(), id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.IdempotencyKey != "key-1" {
		t.Errorf("idempotencyKey: got %q, want key-1", got.IdempotencyKey)
	}
	if got.RequestFingerprint != "fp-1" {
		t.Errorf("requestFingerprint: got %q, want fp-1", got.RequestFingerprint)
	}
}

func TestImportJobCreateWithKeyReplayReturnsOriginal(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	now := time.Now().UTC()
	original := uuid.New()
	if _, _, err := repo.CreateWithKey(t.Context(), keyedJob(original, now, "key-2", "fp-2")); err != nil {
		t.Fatalf("first CreateWithKey: %v", err)
	}

	// A retry mints a fresh candidate id but carries the same key + fingerprint;
	// the arbitration must return the original job, not the candidate.
	stored, replay, err := repo.CreateWithKey(t.Context(), keyedJob(uuid.New(), now, "key-2", "fp-2"))
	if err != nil {
		t.Fatalf("replay CreateWithKey: %v", err)
	}
	if !replay {
		t.Error("a same-key, same-fingerprint retry must report a replay")
	}
	if stored.ID != original {
		t.Errorf("replay must return the original job: got %s, want %s", stored.ID, original)
	}
	assertOneJobForKey(t, pool, "key-2")
}

func TestImportJobCreateWithKeyFingerprintMismatchIsConflict(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)

	now := time.Now().UTC()
	if _, _, err := repo.CreateWithKey(t.Context(), keyedJob(uuid.New(), now, "key-3", "fp-A")); err != nil {
		t.Fatalf("first CreateWithKey: %v", err)
	}

	_, _, err := repo.CreateWithKey(t.Context(), keyedJob(uuid.New(), now, "key-3", "fp-B"))
	if !errors.Is(err, crawler.ErrIdempotencyKeyConflict) {
		t.Fatalf("reuse with a different fingerprint: got %v, want ErrIdempotencyKeyConflict", err)
	}
	assertOneJobForKey(t, pool, "key-3")
}

// TestImportJobCreateWithKeyConcurrentInsertsExactlyOne is the race-safety
// criterion: many concurrent same-key submissions must resolve to exactly one
// fresh insert, with every other caller observing that winner as a replay.
func TestImportJobCreateWithKeyConcurrentInsertsExactlyOne(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewImportJobRepository(pool)
	ctx := t.Context()

	const n = 8
	now := time.Now().UTC()
	type outcome struct {
		id     uuid.UUID
		replay bool
		err    error
	}
	results := make([]outcome, n)
	var wg sync.WaitGroup
	start := make(chan struct{}) // release all goroutines together for max contention
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			got, replay, err := repo.CreateWithKey(ctx, keyedJob(uuid.New(), now, "race-key", "race-fp"))
			if err != nil {
				results[i] = outcome{err: err}
				return
			}
			results[i] = outcome{id: got.ID, replay: replay}
		}(i)
	}
	close(start)
	wg.Wait()

	fresh := 0
	var winner uuid.UUID
	for _, r := range results {
		if r.err != nil {
			t.Fatalf("concurrent CreateWithKey errored: %v", r.err)
		}
		if !r.replay {
			fresh++
			winner = r.id
		}
	}
	if fresh != 1 {
		t.Fatalf("exactly one submission must create the job, got %d fresh inserts", fresh)
	}
	for _, r := range results {
		if r.id != winner {
			t.Errorf("every caller must observe the winner %s, got %s", winner, r.id)
		}
	}
	assertOneJobForKey(t, pool, "race-key")
}
