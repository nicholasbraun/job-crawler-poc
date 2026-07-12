package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
	"github.com/nicholasbraun/job-crawler-poc/internal/runner"
)

type fakeRunner struct {
	started   uuid.UUID
	stopped   uuid.UUID
	paused    uuid.UUID
	resumed   uuid.UUID
	startErr  error
	stopErr   error
	pauseErr  error
	resumeErr error
	startFn   func(uuid.UUID) *crawler.CrawlRun
}

func (f *fakeRunner) Start(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error) {
	f.started = definitionID
	if f.startErr != nil {
		return nil, f.startErr
	}
	if f.startFn != nil {
		return f.startFn(definitionID), nil
	}
	return &crawler.CrawlRun{ID: uuid.New(), DefinitionID: definitionID, Status: crawler.RunStatusRunning}, nil
}

func (f *fakeRunner) Stop(ctx context.Context, runID uuid.UUID) error {
	f.stopped = runID
	return f.stopErr
}

func (f *fakeRunner) Pause(ctx context.Context, runID uuid.UUID) error {
	f.paused = runID
	return f.pauseErr
}

func (f *fakeRunner) Resume(ctx context.Context, runID uuid.UUID) error {
	f.resumed = runID
	return f.resumeErr
}

type fakeDefRepo struct {
	created   *crawler.CrawlDefinition
	list      []*crawler.CrawlDefinition
	get       *crawler.CrawlDefinition
	deleted   uuid.UUID
	deleteErr error
}

func (f *fakeDefRepo) Create(ctx context.Context, def *crawler.CrawlDefinition) error {
	def.ID = uuid.New()
	f.created = def
	return nil
}
func (f *fakeDefRepo) Delete(ctx context.Context, id uuid.UUID) error {
	f.deleted = id
	return f.deleteErr
}
func (f *fakeDefRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlDefinition, error) {
	if f.get != nil && f.get.ID == id {
		return f.get, nil
	}
	return nil, crawler.ErrNotFound
}
func (f *fakeDefRepo) List(ctx context.Context) ([]*crawler.CrawlDefinition, error) {
	return f.list, nil
}

type fakeRunRepo struct {
	runs   []*crawler.CrawlRun
	getErr error
}

