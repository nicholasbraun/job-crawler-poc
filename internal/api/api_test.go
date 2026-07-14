package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
	"github.com/nicholasbraun/job-crawler-poc/internal/importer"
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

// fakeCompanyRepo is a mutex-guarded in-memory CompanyRepository. The mutex
// matters when a real importer.Importer is wired over it: it merges on a
// background goroutine while handlers read List, so the two race under -race.
// MergeImport is a faithful port of the Postgres merge (presence-wins fields,
// monotone LEAST/GREATEST timestamps, not-a-Sighting) so the API-seam
// idempotency test is meaningful.
type fakeCompanyRepo struct {
	mu        sync.Mutex
	companies []*crawler.Company
	listErr   error
}

func (f *fakeCompanyRepo) Upsert(ctx context.Context, c *crawler.Company) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	saved := *c
	f.companies = append(f.companies, &saved)
	return nil
}
func (f *fakeCompanyRepo) List(ctx context.Context) ([]*crawler.Company, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.companies, nil
}
func (f *fakeCompanyRepo) MergeImport(ctx context.Context, m *crawler.CompanyMerge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.companies {
		if c.CompanyKey != m.CompanyKey {
			continue
		}
		if m.ATSProviderPresent {
			c.ATSProvider = m.ATSProvider
		}
		if m.DisplayDomainPresent {
			c.DisplayDomain = m.DisplayDomain
		}
		if m.NamePresent {
			c.Name = m.Name
		}
		if m.FirstSeen != nil && m.FirstSeen.Before(c.FirstSeen) {
			c.FirstSeen = *m.FirstSeen
		}
		if m.LastSeen != nil && m.LastSeen.After(c.LastSeen) {
			c.LastSeen = *m.LastSeen
		}
		m.ID = c.ID
		return nil
	}
	now := time.Now().UTC()
	firstSeen, lastSeen := now, now
	if m.FirstSeen != nil {
		firstSeen = *m.FirstSeen
	}
	if m.LastSeen != nil {
		lastSeen = *m.LastSeen
	}
	c := &crawler.Company{
		ID:            uuid.New(),
		CompanyKey:    m.CompanyKey,
		ATSProvider:   m.ATSProvider,
		DisplayDomain: m.DisplayDomain,
		Name:          m.Name,
		FirstSeen:     firstSeen,
		LastSeen:      lastSeen,
	}
	f.companies = append(f.companies, c)
	m.ID = c.ID
	return nil
}

// fakeCareerPageRepo is a mutex-guarded in-memory CareerPageRepository. Like
// fakeCompanyRepo it is guarded so the importer's background merges never race
// handler reads under -race, and its MergeImport faithfully ports the Postgres
// merge (monotone timestamps, refreshed politeness domain, not-a-Sighting).
type fakeCareerPageRepo struct {
	mu             sync.Mutex
	pages          []*crawler.CareerPage
	firstSeenByDay []crawler.DayCount
	listErr        error
	// onList runs before List returns, letting a test mutate the catalog
	// between the export handler's two reads to simulate the
	// unsynchronised-snapshot race (ADR-0015).
	onList func()
}

func (f *fakeCareerPageRepo) Upsert(ctx context.Context, p *crawler.CareerPage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	saved := *p
	f.pages = append(f.pages, &saved)
	return nil
}
func (f *fakeCareerPageRepo) ListURLs(ctx context.Context) ([]string, error) { return nil, nil }
func (f *fakeCareerPageRepo) List(ctx context.Context) ([]*crawler.CareerPage, error) {
	if f.onList != nil {
		f.onList()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.pages, nil
}
func (f *fakeCareerPageRepo) FirstSeenByDay(ctx context.Context) ([]crawler.DayCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.firstSeenByDay, nil
}
func (f *fakeCareerPageRepo) MergeImport(ctx context.Context, m *crawler.CareerPageMerge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.pages {
		if p.CompanyID != m.CompanyID || p.URL != m.URL {
			continue
		}
		p.PolitenessDomain = m.PolitenessDomain
		if m.FirstSeen != nil && m.FirstSeen.Before(p.FirstSeen) {
			p.FirstSeen = *m.FirstSeen
		}
		if m.LastSeen != nil && m.LastSeen.After(p.LastSeen) {
			p.LastSeen = *m.LastSeen
		}
		return nil
	}
	now := time.Now().UTC()
	firstSeen, lastSeen := now, now
	if m.FirstSeen != nil {
		firstSeen = *m.FirstSeen
	}
	if m.LastSeen != nil {
		lastSeen = *m.LastSeen
	}
	f.pages = append(f.pages, &crawler.CareerPage{
		ID:               uuid.New(),
		CompanyID:        m.CompanyID,
		URL:              m.URL,
		PolitenessDomain: m.PolitenessDomain,
		FirstSeen:        firstSeen,
		LastSeen:         lastSeen,
	})
	return nil
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

// fakeImportJobRepo is a mutex-guarded in-memory ImportJobRepository shared by
// the import handler tests (in catalog_import_test.go) and newHandler's
// nil-defaults. The mutex matters: a real importer.Importer wired over it runs
// jobs on background goroutines, so Create/Update race handler reads under -race.
type fakeImportJobRepo struct {
	mu   sync.Mutex
	jobs map[uuid.UUID]*crawler.ImportJob

	listErr error
	getErr  error
}

func newFakeImportJobRepo() *fakeImportJobRepo {
	return &fakeImportJobRepo{jobs: map[uuid.UUID]*crawler.ImportJob{}}
}

func (f *fakeImportJobRepo) Create(ctx context.Context, job *crawler.ImportJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	stored := *job
	f.jobs[job.ID] = &stored
	return nil
}

func (f *fakeImportJobRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.ImportJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
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
	if f.listErr != nil {
		return nil, f.listErr
	}
	jobs := make([]*crawler.ImportJob, 0, len(f.jobs))
	for _, j := range f.jobs {
		copied := *j
		jobs = append(jobs, &copied)
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.After(jobs[j].CreatedAt) })
	return jobs, nil
}

func (f *fakeImportJobRepo) Update(ctx context.Context, job *crawler.ImportJob) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored := *job
	f.jobs[job.ID] = &stored
	return nil
}

