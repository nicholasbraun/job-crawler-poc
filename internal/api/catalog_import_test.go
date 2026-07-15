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
	"github.com/nicholasbraun/job-crawler-poc/internal/importer"
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

func TestImportOversizeUploadIs413(t *testing.T) {
	srv, repo := newImportHandler(t)

	// A file field one byte over the cap; with multipart framing the body
	// exceeds MaxBytesReader's limit mid-parse.
	oversize := strings.Repeat("x", 32<<20+1)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newImportRequest(t, "huge.ndjson", oversize, ""))

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status: got %d, want 413; body=%s", rec.Code, rec.Body)
	}
	var got struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error body %s: %v", rec.Body, err)
	}
	if !strings.Contains(got.Error, "32 MB") {
		t.Errorf("error message should name the limit, got %q", got.Error)
	}
	assertNoJobsCreated(t, repo)
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

// newMergeImportHandler wires a real importer.Importer with the Catalog-merging
// executor (NewMergeExecutor) over faithful fakes, so submit -> poll -> result
// exercises the real merge write path end-to-end. The one job repo is shared as
// both the importer's store and the read-endpoint source, matching the server.
func newMergeImportHandler(t *testing.T) (http.Handler, *fakeCompanyRepo, *fakeCareerPageRepo) {
	t.Helper()
	jobs := newFakeImportJobRepo()
	companies := &fakeCompanyRepo{}
	pages := &fakeCareerPageRepo{}
	im := importer.New(jobs, importer.WithExecutor(importer.NewMergeExecutor(companies, pages)))
	srv := newHandler(api.Config{ImportJobs: jobs, Importer: im, Companies: companies, CareerPages: pages})
	return srv, companies, pages
}

// twoCompanyThreePageImport is a real import file (explicit timestamps, a
// trailing-slash URL) used by the real-run and idempotency tests.
const twoCompanyThreePageImport = `{"companyKey":"acme.com","name":"Acme","firstSeen":"2025-01-01T00:00:00Z","lastSeen":"2025-06-01T00:00:00Z","careerPages":[{"url":"https://acme.com/careers/","firstSeen":"2025-01-01T00:00:00Z","lastSeen":"2025-06-01T00:00:00Z"},{"url":"https://acme.com/jobs","firstSeen":"2025-02-01T00:00:00Z","lastSeen":"2025-06-01T00:00:00Z"}]}
{"companyKey":"globex.com","name":"Globex","firstSeen":"2025-03-01T00:00:00Z","lastSeen":"2025-07-01T00:00:00Z","careerPages":[{"url":"https://globex.com/jobs","firstSeen":"2025-03-01T00:00:00Z","lastSeen":"2025-07-01T00:00:00Z"}]}`

// submitAndAwaitImport submits a real (non-dry-run) import and returns the
// completed job. Must run inside a synctest bubble.
func submitAndAwaitImport(t *testing.T, srv http.Handler, body string) importJobResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, newImportRequest(t, "catalog.ndjson", body, ""))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("submit status: got %d, want 202; body=%s", rec.Code, rec.Body)
	}
	id := decodeImportJob(t, rec.Body).ID
	synctest.Wait()
	got := getImportJob(t, srv, id)
	if got.Status != "completed" {
		t.Fatalf("status: got %q, want completed; error=%q", got.Status, got.Error)
	}
	return got
}

func TestImportRealRunLandsCatalogRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, companies, pages := newMergeImportHandler(t)

		got := submitAndAwaitImport(t, srv, twoCompanyThreePageImport)
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

		catalogCompanies, err := companies.List(t.Context())
		if err != nil {
			t.Fatalf("list companies: %v", err)
		}
		byKey := map[string]*crawler.Company{}
		for _, c := range catalogCompanies {
			byKey[c.CompanyKey] = c
		}
		if len(byKey) != 2 || byKey["acme.com"] == nil || byKey["globex.com"] == nil {
			t.Fatalf("catalog should hold acme.com and globex.com, got %v", byKey)
		}
		// User story 5: the imported first_seen survives verbatim (not stamped to
		// import day).
		wantAcmeFirstSeen := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		if fs := byKey["acme.com"].FirstSeen; !fs.Equal(wantAcmeFirstSeen) {
			t.Errorf("acme first_seen: got %v, want %v", fs, wantAcmeFirstSeen)
		}

		catalogPages, err := pages.List(t.Context())
		if err != nil {
			t.Fatalf("list pages: %v", err)
		}
		if len(catalogPages) != 3 {
			t.Fatalf("catalog should hold 3 career pages, got %d", len(catalogPages))
		}
		gotURLs := map[string]bool{}
		for _, p := range catalogPages {
			gotURLs[p.URL] = true
		}
		// The trailing-slash acme/careers URL is stored canonicalised.
		if !gotURLs["https://acme.com/careers"] {
			t.Errorf("acme careers page should be canonicalised (no trailing slash); got %v", gotURLs)
		}
	})
}

func TestImportSameFileTwiceIsIdempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, companies, pages := newMergeImportHandler(t)

		first := submitAndAwaitImport(t, srv, twoCompanyThreePageImport)

		companiesAfterFirst := snapshotCompanies(t, companies)
		pagesAfterFirst := snapshotPages(t, pages)

		second := submitAndAwaitImport(t, srv, twoCompanyThreePageImport)

		// Both runs report identical counters (the Result struct carries a slice,
		// so compare the scalar counters field by field).
		if first.Result == nil || second.Result == nil {
			t.Fatal("both jobs must carry a result")
		}
		if first.Result.CompaniesUpserted != second.Result.CompaniesUpserted ||
			first.Result.PagesUpserted != second.Result.PagesUpserted ||
			first.Result.ErrorCount != second.Result.ErrorCount {
			t.Errorf("counters differ between runs: first=%+v second=%+v", first.Result, second.Result)
		}

		// No duplicate rows: the same file re-uploaded converges to a no-op.
		companiesAfterSecond := snapshotCompanies(t, companies)
		pagesAfterSecond := snapshotPages(t, pages)
		if len(companiesAfterSecond) != len(companiesAfterFirst) {
			t.Errorf("company count changed on re-import: was %d, now %d", len(companiesAfterFirst), len(companiesAfterSecond))
		}
		if len(pagesAfterSecond) != len(pagesAfterFirst) {
			t.Errorf("page count changed on re-import: was %d, now %d", len(pagesAfterFirst), len(pagesAfterSecond))
		}

		// The not-a-Sighting property, end-to-end: re-importing advances no
		// timestamp.
		for key, ts := range companiesAfterFirst {
			after, ok := companiesAfterSecond[key]
			if !ok {
				t.Errorf("company %q vanished on re-import", key)
				continue
			}
			if !after.first.Equal(ts.first) || !after.last.Equal(ts.last) {
				t.Errorf("company %q timestamps changed: was (%v,%v), now (%v,%v)", key, ts.first, ts.last, after.first, after.last)
			}
		}
		for key, ts := range pagesAfterFirst {
			after, ok := pagesAfterSecond[key]
			if !ok {
				t.Errorf("page %q vanished on re-import", key)
				continue
			}
			if !after.first.Equal(ts.first) || !after.last.Equal(ts.last) {
				t.Errorf("page %q timestamps changed: was (%v,%v), now (%v,%v)", key, ts.first, ts.last, after.first, after.last)
			}
		}
	})
}

type timestamps struct{ first, last time.Time }

// snapshotCompanies reads the catalog's companies keyed by CompanyKey with their
// first/last seen, for before/after idempotency comparison.
func snapshotCompanies(t *testing.T, repo *fakeCompanyRepo) map[string]timestamps {
	t.Helper()
	list, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("list companies: %v", err)
	}
	out := map[string]timestamps{}
	for _, c := range list {
		out[c.CompanyKey] = timestamps{first: c.FirstSeen, last: c.LastSeen}
	}
	return out
}

// snapshotPages reads the catalog's career pages keyed by URL with their
// first/last seen.
func snapshotPages(t *testing.T, repo *fakeCareerPageRepo) map[string]timestamps {
	t.Helper()
	list, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("list pages: %v", err)
	}
	out := map[string]timestamps{}
	for _, p := range list {
		out[p.URL] = timestamps{first: p.FirstSeen, last: p.LastSeen}
	}
	return out
}

// companyByKey reads the catalog and returns the company with the given key, or
// nil when absent.
func companyByKey(t *testing.T, repo *fakeCompanyRepo, key string) *crawler.Company {
	t.Helper()
	list, err := repo.List(t.Context())
	if err != nil {
		t.Fatalf("list companies: %v", err)
	}
	for _, c := range list {
		if c.CompanyKey == key {
			return c
		}
	}
	return nil
}

