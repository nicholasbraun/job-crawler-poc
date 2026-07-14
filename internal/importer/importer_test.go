package importer_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/importer"
)

// fakeImportJobRepo is a mutex-guarded in-memory ImportJobRepository. Besides the
// job store it records the ordered sequence of statuses each id passed through
// (Create + every Update), so a test can assert the pending->running->terminal
// lifecycle without timing.
type fakeImportJobRepo struct {
	mu   sync.Mutex
	jobs map[uuid.UUID]*crawler.ImportJob
	seq  map[uuid.UUID][]crawler.ImportJobStatus

	createErr error
	updateErr error
	sweepErr  error
	swept     int64
}

func newFakeImportJobRepo() *fakeImportJobRepo {
	return &fakeImportJobRepo{
		jobs: map[uuid.UUID]*crawler.ImportJob{},
		seq:  map[uuid.UUID][]crawler.ImportJobStatus{},
	}
}

func (f *fakeImportJobRepo) Create(ctx context.Context, job *crawler.ImportJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	stored := *job
	f.jobs[job.ID] = &stored
	f.seq[job.ID] = append(f.seq[job.ID], job.Status)
	return nil
}

func (f *fakeImportJobRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	job, ok := f.jobs[id]
	if !ok {
		return nil, crawler.ErrNotFound
	}
	copied := *job
	return &copied, nil
}

func (f *fakeImportJobRepo) List(ctx context.Context) ([]*crawler.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	jobs := make([]*crawler.ImportJob, 0, len(f.jobs))
	for _, j := range f.jobs {
		copied := *j
		jobs = append(jobs, &copied)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (f *fakeImportJobRepo) Update(ctx context.Context, job *crawler.ImportJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updateErr != nil {
		return f.updateErr
	}
	stored := *job
	f.jobs[job.ID] = &stored
	f.seq[job.ID] = append(f.seq[job.ID], job.Status)
	return nil
}

func (f *fakeImportJobRepo) SweepInterrupted(ctx context.Context, msg string, at time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sweepErr != nil {
		return 0, f.sweepErr
	}
	return f.swept, nil
}

func (f *fakeImportJobRepo) statusSeq(id uuid.UUID) []crawler.ImportJobStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seq[id])
}

// runImport submits payload, waits for the background job to finish, and returns
// the completed job's Result. It must be called inside a synctest bubble.
func runImport(t *testing.T, im *importer.Importer, repo *fakeImportJobRepo, payload string) *crawler.ImportJob {
	t.Helper()
	job, err := im.Submit(t.Context(), "catalog.ndjson", []byte(payload), false)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	synctest.Wait()
	got, err := repo.Get(t.Context(), job.ID)
	if err != nil {
		t.Fatalf("Get after completion: %v", err)
	}
	return got
}

const validKeyedOnePage = `{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"}]}`

func TestExecuteImportCountsAndCollectsErrors(t *testing.T) {
	tests := []struct {
		name          string
		payload       string
		wantCompanies int
		wantPages     int
		wantErrCount  int
		wantErrLines  []int
	}{
		{
			name: "two valid companies",
			payload: validKeyedOnePage + "\n" +
				`{"companyKey":"globex.com","careerPages":[{"url":"https://globex.com/jobs"},{"url":"https://globex.com/roles"}]}`,
			wantCompanies: 2,
			wantPages:     3,
			wantErrCount:  0,
		},
		{
			name:          "invalid json line still counts the valid ones",
			payload:       validKeyedOnePage + "\n" + `{bad json`,
			wantCompanies: 1,
			wantPages:     1,
			wantErrCount:  1,
			wantErrLines:  []int{2},
		},
		{
			name:          "keyless record with no identity is a whole-line error",
			payload:       `{}`,
			wantCompanies: 0,
			wantPages:     0,
			wantErrCount:  1,
			wantErrLines:  []int{1},
		},
		{
			name:          "keyless pages deriving two companies is a whole-line error",
			payload:       `{"careerPages":[{"url":"https://boards.greenhouse.io/acme"},{"url":"https://jobs.lever.co/globex"}]}`,
			wantCompanies: 0,
			wantPages:     0,
			wantErrCount:  1,
			wantErrLines:  []int{1},
		},
		{
			name:          "keyed record with a bad page is sub-line best-effort",
			payload:       `{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"},{"url":"not-a-url"}]}`,
			wantCompanies: 1,
			wantPages:     1,
			wantErrCount:  1,
			wantErrLines:  []int{1},
		},
		{
			name:          "blank and whitespace lines are skipped and keep line numbers aligned",
			payload:       validKeyedOnePage + "\n\n   \n" + `{bad json`,
			wantCompanies: 1,
			wantPages:     1,
			wantErrCount:  1,
			wantErrLines:  []int{4},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				repo := newFakeImportJobRepo()
				im := importer.New(repo)
				job := runImport(t, im, repo, tt.payload)

				if job.Status != crawler.ImportJobStatusCompleted {
					t.Fatalf("status: got %q, want completed; error=%q", job.Status, job.Error)
				}
				res := job.Result
				if res == nil {
					t.Fatal("completed job must carry a result")
				}
				if res.CompaniesUpserted != tt.wantCompanies {
					t.Errorf("companiesUpserted: got %d, want %d", res.CompaniesUpserted, tt.wantCompanies)
				}
				if res.PagesUpserted != tt.wantPages {
					t.Errorf("pagesUpserted: got %d, want %d", res.PagesUpserted, tt.wantPages)
				}
				if res.ErrorCount != tt.wantErrCount {
					t.Errorf("errorCount: got %d, want %d (errors=%v)", res.ErrorCount, tt.wantErrCount, res.Errors)
				}
				if res.Errors == nil {
					t.Error("Errors must be a non-nil slice, never null")
				}
				var gotLines []int
				for _, e := range res.Errors {
					gotLines = append(gotLines, e.Line)
					if e.Message == "" {
						t.Errorf("error on line %d has an empty message", e.Line)
					}
				}
				if !slices.Equal(gotLines, tt.wantErrLines) {
					t.Errorf("error lines: got %v, want %v", gotLines, tt.wantErrLines)
				}
			})
		})
	}
}