func (f *fakeImportJobRepo) SweepInterrupted(ctx context.Context, msg string, at time.Time) (int64, error) {
	return 0, nil
}

func defaults() api.Defaults {
	return api.Defaults{
		MaxDepth:  7,
		URLFilter: crawler.URLFilterConfig{AllowedTLDs: []string{"com", "io"}},
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
	if cfg.ImportJobs == nil {
		cfg.ImportJobs = newFakeImportJobRepo()
	}
	if cfg.Importer == nil {
		// A real importer over the fake repo, so submit->poll->result exercises
		// the executor end-to-end in the import handler tests.
		cfg.Importer = importer.New(cfg.ImportJobs)
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
	if defs.created.MaxDepth != 7 {
		t.Errorf("omitted fields not defaulted: depth=%d", defs.created.MaxDepth)
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
	t.Run("existing run that is not stoppable returns 409", func(t *testing.T) {
		run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusRunning}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
		rnr := &fakeRunner{stopErr: runner.ErrRunNotActive}
		srv := newHandler(api.Config{Runner: rnr, Runs: runs})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+run.ID.String()+"/stop", nil))

		if rec.Code != http.StatusConflict {
			t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("unknown run is 404", func(t *testing.T) {
		rnr := &fakeRunner{}
		srv := newHandler(api.Config{Runner: rnr, Runs: &fakeRunRepo{}})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls/"+uuid.New().String()+"/stop", nil))

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
		if rnr.stopped != uuid.Nil {
			t.Errorf("runner.Stop must not be called for an unknown id; got %v", rnr.stopped)
		}
	})
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

func TestListCrawlsIncludesFrontierSize(t *testing.T) {
	active := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusRunning}
	done := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusCompleted}
	runs := &fakeRunRepo{runs: []*crawler.CrawlRun{active, done}}

	var sized []uuid.UUID
	sizer := func(ctx context.Context, id uuid.UUID) (int64, error) {
		sized = append(sized, id)
		return 42, nil
	}
	srv := newHandler(api.Config{Runs: runs, FrontierSizer: sizer})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(got))
	}
	// The running run is enriched from the sizer; the terminal run reports 0
	// without the sizer ever being consulted (its frontier is gone).
	if got[0]["frontierSize"] != float64(42) {
		t.Errorf("active run frontierSize: got %v, want 42", got[0]["frontierSize"])
	}
	if got[1]["frontierSize"] != float64(0) {
		t.Errorf("terminal run frontierSize: got %v, want 0", got[1]["frontierSize"])
	}
	if len(sized) != 1 || sized[0] != active.ID {
		t.Errorf("sizer should be called only for the active run; called for %v", sized)
	}
}

func TestGetCrawlIncludesFrontierSize(t *testing.T) {
	run := &crawler.CrawlRun{ID: uuid.New(), Status: crawler.RunStatusRunning}
	runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
	sizer := func(ctx context.Context, id uuid.UUID) (int64, error) { return 7, nil }
	srv := newHandler(api.Config{Runs: runs, FrontierSizer: sizer})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/crawls/"+run.ID.String(), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["frontierSize"] != float64(7) {
		t.Errorf("frontierSize: got %v, want 7", got["frontierSize"])
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

func TestCatalogHistory(t *testing.T) {
	utcDay := func(d int) time.Time { return time.Date(2026, 1, d, 0, 0, 0, 0, time.UTC) }

	t.Run("returns the cumulative gap-filled series under careerPages", func(t *testing.T) {
		// Jan 10: +2, gap on Jan 11, Jan 12: +3. The endpoint (5) equals the total.
		pages := &fakeCareerPageRepo{firstSeenByDay: []crawler.DayCount{
			{Day: utcDay(10), Count: 2},
			{Day: utcDay(12), Count: 3},
		}}
		srv := newHandler(api.Config{CareerPages: pages})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog-history", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}

		var got struct {
			CareerPages []int `json:"careerPages"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("error decoding body %s: %v", rec.Body, err)
		}
		if len(got.CareerPages) < 3 {
			t.Fatalf("want at least the 3 seeded days, got %v", got.CareerPages)
		}
		if got.CareerPages[0] != 2 {
			t.Errorf("series should start at the first day's count 2, got %v", got.CareerPages)
		}
		if last := got.CareerPages[len(got.CareerPages)-1]; last != 5 {
			t.Errorf("endpoint should equal the cumulative total 5, got %d", last)
		}
	})

	t.Run("empty catalog yields an empty, non-null array", func(t *testing.T) {
		srv := newHandler(api.Config{CareerPages: &fakeCareerPageRepo{}})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog-history", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"careerPages":[]`) {
			t.Errorf("empty catalog should serialize an empty array, got %s", body)
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
