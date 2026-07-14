package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
)

// importJobResponse mirrors the import job DTO the handlers emit, for decoding in
// assertions. Result is a pointer so `null` (a still-pending job) is
// distinguishable from a zero-valued report.
type importJobResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	DryRun   bool   `json:"dryRun"`
	Filename string `json:"filename"`
	FileSize int64  `json:"fileSize"`
	Result   *struct {
		CompaniesUpserted int `json:"companiesUpserted"`
		PagesUpserted     int `json:"pagesUpserted"`
		Errors            []struct {
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"errors"`
		ErrorCount int `json:"errorCount"`
	} `json:"result"`
	Error string `json:"error"`
}

const validImportLine = `{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"}]}`

// newImportHandler wires a real importer.Importer over the fake repository (via
// newHandler's nil-defaults), returning both so a test can submit and then poll.
func newImportHandler(t *testing.T) (http.Handler, *fakeImportJobRepo) {
	t.Helper()
	repo := newFakeImportJobRepo()
	return newHandler(api.Config{ImportJobs: repo}), repo
}

// newImportRequest builds a multipart/form-data POST carrying body under the
// "file" field, with an optional raw query string.
func newImportRequest(t *testing.T, filename, body, query string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := io.WriteString(fw, body); err != nil {
		t.Fatalf("write file field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	url := "/api/catalog/import"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodPost, url, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// newImportRequestWithKey is newImportRequest plus an Idempotency-Key header.
func newImportRequestWithKey(t *testing.T, filename, body, query, key string) *http.Request {
	t.Helper()
	req := newImportRequest(t, filename, body, query)
	req.Header.Set("Idempotency-Key", key)
	return req
}

func decodeImportJob(t *testing.T, body *bytes.Buffer) importJobResponse {
	t.Helper()
	var got importJobResponse
	if err := json.Unmarshal(body.Bytes(), &got); err != nil {
		t.Fatalf("decode import job %s: %v", body, err)
	}
	return got
}

func TestImportSubmitReturns202PendingJob(t *testing.T) {
	srv, repo := newImportHandler(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", validImportLine, "dryRun=true"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202; body=%s", rec.Code, rec.Body)
	}
	got := decodeImportJob(t, rec.Body)
	if got.Status != "pending" {
		t.Errorf("status: got %q, want pending", got.Status)
	}
	if !got.DryRun {
		t.Error("dryRun: got false, want true")
	}
	if got.Filename != "catalog.ndjson" {
		t.Errorf("filename: got %q, want catalog.ndjson", got.Filename)
	}
	if got.FileSize <= 0 {
		t.Errorf("fileSize: got %d, want > 0", got.FileSize)
	}
	if got.ID == "" {
		t.Error("id: got empty, want a job id")
	}
	if got.Result != nil {
		t.Errorf("pending job must report a null result, got %+v", got.Result)
	}
	// The job was persisted (the poll endpoints can find it).
	if _, err := repo.Get(t.Context(), uuid.MustParse(got.ID)); err != nil {
		t.Errorf("submitted job not persisted: %v", err)
	}
}

func TestImportDryRunReportsWouldUpsertCounters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, _ := newImportHandler(t)

		payload := validImportLine + "\n" +
			`{"companyKey":"globex.com","careerPages":[{"url":"https://globex.com/jobs"},{"url":"https://globex.com/roles"}]}`

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", payload, "dryRun=true"))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		id := decodeImportJob(t, rec.Body).ID

		synctest.Wait()

		got := getImportJob(t, srv, id)
		if got.Status != "completed" {
			t.Fatalf("status: got %q, want completed; error=%q", got.Status, got.Error)
		}
		if got.Result == nil {
			t.Fatal("completed job must carry a result")
		}
		if got.Result.CompaniesUpserted != 2 {
			t.Errorf("companiesUpserted: got %d, want 2", got.Result.CompaniesUpserted)
		}
		if got.Result.PagesUpserted != 3 {
			t.Errorf("pagesUpserted: got %d, want 3", got.Result.PagesUpserted)
		}
		if got.Result.ErrorCount != 0 {
			t.Errorf("errorCount: got %d, want 0", got.Result.ErrorCount)
		}
		if got.Result.Errors == nil {
			t.Error("errors must be a present, non-null array")
		}
	})
}

func TestImportCollectsLineTaggedErrors(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, _ := newImportHandler(t)

		payload := strings.Join([]string{
			validImportLine, // line 1: valid, 1 company / 1 page
			`{bad json`,     // line 2: invalid json
			`{}`,            // line 3: no identity
			// line 4: keyed, so a bad page is sub-line best-effort; company + valid page still count.
			`{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"},{"url":"not-a-url"}]}`,
		}, "\n")

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", payload, ""))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		id := decodeImportJob(t, rec.Body).ID

		synctest.Wait()

		got := getImportJob(t, srv, id)
		if got.Status != "completed" {
			t.Fatalf("status: got %q, want completed", got.Status)
		}
		res := got.Result
		if res == nil {
			t.Fatal("completed job must carry a result")
		}
		// Only the valid data is counted: line 1 and line 4's company + valid page.
		if res.CompaniesUpserted != 2 {
			t.Errorf("companiesUpserted: got %d, want 2", res.CompaniesUpserted)
		}
		if res.PagesUpserted != 2 {
			t.Errorf("pagesUpserted: got %d, want 2", res.PagesUpserted)
		}
		if res.ErrorCount != 3 {
			t.Errorf("errorCount: got %d, want 3", res.ErrorCount)
		}
		gotLines := make([]int, 0, len(res.Errors))
		for _, e := range res.Errors {
			gotLines = append(gotLines, e.Line)
			if e.Message == "" {
				t.Errorf("error on line %d has an empty message", e.Line)
			}
		}
		wantLines := []int{2, 3, 4} // 4 is the sub-line page error whose company still counted
		if len(gotLines) != len(wantLines) {
			t.Fatalf("error lines: got %v, want %v", gotLines, wantLines)
		}
		for i, want := range wantLines {
			if gotLines[i] != want {
				t.Errorf("error line[%d]: got %d, want %d (all: %v)", i, gotLines[i], want, gotLines)
			}
		}
	})
}