func (f *fakeRunRepo) Create(ctx context.Context, run *crawler.CrawlRun) error { return nil }
func (f *fakeRunRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlRun, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	for _, r := range f.runs {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, crawler.ErrNotFound
}
func (f *fakeRunRepo) List(ctx context.Context) ([]*crawler.CrawlRun, error) { return f.runs, nil }
func (f *fakeRunRepo) ListByStatus(ctx context.Context, statuses ...crawler.RunStatus) ([]*crawler.CrawlRun, error) {
	return nil, nil
}
func (f *fakeRunRepo) GetStatus(ctx context.Context, id uuid.UUID) (crawler.RunStatus, error) {
	return "", nil
}
func (f *fakeRunRepo) UpdateStatus(ctx context.Context, id uuid.UUID, s crawler.RunStatus, ft *time.Time, e string) error {
	return nil
}
func (f *fakeRunRepo) UpdateCounters(ctx context.Context, id uuid.UUID, c crawler.RunCounters) error {
	return nil
}

type fakeCompanyRepo struct {
	companies []*crawler.Company
}

func (f *fakeCompanyRepo) Upsert(ctx context.Context, c *crawler.Company) error { return nil }
func (f *fakeCompanyRepo) List(ctx context.Context) ([]*crawler.Company, error) {
	return f.companies, nil
}

type fakeCareerPageRepo struct {
	pages []*crawler.CareerPage
}

func (f *fakeCareerPageRepo) Upsert(ctx context.Context, p *crawler.CareerPage) error { return nil }
func (f *fakeCareerPageRepo) ListURLs(ctx context.Context) ([]string, error)          { return nil, nil }
func (f *fakeCareerPageRepo) List(ctx context.Context) ([]*crawler.CareerPage, error) {
	return f.pages, nil
}

type fakeListingRepo struct {
	byDefinition  []*crawler.JobListing
	gotDefinition uuid.UUID
	gotKeyword    string
}

func (f *fakeListingRepo) Save(ctx context.Context, definitionID uuid.UUID, jl *crawler.JobListing) error {
	return nil
}
func (f *fakeListingRepo) Find(ctx context.Context) ([]*crawler.JobListing, error) { return nil, nil }
func (f *fakeListingRepo) FindByDefinition(ctx context.Context, definitionID uuid.UUID, keyword string) ([]*crawler.JobListing, error) {
	f.gotDefinition = definitionID
	f.gotKeyword = keyword
	return f.byDefinition, nil
}

func defaults() api.Defaults {
	return api.Defaults{
		MaxDepth:   7,
		MaxDomains: 42,
		URLFilter:  crawler.URLFilterConfig{AllowedTLDs: []string{"com", "io"}},
	}
}

// newHandler builds a Handler with sensible fake defaults, letting each test
// override only the dependencies it exercises.
func newHandler(cfg api.Config) http.Handler {
	if cfg.Runner == nil {
		cfg.Runner = &fakeRunner{}
	}
	if cfg.Runs == nil {
		cfg.Runs = &fakeRunRepo{}
	}
	if cfg.Definitions == nil {
		cfg.Definitions = &fakeDefRepo{}
	}
	if cfg.Companies == nil {
		cfg.Companies = &fakeCompanyRepo{}
	}
	if cfg.CareerPages == nil {
		cfg.CareerPages = &fakeCareerPageRepo{}
	}
	if cfg.Listings == nil {
		cfg.Listings = &fakeListingRepo{}
	}
	cfg.Defaults = defaults()
	return api.New(cfg).Routes()
}

func TestCreateCrawlFillsDefaults(t *testing.T) {
	rnr := &fakeRunner{}
	defs := &fakeDefRepo{}
	srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

	body, _ := json.Marshal(map[string]any{
		"name":     "minimal",
		"seedUrls": []string{"https://example.com"},
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
	}
	if defs.created == nil {
		t.Fatal("expected a definition to be created")
	}
	if defs.created.MaxDepth != 7 || defs.created.MaxDomains != 42 {
		t.Errorf("omitted fields not defaulted: depth=%d domains=%d", defs.created.MaxDepth, defs.created.MaxDomains)
	}
	if defs.created.Kind != crawler.CrawlKindDiscovery {
		t.Errorf("kind: got %q, want discovery", defs.created.Kind)
	}
	if len(defs.created.URLFilter.AllowedTLDs) != 2 {
		t.Errorf("url filter not defaulted: %+v", defs.created.URLFilter)
	}
	if rnr.started != defs.created.ID {
		t.Errorf("runner started %v, want created def id %v", rnr.started, defs.created.ID)
	}
}

func TestCreateCrawlRollsBackDefinitionWhenStartFails(t *testing.T) {
	t.Run("deletes the orphaned definition and still 500s", func(t *testing.T) {
		rnr := &fakeRunner{startErr: crawler.ErrNotFound}
		defs := &fakeDefRepo{}
		srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "start-fails",
			"seedUrls": []string{"https://example.com"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status: got %d, want 500; body=%s", rec.Code, rec.Body)
		}
		if defs.created == nil {
			t.Fatal("expected a definition to have been created")
		}
		if defs.deleted != defs.created.ID {
			t.Errorf("orphaned definition not rolled back: deleted %v, want %v", defs.deleted, defs.created.ID)
		}
	})

	t.Run("a failed rollback is swallowed, response stays 500", func(t *testing.T) {
		rnr := &fakeRunner{startErr: crawler.ErrNotFound}
		defs := &fakeDefRepo{deleteErr: crawler.ErrNotFound}
		srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "cleanup-fails",
			"seedUrls": []string{"https://example.com"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status: got %d, want 500; body=%s", rec.Code, rec.Body)
		}
		if defs.deleted != defs.created.ID {
			t.Errorf("rollback should still be attempted: deleted %v, want %v", defs.deleted, defs.created.ID)
		}
	})
}

func TestCreateCrawlRejectsMissingFields(t *testing.T) {
	srv := newHandler(api.Config{})

	body, _ := json.Marshal(map[string]any{"name": "no seeds"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateKeywordCrawl(t *testing.T) {
	t.Run("keyword crawl needs keywords but not seedUrls", func(t *testing.T) {
		defs := &fakeDefRepo{}
		srv := newHandler(api.Config{Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "go-backend",
			"kind":     "keyword",
			"keywords": []string{"golang", "backend"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
		}
		if defs.created == nil || defs.created.Kind != crawler.CrawlKindKeyword {
			t.Fatalf("expected a keyword definition, got %+v", defs.created)
		}
		if len(defs.created.Keywords) != 2 {
			t.Errorf("keywords not persisted: %+v", defs.created.Keywords)
		}
	})

	t.Run("keyword crawl with no keywords is rejected", func(t *testing.T) {
		srv := newHandler(api.Config{})

		body, _ := json.Marshal(map[string]any{"name": "no keywords", "kind": "keyword"})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rec.Code)
		}
	})

	t.Run("unknown kind is rejected", func(t *testing.T) {
		srv := newHandler(api.Config{})

		body, _ := json.Marshal(map[string]any{
			"name":     "bogus",
			"kind":     "sitemap",
			"seedUrls": []string{"https://example.com"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rec.Code)
		}
	})
}

func TestGetCrawlNotFound(t *testing.T) {
	srv := newHandler(api.Config{})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls/"+uuid.New().String(), nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestStopCrawlNotActive(t *testing.T) {
	rnr := &fakeRunner{stopErr: runner.ErrRunNotActive}
	srv := newHandler(api.Config{Runner: rnr})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+uuid.New().String()+"/stop", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
}

// TestStopCrawlPausedReturns202 verifies acceptance criterion 2 / carry-forward
// requirement 3: a paused run is now stoppable, so runner.Stop returns nil rather
// than ErrRunNotActive and the handler answers 202 (not the 409 a paused run used
// to collapse to).
func TestStopCrawlPausedReturns202(t *testing.T) {
	run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusPaused}
	runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
	rnr := &fakeRunner{} // stopErr nil: Stop now drives a paused run to stopped
	srv := newHandler(api.Config{Runner: rnr, Runs: runs})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+run.ID.String()+"/stop", nil))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d, want 202; body=%s", rec.Code, rec.Body)
	}
	if rnr.stopped != run.ID {
		t.Errorf("runner stopped %v, want %v", rnr.stopped, run.ID)
	}
}

func TestPauseCrawl(t *testing.T) {
	t.Run("running run pauses and returns 202", func(t *testing.T) {
		run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusRunning}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
		rnr := &fakeRunner{}
		srv := newHandler(api.Config{Runner: rnr, Runs: runs})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+run.ID.String()+"/pause", nil))

		if rec.Code != http.StatusAccepted {
			t.Fatalf("status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		if rnr.paused != run.ID {
			t.Errorf("runner paused %v, want %v", rnr.paused, run.ID)
		}
	})

	t.Run("non-running run returns 409", func(t *testing.T) {
		run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusPaused}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
		rnr := &fakeRunner{pauseErr: runner.ErrRunNotActive}
		srv := newHandler(api.Config{Runner: rnr, Runs: runs})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+run.ID.String()+"/pause", nil))

		if rec.Code != http.StatusConflict {
			t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("unknown run is 404", func(t *testing.T) {
		rnr := &fakeRunner{}
		srv := newHandler(api.Config{Runner: rnr, Runs: &fakeRunRepo{}})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+uuid.New().String()+"/pause", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
		if rnr.paused != uuid.Nil {
			t.Errorf("runner.Pause must not be called for an unknown id; got %v", rnr.paused)
		}
	})
}

