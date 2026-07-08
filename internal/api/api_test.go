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
	started uuid.UUID
	stopped uuid.UUID
	stopErr error
	startFn func(uuid.UUID) *crawler.CrawlRun
}

func (f *fakeRunner) Start(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error) {
	f.started = definitionID
	if f.startFn != nil {
		return f.startFn(definitionID), nil
	}
	return &crawler.CrawlRun{ID: uuid.New(), DefinitionID: definitionID, Status: crawler.RunStatusRunning}, nil
}

func (f *fakeRunner) Stop(ctx context.Context, runID uuid.UUID) error {
	f.stopped = runID
	return f.stopErr
}

type fakeDefRepo struct {
	created *crawler.CrawlDefinition
}

func (f *fakeDefRepo) Create(ctx context.Context, def *crawler.CrawlDefinition) error {
	def.ID = uuid.New()
	f.created = def
	return nil
}
func (f *fakeDefRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.CrawlDefinition, error) {
	return nil, crawler.ErrNotFound
}
func (f *fakeDefRepo) List(ctx context.Context) ([]*crawler.CrawlDefinition, error) { return nil, nil }

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

func defaults() api.Defaults {
	return api.Defaults{
		MaxDepth:   7,
		MaxDomains: 42,
		URLFilter:  crawler.URLFilterConfig{AllowedTLDs: []string{"com", "io"}},
	}
}

func TestCreateCrawlFillsDefaults(t *testing.T) {
	rnr := &fakeRunner{}
	defs := &fakeDefRepo{}
	runs := &fakeRunRepo{}
	srv := api.New(rnr, runs, defs, defaults()).Routes()

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

func TestCreateCrawlRejectsMissingFields(t *testing.T) {
	srv := api.New(&fakeRunner{}, &fakeRunRepo{}, &fakeDefRepo{}, defaults()).Routes()

	body, _ := json.Marshal(map[string]any{"name": "no seeds"})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", rec.Code)
	}
}

func TestCreateKeywordCrawl(t *testing.T) {
	t.Run("keyword crawl needs keywords but not seedUrls", func(t *testing.T) {
		rnr := &fakeRunner{}
		defs := &fakeDefRepo{}
		srv := api.New(rnr, &fakeRunRepo{}, defs, defaults()).Routes()

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
		srv := api.New(&fakeRunner{}, &fakeRunRepo{}, &fakeDefRepo{}, defaults()).Routes()

		body, _ := json.Marshal(map[string]any{"name": "no keywords", "kind": "keyword"})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", rec.Code)
		}
	})

	t.Run("unknown kind is rejected", func(t *testing.T) {
		srv := api.New(&fakeRunner{}, &fakeRunRepo{}, &fakeDefRepo{}, defaults()).Routes()

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
	srv := api.New(&fakeRunner{}, &fakeRunRepo{}, &fakeDefRepo{}, defaults()).Routes()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls/"+uuid.New().String(), nil))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status: got %d, want 404", rec.Code)
	}
}

func TestStopCrawlNotActive(t *testing.T) {
	rnr := &fakeRunner{stopErr: runner.ErrRunNotActive}
	srv := api.New(rnr, &fakeRunRepo{}, &fakeDefRepo{}, defaults()).Routes()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+uuid.New().String()+"/stop", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409", rec.Code)
	}
}

func TestListCrawls(t *testing.T) {
	runs := &fakeRunRepo{runs: []*crawler.CrawlRun{
		{ID: uuid.New(), Status: crawler.RunStatusRunning},
		{ID: uuid.New(), Status: crawler.RunStatusCompleted},
	}}
	srv := api.New(&fakeRunner{}, runs, &fakeDefRepo{}, defaults()).Routes()

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