func TestImportErrorReportCappedAt100(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, _ := newImportHandler(t)

		payload := strings.Repeat("{}\n", 120) // 120 whole-line errors

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", payload, ""))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		id := decodeImportJob(t, rec.Body).ID

		synctest.Wait()

		got := getImportJob(t, srv, id)
		if got.Result == nil {
			t.Fatal("completed job must carry a result")
		}
		if got.Result.ErrorCount != 120 {
			t.Errorf("errorCount: got %d, want 120 (true total)", got.Result.ErrorCount)
		}
		if len(got.Result.Errors) != 100 {
			t.Errorf("reported errors: got %d, want 100 (capped)", len(got.Result.Errors))
		}
	})
}

// TestImportReplaySameKeyReturns200SameJob covers the idempotent-replay contract:
// resubmitting an identical request under the same Idempotency-Key returns the
// original job (now finished) with 200 instead of forking a duplicate.
func TestImportReplaySameKeyReturns200SameJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, repo := newImportHandler(t)

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequestWithKey(t, "catalog.ndjson", validImportLine, "dryRun=true", "key-abc"))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("first submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		first := decodeImportJob(t, rec.Body)

		// Let the background job finish so the replay observes a terminal job.
		synctest.Wait()

		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequestWithKey(t, "catalog.ndjson", validImportLine, "dryRun=true", "key-abc"))
		if rec.Code != http.StatusOK {
			t.Fatalf("replay status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		replayed := decodeImportJob(t, rec.Body)
		if replayed.ID != first.ID {
			t.Errorf("replay must return the original job: got %s, want %s", replayed.ID, first.ID)
		}
		if replayed.Result == nil {
			t.Error("replay of a finished job must carry its result report")
		}

		jobs, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(jobs) != 1 {
			t.Errorf("a same-key replay must not create a duplicate: got %d jobs, want 1", len(jobs))
		}
	})
}