func TestResumeCrawl(t *testing.T) {
	t.Run("paused run resumes and returns 202", func(t *testing.T) {
		run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusPaused}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
		rnr := &fakeRunner{}
		srv := newHandler(api.Config{Runner: rnr, Runs: runs})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+run.ID.String()+"/resume", nil))

		if rec.Code != http.StatusAccepted {
			t.Fatalf("status: got %d, want 202; body=%s", rec.Code, rec.Body)
		}
		if rnr.resumed != run.ID {
			t.Errorf("runner resumed %v, want %v", rnr.resumed, run.ID)
		}
	})

	t.Run("non-paused run returns 409", func(t *testing.T) {
		run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusRunning}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
		rnr := &fakeRunner{resumeErr: runner.ErrRunNotPaused}
		srv := newHandler(api.Config{Runner: rnr, Runs: runs})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+run.ID.String()+"/resume", nil))

		if rec.Code != http.StatusConflict {
			t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("unknown run is 404", func(t *testing.T) {
		rnr := &fakeRunner{}
		srv := newHandler(api.Config{Runner: rnr, Runs: &fakeRunRepo{}})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+uuid.New().String()+"/resume", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
		if rnr.resumed != uuid.Nil {
			t.Errorf("runner.Resume must not be called for an unknown id; got %v", rnr.resumed)
		}
	})
}