// TestImportPersistsWebsitePresenceWins covers AC bullet 3 end-to-end: a keyed
// record's website lands, a keyless website-only record (Identity Ladder rung 2)
// derives its identity and displayDomain from the website and stores it, and a
// later sparse re-import that omits the website never blanks the catalogued one.
func TestImportPersistsWebsitePresenceWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, companies, _ := newMergeImportHandler(t)

		keyed := `{"companyKey":"acme.com","website":"https://acme.com","careerPages":[{"url":"https://acme.com/careers"}]}`
		// Keyless + website-only: identity derives from the website's registrable
		// domain (rung 2), and displayDomain fills from it too.
		keyless := `{"name":"Initech","website":"https://initech.io","careerPages":[]}`
		submitAndAwaitImport(t, srv, keyed+"\n"+keyless)

		acme := companyByKey(t, companies, "acme.com")
		if acme == nil {
			t.Fatal("acme.com missing from catalog")
		}
		if acme.Website != "https://acme.com" {
			t.Errorf("acme website: got %q, want https://acme.com", acme.Website)
		}

		initech := companyByKey(t, companies, "initech.io")
		if initech == nil {
			t.Fatalf("keyless record should resolve to key initech.io; catalog missing it")
		}
		if initech.Website != "https://initech.io" {
			t.Errorf("initech website: got %q, want https://initech.io", initech.Website)
		}
		if initech.DisplayDomain != "initech.io" {
			t.Errorf("initech displayDomain should fill from the website registrable domain: got %q, want initech.io", initech.DisplayDomain)
		}

		// Presence-wins: a sparse re-import that omits website must not blank it.
		reimport := `{"companyKey":"acme.com","careerPages":[{"url":"https://acme.com/careers"}]}`
		submitAndAwaitImport(t, srv, reimport)
		if acme := companyByKey(t, companies, "acme.com"); acme == nil || acme.Website != "https://acme.com" {
			t.Errorf("sparse re-import blanked the website: got %v", acme)
		}
	})
}

// websiteRoundTripExport is a full-fidelity Catalog Export: every field is
// present, so importing it and re-exporting reproduces it byte-for-byte. Companies
// are in export order (companyKey ascending: acme.com < greenhouse:zeta <
// initech.io). It exercises the website round trip in every shape — present
// (acme, initech) and absent (zeta) — plus displayDomain-from-website. Only
// full-fidelity lines are byte-stable: a minimal record would gain derivation
// defaults on import that a second export then emits, diverging.
const websiteRoundTripExport = `{"companyKey":"acme.com","atsProvider":"","name":"Acme","displayDomain":"acme.com","website":"https://acme.com","firstSeen":"2025-01-01T00:00:00Z","lastSeen":"2025-06-01T00:00:00Z","careerPages":[{"url":"https://acme.com/careers","firstSeen":"2025-01-01T00:00:00Z","lastSeen":"2025-06-01T00:00:00Z"}]}
{"companyKey":"greenhouse:zeta","atsProvider":"greenhouse","name":"Zeta","displayDomain":"zeta.com","firstSeen":"2025-03-01T00:00:00Z","lastSeen":"2025-07-01T00:00:00Z","careerPages":[{"url":"https://boards.greenhouse.io/zeta","firstSeen":"2025-03-01T00:00:00Z","lastSeen":"2025-07-01T00:00:00Z"}]}
{"companyKey":"initech.io","atsProvider":"","name":"Initech","displayDomain":"initech.io","website":"https://initech.io","firstSeen":"2025-02-01T00:00:00Z","lastSeen":"2025-02-01T00:00:00Z","careerPages":[]}
`

// TestCatalogExportImportRoundTripByteIdentical satisfies the AC "an export →
// import → export round trip is byte-identical including website": importing a
// full-fidelity export and re-exporting must reproduce it verbatim — website
// present survives, website absent stays absent, and ordering/timestamps hold.
func TestCatalogExportImportRoundTripByteIdentical(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv, _, _ := newMergeImportHandler(t)

		submitAndAwaitImport(t, srv, websiteRoundTripExport)

		rec := exportBody(t, srv)
		if rec.Code != http.StatusOK {
			t.Fatalf("export status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if got := rec.Body.String(); got != websiteRoundTripExport {
			t.Errorf("export → import → export not byte-identical:\ngot:\n%s\nwant:\n%s", got, websiteRoundTripExport)
		}
	})
}