func TestSubmitRunsPendingToCompletion(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newFakeImportJobRepo()
		im := importer.New(repo)

		job, err := im.Submit(t.Context(), "catalog.ndjson", []byte(validKeyedOnePage), false)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		// The returned snapshot is the initial pending state.
		if job.Status != crawler.ImportJobStatusPending {
			t.Errorf("returned job status: got %q, want pending", job.Status)
		}

		synctest.Wait()

		stored, err := repo.Get(t.Context(), job.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if stored.Status != crawler.ImportJobStatusCompleted {
			t.Fatalf("stored status: got %q, want completed", stored.Status)
		}
		if stored.Result == nil || stored.Result.CompaniesUpserted != 1 || stored.Result.PagesUpserted != 1 {
			t.Errorf("result: got %+v, want 1 company / 1 page", stored.Result)
		}

		want := []crawler.ImportJobStatus{
			crawler.ImportJobStatusPending,
			crawler.ImportJobStatusRunning,
			crawler.ImportJobStatusCompleted,
		}
		if got := repo.statusSeq(job.ID); !slices.Equal(got, want) {
			t.Errorf("status sequence: got %v, want %v", got, want)
		}
	})
}

func TestSubmitFailsJobOnInfrastructureError(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newFakeImportJobRepo()
		im := importer.New(repo, importer.WithExecutor(
			func(context.Context, []byte, bool) (crawler.ImportResult, error) {
				return crawler.ImportResult{}, errors.New("boom")
			}))

		job, err := im.Submit(t.Context(), "catalog.ndjson", []byte(validKeyedOnePage), false)
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		synctest.Wait()

		stored, err := repo.Get(t.Context(), job.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if stored.Status != crawler.ImportJobStatusFailed {
			t.Fatalf("status: got %q, want failed", stored.Status)
		}
		if stored.Error != "boom" {
			t.Errorf("error text: got %q, want %q", stored.Error, "boom")
		}
		if stored.Result != nil {
			t.Errorf("failed job must not carry a result, got %+v", stored.Result)
		}

		want := []crawler.ImportJobStatus{
			crawler.ImportJobStatusPending,
			crawler.ImportJobStatusRunning,
			crawler.ImportJobStatusFailed,
		}
		if got := repo.statusSeq(job.ID); !slices.Equal(got, want) {
			t.Errorf("status sequence: got %v, want %v", got, want)
		}
	})
}

func TestSubmitReportsErrorCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newFakeImportJobRepo()
		im := importer.New(repo)

		// 150 whole-line-error lines ("{}" has no identity).
		payload := strings.Repeat("{}\n", 150)
		job := runImport(t, im, repo, payload)

		if job.Status != crawler.ImportJobStatusCompleted {
			t.Fatalf("status: got %q, want completed", job.Status)
		}
		res := job.Result
		if res.ErrorCount != 150 {
			t.Errorf("errorCount: got %d, want 150 (true total)", res.ErrorCount)
		}
		if len(res.Errors) != 100 {
			t.Errorf("reported errors: got %d, want 100 (capped)", len(res.Errors))
		}
		if res.Errors[0].Line != 1 {
			t.Errorf("first reported error line: got %d, want 1", res.Errors[0].Line)
		}
	})
}

// TestExecutionIsSerialized proves the size-1 semaphore lets at most one job run
// at a time: while the first job is parked inside its executor, a second submit
// stays blocked on the semaphore rather than entering the executor concurrently.
func TestExecutionIsSerialized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		repo := newFakeImportJobRepo()

		var running, maxRunning atomic.Int32
		gate := make(chan struct{})
		exec := func(context.Context, []byte, bool) (crawler.ImportResult, error) {
			n := running.Add(1)
			for {
				m := maxRunning.Load()
				if n <= m || maxRunning.CompareAndSwap(m, n) {
					break
				}
			}
			<-gate
			running.Add(-1)
			return crawler.ImportResult{}, nil
		}
		im := importer.New(repo, importer.WithExecutor(exec))

		if _, err := im.Submit(t.Context(), "a.ndjson", []byte(validKeyedOnePage), false); err != nil {
			t.Fatalf("Submit a: %v", err)
		}
		if _, err := im.Submit(t.Context(), "b.ndjson", []byte(validKeyedOnePage), false); err != nil {
			t.Fatalf("Submit b: %v", err)
		}

		// Both goroutines are launched; one holds the slot and is parked on the
		// gate, the other is blocked on the size-1 semaphore.
		synctest.Wait()
		if got := running.Load(); got != 1 {
			t.Fatalf("concurrently running executors: got %d, want 1 (second blocked on the sem)", got)
		}

		close(gate)
		synctest.Wait()
		if got := maxRunning.Load(); got != 1 {
			t.Errorf("max concurrent executors: got %d, want 1 (execution must serialize)", got)
		}
	})
}