// TestImportSameKeyDifferentRequestIs422 covers the dangerous-reuse contract: a
// key already bound to one request may not be reused for a different file or
// dry-run flag; either is a 422 and never a second job (ADR-0014).
func TestImportSameKeyDifferentRequestIs422(t *testing.T) {
	t.Run("same key, different file body", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			srv, repo := newImportHandler(t)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, newImportRequestWithKey(t, "catalog.ndjson", validImportLine, "", "key-x"))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("first submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
			}
			synctest.Wait()

			other := `{"companyKey":"globex.com","careerPages":[{"url":"https://globex.com/jobs"}]}`
			rec = httptest.NewRecorder()
			srv.ServeHTTP(rec, newImportRequestWithKey(t, "catalog.ndjson", other, "", "key-x"))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("reuse status: got %d, want 422; body=%s", rec.Code, rec.Body)
			}

			jobs, err := repo.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(jobs) != 1 {
				t.Errorf("a conflicting reuse must not create a job: got %d jobs, want 1", len(jobs))
			}
		})
	})

	t.Run("same file, different dry-run flag", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			srv, repo := newImportHandler(t)

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, newImportRequestWithKey(t, "catalog.ndjson", validImportLine, "dryRun=true", "key-y"))
			if rec.Code != http.StatusAccepted {
				t.Fatalf("first submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
			}
			synctest.Wait()

			// Same bytes, but dryRun flips false -> a different fingerprint.
			rec = httptest.NewRecorder()
			srv.ServeHTTP(rec, newImportRequestWithKey(t, "catalog.ndjson", validImportLine, "", "key-y"))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("flag-changed reuse status: got %d, want 422; body=%s", rec.Code, rec.Body)
			}

			jobs, err := repo.List(t.Context())
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(jobs) != 1 {
				t.Errorf("a flag-changed reuse must not create a job: got %d jobs, want 1", len(jobs))
			}
		})
	})
}

// TestImportWithoutKeyAlwaysCreatesFreshJob covers the keyless contract: with no
// Idempotency-Key header every submit is a distinct 202 job, even byte-identical
// uploads.
func TestImportWithoutKeyAlwaysCreatesFreshJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, repo := newImportHandler(t)

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", validImportLine, ""))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("first submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		first := decodeImportJob(t, rec.Body)

		rec = httptest.NewRecorder()
		srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", validImportLine, ""))
		if rec.Code != http.StatusAccepted {
			t.Fatalf("second submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		second := decodeImportJob(t, rec.Body)

		if first.ID == second.ID {
			t.Errorf("keyless submits must be distinct jobs, got the same id %s", first.ID)
		}

		synctest.Wait()

		jobs, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(jobs) != 2 {
			t.Errorf("two keyless submits must create two jobs: got %d, want 2", len(jobs))
		}
	})
}

func TestImportMalformedUploadIs400(t *testing.T) {
	t.Run("body that is not multipart", func(t *testing.T) {
		srv, repo := newImportHandler(t)

		req := httptest.NewRequest(http.MethodPost, "/api/catalog/import", strings.NewReader(`{"not":"multipart"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
		assertNoJobsCreated(t, repo)
	})

	t.Run("multipart with no file field", func(t *testing.T) {
		srv, repo := newImportHandler(t)

		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		if err := mw.WriteField("notfile", "x"); err != nil {
			t.Fatalf("WriteField: %v", err)
		}
		mw.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/catalog/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
		assertNoJobsCreated(t, repo)
	})
}

func TestListImportJobsNewestFirst(t *testing.T) {
	repo := newFakeImportJobRepo()
	base := time.Now().UTC()
	older := &crawler.ImportJob{ID: uuid.New(), Status: crawler.ImportJobStatusCompleted, CreatedAt: base}
	middle := &crawler.ImportJob{ID: uuid.New(), Status: crawler.ImportJobStatusFailed, CreatedAt: base.Add(time.Hour)}
	newest := &crawler.ImportJob{ID: uuid.New(), Status: crawler.ImportJobStatusPending, CreatedAt: base.Add(2 * time.Hour)}
	for _, j := range []*crawler.ImportJob{older, newest, middle} {
		if err := repo.Create(context.Background(), j); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	srv := newHandler(api.Config{ImportJobs: repo})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/import-jobs", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	var got []importJobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	wantOrder := []string{newest.ID.String(), middle.ID.String(), older.ID.String()}
	if len(got) != len(wantOrder) {
		t.Fatalf("count: got %d, want %d", len(got), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got[i].ID != want {
			t.Errorf("job[%d]: got %s, want %s (newest first)", i, got[i].ID, want)
		}
	}
}

func TestGetImportJobNotFoundAndInvalidID(t *testing.T) {
	srv := newHandler(api.Config{ImportJobs: newFakeImportJobRepo()})

	t.Run("unknown id is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/import-jobs/"+uuid.New().String(), nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("invalid id is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/import-jobs/not-a-uuid", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
	})
}

// getImportJob polls GET /api/catalog/import-jobs/{id} and decodes the DTO.
func getImportJob(t *testing.T, srv http.Handler, id string) importJobResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/import-jobs/"+id, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get import job status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	return decodeImportJob(t, rec.Body)
}

func assertNoJobsCreated(t *testing.T, repo *fakeImportJobRepo) {
	t.Helper()
	jobs, err := repo.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(jobs) != 0 {
		t.Errorf("a malformed upload must not create a job, got %d", len(jobs))
	}
}