func TestListCrawls(t *testing.T) {
	runs := &fakeRunRepo{runs: []*crawler.CrawlRun{
		{ID: uuid.New(), Status: crawler.RunStatusRunning},
		{ID: uuid.New(), Status: crawler.RunStatusCompleted},
	}}
	srv := newHandler(api.Config{Runs: runs})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 runs, got %d", len(got))
	}
}

func TestCreateDefinitionDoesNotStartRun(t *testing.T) {
	rnr := &fakeRunner{}
	defs := &fakeDefRepo{}
	srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

	body, _ := json.Marshal(map[string]any{
		"name":     "library-def",
		"seedUrls": []string{"https://example.com"},
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/definitions", bytes.NewReader(body)))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
	}
	if defs.created == nil {
		t.Fatal("expected a definition to be created")
	}
	if rnr.started != uuid.Nil {
		t.Errorf("create-only must not start a run, but runner.Start was called with %v", rnr.started)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["id"] != defs.created.ID.String() {
		t.Errorf("response id %v, want created def id %v", got["id"], defs.created.ID)
	}
}

func TestListAndGetDefinitions(t *testing.T) {
	def := &crawler.CrawlDefinition{
		ID:       uuid.New(),
		Name:     "discovery-1",
		Kind:     crawler.CrawlKindDiscovery,
		SeedURLs: []string{"https://example.com"},
	}
	defs := &fakeDefRepo{list: []*crawler.CrawlDefinition{def}, get: def}
	srv := newHandler(api.Config{Definitions: defs})

	t.Run("list", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/definitions", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rec.Code)
		}
		var got []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got) != 1 || got[0]["name"] != "discovery-1" {
			t.Errorf("unexpected list body: %v", got)
		}
		// nil slices must serialize as [] not null.
		if _, ok := got[0]["keywords"].([]any); !ok {
			t.Errorf("keywords should be an array, got %T", got[0]["keywords"])
		}
	})

	t.Run("get one", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/definitions/"+def.ID.String(), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rec.Code)
		}
	})

	t.Run("get unknown is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/definitions/"+uuid.New().String(), nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rec.Code)
		}
	})
}

func TestStartRunOfExistingDefinition(t *testing.T) {
	def := &crawler.CrawlDefinition{ID: uuid.New(), Name: "d", Kind: crawler.CrawlKindDiscovery}

	t.Run("starts a run", func(t *testing.T) {
		rnr := &fakeRunner{}
		defs := &fakeDefRepo{get: def}
		srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/definitions/"+def.ID.String()+"/runs", nil))
		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
		}
		if rnr.started != def.ID {
			t.Errorf("runner started %v, want %v", rnr.started, def.ID)
		}
	})

	t.Run("unknown definition is 404", func(t *testing.T) {
		rnr := &fakeRunner{startErr: crawler.ErrNotFound}
		srv := newHandler(api.Config{Runner: rnr})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/definitions/"+uuid.New().String()+"/runs", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rec.Code)
		}
	})
}

