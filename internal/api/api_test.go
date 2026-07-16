package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
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
	createErr error
	list      []*crawler.CrawlDefinition
	get       *crawler.CrawlDefinition
	deleted   uuid.UUID
	deleteErr error
	appended  []string
	appendErr error
}

func (f *fakeDefRepo) Create(ctx context.Context, def *crawler.CrawlDefinition) error {
	if f.createErr != nil {
		return f.createErr
	}
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
func (f *fakeDefRepo) AppendSeedURL(ctx context.Context, id uuid.UUID, url string) error {
	if f.appendErr != nil {
		return f.appendErr
	}
	f.appended = append(f.appended, url)
	// Mirror the DB's idempotent append onto `get` so the handler's response
	// (built from the in-memory def) reflects the new Seed exactly once.
	if f.get != nil && f.get.ID == id && !slices.Contains(f.get.SeedURLs, url) {
		f.get.SeedURLs = append(f.get.SeedURLs, url)
	}
	return nil
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
	var out []*crawler.CrawlRun
	for _, r := range f.runs {
		for _, s := range statuses {
			if r.Status == s {
				out = append(out, r)
				break
			}
		}
	}
	return out, nil
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
		if m.WebsitePresent {
			c.Website = m.Website
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
		Website:       m.Website,
		FirstSeen:     firstSeen,
		LastSeen:      lastSeen,
	}
	f.companies = append(f.companies, c)
	m.ID = c.ID
	return nil
}
func (f *fakeCompanyRepo) ListPagelessWebsites(ctx context.Context) ([]string, error) {
	return nil, nil
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
	mu    sync.Mutex
	jobs  map[uuid.UUID]*crawler.ImportJob
	byKey map[string]uuid.UUID // idempotency key -> job id, for replay arbitration

	listErr error
	getErr  error
}

func newFakeImportJobRepo() *fakeImportJobRepo {
	return &fakeImportJobRepo{
		jobs:  map[uuid.UUID]*crawler.ImportJob{},
		byKey: map[string]uuid.UUID{},
	}
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

func (f *fakeImportJobRepo) CreateWithKey(ctx context.Context, job *crawler.ImportJob) (*crawler.ImportJob, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byKey[job.IdempotencyKey]; ok {
		existing := f.jobs[id]
		if existing.RequestFingerprint != job.RequestFingerprint {
			return nil, false, crawler.ErrIdempotencyKeyConflict
		}
		copied := *existing // reflect any background Update the original job saw
		return &copied, true, nil
	}
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	stored := *job
	f.jobs[job.ID] = &stored
	f.byKey[job.IdempotencyKey] = job.ID
	return job, false, nil
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
		KeywordMaxDepth:   4,
		DiscoveryMaxDepth: 10,
		DiscoverySeeds:    []string{"https://seed-a.example.com/", "https://seed-b.example.com/"},
		URLFilter:         crawler.URLFilterConfig{AllowedTLDs: []string{"com", "io"}},
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
	if defs.created.MaxDepth != 10 {
		t.Errorf("omitted depth not defaulted to discovery default: depth=%d", defs.created.MaxDepth)
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

// TestCreateCrawlNormalizesSeedURLs asserts the create path stores Seeds in the
// same canonical form the add-seed path uses (crawler.NewURL). Without this, a
// Seed created with a trailing slash and the same Seed re-added later via
// /seeds would not dedupe, appending a near-duplicate (PR #109 review).
func TestCreateCrawlNormalizesSeedURLs(t *testing.T) {
	t.Run("stores the canonical form", func(t *testing.T) {
		rnr := &fakeRunner{}
		defs := &fakeDefRepo{}
		srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "discovery",
			"kind":     "discovery",
			"seedUrls": []string{"https://www.eu-startups.com/directory/"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
		}
		want := "https://www.eu-startups.com/directory"
		if got := defs.created.SeedURLs; len(got) != 1 || got[0] != want {
			t.Errorf("seed not normalized on create: got %v, want [%q]", got, want)
		}
	})

	t.Run("rejects a malformed seed", func(t *testing.T) {
		defs := &fakeDefRepo{}
		srv := newHandler(api.Config{Runner: &fakeRunner{}, Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "discovery",
			"kind":     "discovery",
			"seedUrls": []string{"https://ok.example.com", ""},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
		if defs.created != nil {
			t.Error("no definition should be created when a seed is invalid")
		}
	})
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

// TestCreateCrawlDiscoveryConflict asserts the fused endpoint maps the
// definition repository's ErrDiscoveryDefinitionExists to 409 (ADR-0017): a
// second discovery definition is refused, not silently duplicated.
func TestCreateCrawlDiscoveryConflict(t *testing.T) {
	rnr := &fakeRunner{}
	defs := &fakeDefRepo{createErr: crawler.ErrDiscoveryDefinitionExists}
	srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

	body, _ := json.Marshal(map[string]any{
		"name":     "discovery",
		"kind":     "discovery",
		"seedUrls": []string{"https://example.com"},
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
	}
	if rnr.started != uuid.Nil {
		t.Errorf("runner.Start must not be called when the definition create conflicts; got %v", rnr.started)
	}
}

// TestCreateCrawlActiveRunConflict asserts the fused endpoint maps the runner's
// ErrActiveRunExists to 409 and still rolls back the orphaned definition, so a
// lost run-insert race is a clean conflict rather than a 500 plus an orphan.
func TestCreateCrawlActiveRunConflict(t *testing.T) {
	rnr := &fakeRunner{startErr: crawler.ErrActiveRunExists}
	defs := &fakeDefRepo{}
	srv := newHandler(api.Config{Runner: rnr, Definitions: defs})

	body, _ := json.Marshal(map[string]any{
		"name":     "discovery",
		"kind":     "discovery",
		"seedUrls": []string{"https://example.com"},
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
	}
	if defs.created == nil {
		t.Fatal("expected a definition to have been created")
	}
	if defs.deleted != defs.created.ID {
		t.Errorf("orphaned definition not rolled back on the conflict path: deleted %v, want %v", defs.deleted, defs.created.ID)
	}
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

func TestDefinitionDefaults(t *testing.T) {
	srv := newHandler(api.Config{})

	get := func(t *testing.T, kind string) (int, map[string]any) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/definitions/defaults?kind="+kind, nil))
		var got map[string]any
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding body: %v; body=%s", err, rec.Body)
			}
		}
		return rec.Code, got
	}

	t.Run("discovery template carries the baseline seeds and depth", func(t *testing.T) {
		code, got := get(t, "discovery")
		if code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", code)
		}
		if got["name"] != "discovery" {
			t.Errorf("name: got %v, want discovery", got["name"])
		}
		if got["kind"] != "discovery" {
			t.Errorf("kind: got %v, want discovery", got["kind"])
		}
		if got["maxDepth"] != float64(10) {
			t.Errorf("maxDepth: got %v, want 10", got["maxDepth"])
		}
		seeds, ok := got["seedUrls"].([]any)
		if !ok {
			t.Fatalf("seedUrls should be an array, got %T", got["seedUrls"])
		}
		if len(seeds) != 2 {
			t.Errorf("seedUrls: got %d, want 2 (the test defaults)", len(seeds))
		}
		if _, present := got["keywords"]; present {
			t.Errorf("discovery template should not carry keywords, got %v", got["keywords"])
		}
	})

	t.Run("keyword template has an empty keyword list and depth", func(t *testing.T) {
		code, got := get(t, "keyword")
		if code != http.StatusOK {
			t.Fatalf("status: got %d, want 200", code)
		}
		if got["kind"] != "keyword" {
			t.Errorf("kind: got %v, want keyword", got["kind"])
		}
		if got["maxDepth"] != float64(4) {
			t.Errorf("maxDepth: got %v, want 4", got["maxDepth"])
		}
		keywords, ok := got["keywords"].([]any)
		if !ok {
			t.Fatalf("keywords should be an array (not null), got %T", got["keywords"])
		}
		if len(keywords) != 0 {
			t.Errorf("keywords should be empty, got %v", keywords)
		}
		if got["name"] != nil {
			t.Errorf("keyword template should not carry a name, got %v", got["name"])
		}
		if got["seedUrls"] != nil {
			t.Errorf("keyword template should not carry seedUrls, got %v", got["seedUrls"])
		}
	})

	t.Run("missing kind is rejected", func(t *testing.T) {
		if code, _ := get(t, ""); code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", code)
		}
	})

	t.Run("unknown kind is rejected", func(t *testing.T) {
		if code, _ := get(t, "sitemap"); code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400", code)
		}
	})
}

func TestCreateCrawlDepthDefaultingPerKind(t *testing.T) {
	t.Run("keyword crawl defaults to the keyword depth", func(t *testing.T) {
		defs := &fakeDefRepo{}
		srv := newHandler(api.Config{Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "kw",
			"kind":     "keyword",
			"keywords": []string{"golang"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
		}
		if defs.created == nil || defs.created.MaxDepth != 4 {
			t.Errorf("omitted depth not defaulted to keyword default: %+v", defs.created)
		}
	})

	t.Run("discovery crawl defaults to the discovery depth", func(t *testing.T) {
		defs := &fakeDefRepo{}
		srv := newHandler(api.Config{Definitions: defs})

		body, _ := json.Marshal(map[string]any{
			"name":     "disc",
			"kind":     "discovery",
			"seedUrls": []string{"https://example.com"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
		}
		if defs.created == nil || defs.created.MaxDepth != 10 {
			t.Errorf("omitted depth not defaulted to discovery default: %+v", defs.created)
		}
	})
}

func TestCreateCrawlRejectsOutOfBoundsDepth(t *testing.T) {
	cases := []struct {
		name     string
		maxDepth int
		want     int
	}{
		{"zero is rejected", 0, http.StatusBadRequest},
		{"above the upper bound is rejected", 21, http.StatusBadRequest},
		{"lower boundary is accepted", 1, http.StatusCreated},
		{"upper boundary is accepted", 20, http.StatusCreated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newHandler(api.Config{})

			body, _ := json.Marshal(map[string]any{
				"name":     "bounded",
				"kind":     "discovery",
				"seedUrls": []string{"https://example.com"},
				"maxDepth": tc.maxDepth,
			})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/crawls", bytes.NewReader(body)))

			if rec.Code != tc.want {
				t.Fatalf("status: got %d, want %d; body=%s", rec.Code, tc.want, rec.Body)
			}
		})
	}
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

// TestCreateDefinitionDiscoveryConflict asserts the create-only endpoint maps
// ErrDiscoveryDefinitionExists to 409, mirroring the fused createCrawl path.
func TestCreateDefinitionDiscoveryConflict(t *testing.T) {
	defs := &fakeDefRepo{createErr: crawler.ErrDiscoveryDefinitionExists}
	srv := newHandler(api.Config{Definitions: defs})

	body, _ := json.Marshal(map[string]any{
		"name":     "discovery",
		"kind":     "discovery",
		"seedUrls": []string{"https://example.com"},
	})
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/definitions", bytes.NewReader(body)))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
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

// TestStartRunActiveRunConflict asserts POST /api/definitions/{id}/runs maps the
// runner's ErrActiveRunExists to 409: a keyword or discovery definition that
// already has a non-terminal run refuses a second start (ADR-0017). startRun does
// not read the definition repo, so no def setup is needed.
func TestStartRunActiveRunConflict(t *testing.T) {
	rnr := &fakeRunner{startErr: crawler.ErrActiveRunExists}
	srv := newHandler(api.Config{Runner: rnr})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/definitions/"+uuid.New().String()+"/runs", nil))

	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d, want 409; body=%s", rec.Code, rec.Body)
	}
}

// TestAddSeed drives POST /api/definitions/{id}/seeds (ADR-0018): a Seed added
// to the Discovery Definition is durably appended and injected at depth 0 into
// the Definition's live Run, while a Keyword Definition, an invalid URL, and an
// unknown Definition are refused, and a Definition with no live Run still stores
// the Seed.
func TestAddSeed(t *testing.T) {
	post := func(t *testing.T, srv http.Handler, defID uuid.UUID, url string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"url": url})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/definitions/"+defID.String()+"/seeds", bytes.NewReader(body)))
		return rec
	}

	t.Run("appends and injects into the active run", func(t *testing.T) {
		def := &crawler.CrawlDefinition{
			ID:       uuid.New(),
			Kind:     crawler.CrawlKindDiscovery,
			SeedURLs: []string{"https://old.example.com"},
		}
		defs := &fakeDefRepo{get: def}
		run := &crawler.CrawlRun{ID: uuid.New(), DefinitionID: def.ID, Status: crawler.RunStatusRunning}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}

		var seededRun []uuid.UUID
		var seededURL []crawler.URL
		seeder := func(ctx context.Context, runID uuid.UUID, u crawler.URL) error {
			seededRun = append(seededRun, runID)
			seededURL = append(seededURL, u)
			return nil
		}
		srv := newHandler(api.Config{Definitions: defs, Runs: runs, FrontierSeeder: seeder})

		rec := post(t, srv, def.ID, "https://newdir.example.com/companies")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if !slices.Contains(defs.appended, "https://newdir.example.com/companies") {
			t.Errorf("normalized seed not appended: %v", defs.appended)
		}
		if len(seededRun) != 1 || seededRun[0] != run.ID {
			t.Errorf("seeder should be called once for the active run; got %v", seededRun)
		}
		if len(seededURL) != 1 || seededURL[0].Depth != 0 {
			t.Errorf("injected seed should be depth 0; got %+v", seededURL)
		}
		if len(seededURL) == 1 && seededURL[0].RawURL != "https://newdir.example.com/companies" {
			t.Errorf("injected seed url: got %q", seededURL[0].RawURL)
		}
		var got struct {
			SeedURLs []string `json:"seedUrls"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !slices.Contains(got.SeedURLs, "https://newdir.example.com/companies") {
			t.Errorf("response seedUrls should include the new seed; got %v", got.SeedURLs)
		}
	})

	t.Run("keyword definition is refused", func(t *testing.T) {
		def := &crawler.CrawlDefinition{ID: uuid.New(), Kind: crawler.CrawlKindKeyword, Keywords: []string{"go"}}
		defs := &fakeDefRepo{get: def}
		var seededRun []uuid.UUID
		seeder := func(ctx context.Context, runID uuid.UUID, u crawler.URL) error {
			seededRun = append(seededRun, runID)
			return nil
		}
		srv := newHandler(api.Config{Definitions: defs, FrontierSeeder: seeder})

		rec := post(t, srv, def.ID, "https://newdir.example.com")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
		if len(defs.appended) != 0 {
			t.Errorf("keyword definition must not append a seed; got %v", defs.appended)
		}
		if len(seededRun) != 0 {
			t.Errorf("keyword definition must not inject a seed; got %v", seededRun)
		}
	})

	t.Run("invalid url is refused", func(t *testing.T) {
		for _, url := range []string{"", "://bad"} {
			def := &crawler.CrawlDefinition{ID: uuid.New(), Kind: crawler.CrawlKindDiscovery, SeedURLs: []string{}}
			defs := &fakeDefRepo{get: def}
			srv := newHandler(api.Config{Definitions: defs})

			rec := post(t, srv, def.ID, url)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("url %q: status got %d, want 400; body=%s", url, rec.Code, rec.Body)
			}
			if len(defs.appended) != 0 {
				t.Errorf("url %q: invalid url must not append; got %v", url, defs.appended)
			}
		}
	})

	t.Run("no active run still appends", func(t *testing.T) {
		def := &crawler.CrawlDefinition{ID: uuid.New(), Kind: crawler.CrawlKindDiscovery, SeedURLs: []string{}}
		defs := &fakeDefRepo{get: def}
		// Only a terminal run for the def, so there is no live frontier to inject into.
		run := &crawler.CrawlRun{ID: uuid.New(), DefinitionID: def.ID, Status: crawler.RunStatusCompleted}
		runs := &fakeRunRepo{runs: []*crawler.CrawlRun{run}}
		var seededRun []uuid.UUID
		seeder := func(ctx context.Context, runID uuid.UUID, u crawler.URL) error {
			seededRun = append(seededRun, runID)
			return nil
		}
		srv := newHandler(api.Config{Definitions: defs, Runs: runs, FrontierSeeder: seeder})

		rec := post(t, srv, def.ID, "https://newdir.example.com")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if !slices.Contains(defs.appended, "https://newdir.example.com") {
			t.Errorf("seed should still be appended without a live run; got %v", defs.appended)
		}
		if len(seededRun) != 0 {
			t.Errorf("no live run: injection must be skipped; got %v", seededRun)
		}
	})

	t.Run("re-adding an existing seed returns 200", func(t *testing.T) {
		def := &crawler.CrawlDefinition{
			ID:       uuid.New(),
			Kind:     crawler.CrawlKindDiscovery,
			SeedURLs: []string{"https://dup.example.com"},
		}
		defs := &fakeDefRepo{get: def}
		srv := newHandler(api.Config{Definitions: defs})

		rec := post(t, srv, def.ID, "https://dup.example.com")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		// The handler still delegates to AppendSeedURL — the DB enforces the real
		// idempotency (proven in the postgres seam) — and the response reflects
		// the seed exactly once.
		if len(defs.appended) != 1 {
			t.Errorf("append should still be delegated once; got %v", defs.appended)
		}
		var got struct {
			SeedURLs []string `json:"seedUrls"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		count := 0
		for _, s := range got.SeedURLs {
			if s == "https://dup.example.com" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("seed should appear exactly once, got %d in %v", count, got.SeedURLs)
		}
	})

	t.Run("unknown definition is 404", func(t *testing.T) {
		// Get matches only `other`'s id, so a POST to a different id is a 404.
		other := &crawler.CrawlDefinition{ID: uuid.New(), Kind: crawler.CrawlKindDiscovery}
		defs := &fakeDefRepo{get: other}
		srv := newHandler(api.Config{Definitions: defs})

		rec := post(t, srv, uuid.New(), "https://newdir.example.com")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
		if len(defs.appended) != 0 {
			t.Errorf("unknown definition must not append; got %v", defs.appended)
		}
	})
}

func TestListCompanies(t *testing.T) {
	companies := &fakeCompanyRepo{companies: []*crawler.Company{
		{ID: uuid.New(), CompanyKey: "greenhouse:acme", ATSProvider: "greenhouse", Name: "Acme", Website: "https://acme.io"},
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
	if got[0]["website"] != "https://acme.io" {
		t.Errorf("website not carried by the DTO: got %v, want https://acme.io", got[0]["website"])
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