func TestListCompanies(t *testing.T) {
	companies := &fakeCompanyRepo{companies: []*crawler.Company{
		{ID: uuid.New(), CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", Name: "Acme"},
	}}
	srv := newHandler(api.Config{Companies: companies})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/companies", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0]["companyKey"] != "greenhouse:acme" {
		t.Errorf("unexpected companies body: %v", got)
	}
}

func TestListCareerPagesFiltersByCompany(t *testing.T) {
	acme := uuid.New()
	globex := uuid.New()
	pages := &fakeCareerPageRepo{pages: []*crawler.CareerPage{
		{ID: uuid.New(), CompanyID: acme, URL: "https://a/1"},
		{ID: uuid.New(), CompanyID: globex, URL: "https://g/1"},
	}}
	srv := newHandler(api.Config{CareerPages: pages})

	t.Run("all pages", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/career-pages", nil))
		var got []map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if len(got) != 2 {
			t.Errorf("want 2 pages, got %d", len(got))
		}
	})

	t.Run("filtered by companyId", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/career-pages?companyId="+acme.String(), nil))
		var got []map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if len(got) != 1 || got[0]["url"] != "https://a/1" {
			t.Errorf("want only acme's page, got %v", got)
		}
	})

	t.Run("invalid companyId is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/career-pages?companyId=not-a-uuid", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rec.Code)
		}
	})
}

func TestListListings(t *testing.T) {
	defID := uuid.New()
	listings := &fakeListingRepo{byDefinition: []*crawler.JobListing{
		{URL: "https://jobs/1", Title: "Go Engineer", TechStack: []string{"go"}},
	}}
	srv := newHandler(api.Config{Listings: listings})

	t.Run("requires definitionId", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rec.Code)
		}
	})

	t.Run("passes definitionId and keyword through", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings?definitionId="+defID.String()+"&keyword=go", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if listings.gotDefinition != defID {
			t.Errorf("definitionId not forwarded: got %v, want %v", listings.gotDefinition, defID)
		}
		if listings.gotKeyword != "go" {
			t.Errorf("keyword not forwarded: got %q, want %q", listings.gotKeyword, "go")
		}
		var got []map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if len(got) != 1 || got[0]["title"] != "Go Engineer" {
			t.Errorf("unexpected listings body: %v", got)
		}
	})

	t.Run("invalid definitionId is 400", func(t *testing.T) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings?definitionId=nope", nil))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rec.Code)
		}
	})
}

func TestGetCrawlStatus(t *testing.T) {
	run := &crawler.CrawlRun{
		ID:       uuid.New(),
		Status:   crawler.RunStatusRunning,
		Counters: crawler.RunCounters{PagesCrawled: 12, ListingsFound: 3},
	}
	runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}

	t.Run("reports counters and frontier size", func(t *testing.T) {
		sizer := func(ctx context.Context, id uuid.UUID) (int64, error) { return 99, nil }
		srv := newHandler(api.Config{Runs: runs, FrontierSizer: sizer})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls/"+run.ID.String()+"/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		var got map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got["pagesCrawled"] != float64(12) || got["listingsFound"] != float64(3) {
			t.Errorf("counters wrong: %v", got)
		}
		if got["frontierSize"] != float64(99) {
			t.Errorf("frontierSize: got %v, want 99", got["frontierSize"])
		}
	})

	t.Run("a frontier sizer error degrades to size 0, not a failure", func(t *testing.T) {
		sizer := func(ctx context.Context, id uuid.UUID) (int64, error) {
			return 0, context.DeadlineExceeded
		}
		srv := newHandler(api.Config{Runs: runs, FrontierSizer: sizer})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls/"+run.ID.String()+"/status", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200 despite sizer error", rec.Code)
		}
		var got map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if got["frontierSize"] != float64(0) {
			t.Errorf("frontierSize on error: got %v, want 0", got["frontierSize"])
		}
	})

	t.Run("unknown run is 404", func(t *testing.T) {
		srv := newHandler(api.Config{Runs: &fakeRunRepo{}})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls/"+uuid.New().String()+"/status", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404", rec.Code)
		}
	})
}
