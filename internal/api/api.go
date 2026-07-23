// Package api exposes the crawl-management REST API. It depends only on the
// runner and the repository interfaces, so it can be tested without a real
// database or crawl stack.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/runner"
)

// Runner is the subset of the run lifecycle the API drives.
type Runner interface {
	Start(ctx context.Context, definitionID uuid.UUID) (*crawler.CrawlRun, error)
	Stop(ctx context.Context, runID uuid.UUID) error
	Pause(ctx context.Context, runID uuid.UUID) error
	Resume(ctx context.Context, runID uuid.UUID) error
}

// FrontierSizer reports the number of URLs still in a run's frontier (queued
// plus in-flight). It is injected so the api package stays decoupled from Redis,
// mirroring runner.WithFrontierCleaner; in the server it wraps
// redisfrontier.Len. A nil sizer makes every frontier size report 0. It backs
// both the live-status endpoint and the frontierSize field on the run read
// endpoints (see h.frontierSize).
type FrontierSizer func(ctx context.Context, runID uuid.UUID) (int64, error)

// FrontierSeeder injects a URL into a run's live Frontier at depth 0 (the
// ordinary depth-0 add). It is injected so the api package stays decoupled from
// Redis, mirroring FrontierSizer; in the server it constructs a redisfrontier
// for the run and calls AddURL. A nil seeder makes runtime Seed injection a
// no-op — the Seed is still durably appended to the Definition (ADR-0018).
type FrontierSeeder func(ctx context.Context, runID uuid.UUID, url crawler.URL) error

// Defaults fill in the fields a create request omits, so a minimal
// {name, seedUrls} request yields a working crawl, and back the
// GET /api/definitions/defaults prefill endpoint. Sourced from the built-in
// domain defaults wired in cmd/server (crawler.DefaultDiscoverySeeds,
// crawler.DefaultURLFilterConfig).
type Defaults struct {
	// DiscoveryMaxDepth is the crawl-depth default applied when a discovery
	// create request omits maxDepth.
	DiscoveryMaxDepth int
	// DiscoverySeeds is the baseline Seed set the Discovery start modal prefills.
	DiscoverySeeds []string
	URLFilter      crawler.URLFilterConfig
}

// Config groups the Handler's dependencies. Every repository the read endpoints
// serve is required; FrontierSizer is optional (nil → the status endpoint
// reports a zero frontier size).
type Config struct {
	Runner      Runner
	Runs        crawler.CrawlRunRepository
	Definitions crawler.CrawlDefinitionRepository
	Companies   crawler.CompanyRepository
	CareerPages crawler.CareerPageRepository
	// Importer starts Catalog Imports; required for POST /api/catalog/import.
	Importer Importer
	// ImportJobs serves the Import Job read endpoints (list + get by id).
	ImportJobs crawler.ImportJobRepository
	// SavedSearches backs the SavedSearch CRUD endpoints (ADR-0037).
	SavedSearches crawler.SavedSearchRepository
	// Search answers a SavedSearch's results endpoint against the Corpus (ADR-0037).
	Search        crawler.CorpusSearchRepository
	FrontierSizer FrontierSizer
	// FrontierSeeder injects a runtime Seed into a Discovery Run's live Frontier;
	// optional (nil → the Seed is still durably appended, injection is skipped).
	FrontierSeeder FrontierSeeder
	Defaults       Defaults
}

type Handler struct {
	cfg Config
}

func New(cfg Config) *Handler {
	return &Handler{cfg: cfg}
}

// Routes returns the API mux. All routes are under /api.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Runs (legacy create-and-start plus lifecycle).
	mux.HandleFunc("POST /api/crawls", h.createCrawl)
	mux.HandleFunc("GET /api/crawls", h.listCrawls)
	mux.HandleFunc("GET /api/crawls/{id}", h.getCrawl)
	mux.HandleFunc("GET /api/crawls/{id}/status", h.getCrawlStatus)
	mux.HandleFunc("POST /api/crawls/{id}/stop", h.stopCrawl)
	mux.HandleFunc("POST /api/crawls/{id}/pause", h.pauseCrawl)
	mux.HandleFunc("POST /api/crawls/{id}/resume", h.resumeCrawl)

	// Definitions: a re-runnable library (create is split from start, ADR-0005).
	mux.HandleFunc("GET /api/definitions", h.listDefinitions)
	mux.HandleFunc("GET /api/definitions/defaults", h.getDefinitionDefaults)
	mux.HandleFunc("GET /api/definitions/{id}", h.getDefinition)
	mux.HandleFunc("POST /api/definitions", h.createDefinition)
	mux.HandleFunc("POST /api/definitions/{id}/runs", h.startRun)
	mux.HandleFunc("POST /api/definitions/{id}/seeds", h.addSeed)

	// Catalog (read-only browse).
	mux.HandleFunc("GET /api/companies", h.listCompanies)
	mux.HandleFunc("GET /api/career-pages", h.listCareerPages)
	mux.HandleFunc("GET /api/catalog-history", h.catalogHistory)
	mux.HandleFunc("GET /api/catalog/export", h.exportCatalog)
	mux.HandleFunc("POST /api/catalog/import", h.importCatalog)
	mux.HandleFunc("GET /api/catalog/import-jobs", h.listImportJobs)
	mux.HandleFunc("GET /api/catalog/import-jobs/{id}", h.getImportJob)

	// SavedSearches (ADR-0037): named Corpus queries + their live results.
	mux.HandleFunc("GET /api/saved-searches", h.listSavedSearches)
	mux.HandleFunc("POST /api/saved-searches", h.createSavedSearch)
	mux.HandleFunc("PATCH /api/saved-searches/{id}", h.renameSavedSearch)
	mux.HandleFunc("DELETE /api/saved-searches/{id}", h.deleteSavedSearch)
	mux.HandleFunc("GET /api/saved-searches/{id}/results", h.savedSearchResults)

	return mux
}

// --- Requests + DTOs ---

type createCrawlRequest struct {
	Name      string                   `json:"name"`
	SeedURLs  []string                 `json:"seedUrls"`
	Kind      string                   `json:"kind"`
	MaxDepth  *int                     `json:"maxDepth"`
	URLFilter *crawler.URLFilterConfig `json:"urlFilter"`
}

type runDTO struct {
	ID            string `json:"id"`
	DefinitionID  string `json:"definitionId"`
	Status        string `json:"status"`
	PagesCrawled  int64  `json:"pagesCrawled"`
	ListingsFound int64  `json:"listingsFound"`
	// FrontierSize is live, transient state read from Redis on the run read
	// endpoints (list/get), sparing the dashboard an N+1 poll to
	// /crawls/{id}/status per card. toRunDTO leaves it 0; the read handlers
	// enrich it via h.frontierSize. It is therefore 0 on create/start responses
	// (a just-created run's frontier is still seeding — poll to observe it).
	FrontierSize int64      `json:"frontierSize"`
	StartedAt    time.Time  `json:"startedAt"`
	FinishedAt   *time.Time `json:"finishedAt"`
	Error        string     `json:"error"`
}

func toRunDTO(run *crawler.CrawlRun) runDTO {
	return runDTO{
		ID:            run.ID.String(),
		DefinitionID:  run.DefinitionID.String(),
		Status:        string(run.Status),
		PagesCrawled:  run.Counters.PagesCrawled,
		ListingsFound: run.Counters.ListingsFound,
		StartedAt:     run.StartedAt,
		FinishedAt:    run.FinishedAt,
		Error:         run.Error,
	}
}

type definitionDTO struct {
	ID        string                  `json:"id"`
	Name      string                  `json:"name"`
	Kind      string                  `json:"kind"`
	SeedURLs  []string                `json:"seedUrls"`
	MaxDepth  int                     `json:"maxDepth"`
	URLFilter crawler.URLFilterConfig `json:"urlFilter"`
	CreatedAt time.Time               `json:"createdAt"`
}

func toDefinitionDTO(def *crawler.CrawlDefinition) definitionDTO {
	// Coalesce a nil slice so the JSON is [] rather than null, keeping the
	// frontend's array handling uniform.
	seedURLs := def.SeedURLs
	if seedURLs == nil {
		seedURLs = []string{}
	}
	return definitionDTO{
		ID:        def.ID.String(),
		Name:      def.Name,
		Kind:      string(def.Kind),
		SeedURLs:  seedURLs,
		MaxDepth:  def.MaxDepth,
		URLFilter: def.URLFilter,
		CreatedAt: def.CreatedAt,
	}
}

type companyDTO struct {
	ID            string    `json:"id"`
	CompanyKey    string    `json:"companyKey"`
	ATSProvider   string    `json:"atsProvider"`
	DisplayDomain string    `json:"displayDomain"`
	Website       string    `json:"website"`
	Name          string    `json:"name"`
	NameSource    string    `json:"nameSource"`
	FirstSeen     time.Time `json:"firstSeen"`
	LastSeen      time.Time `json:"lastSeen"`
}

func toCompanyDTO(c *crawler.Company) companyDTO {
	return companyDTO{
		ID:            c.ID.String(),
		CompanyKey:    c.CompanyKey,
		ATSProvider:   c.ATSProvider,
		DisplayDomain: c.DisplayDomain,
		Website:       c.Website,
		Name:          c.Name,
		NameSource:    string(c.NameSource),
		FirstSeen:     c.FirstSeen,
		LastSeen:      c.LastSeen,
	}
}

type careerPageDTO struct {
	ID               string    `json:"id"`
	CompanyID        string    `json:"companyId"`
	URL              string    `json:"url"`
	PolitenessDomain string    `json:"politenessDomain"`
	FirstSeen        time.Time `json:"firstSeen"`
	LastSeen         time.Time `json:"lastSeen"`
}

func toCareerPageDTO(p *crawler.CareerPage) careerPageDTO {
	return careerPageDTO{
		ID:               p.ID.String(),
		CompanyID:        p.CompanyID.String(),
		URL:              p.URL,
		PolitenessDomain: p.PolitenessDomain,
		FirstSeen:        p.FirstSeen,
		LastSeen:         p.LastSeen,
	}
}

// catalogHistoryResponse is the Catalog History sparkline series: a cumulative,
// daily, gap-filled growth curve of catalogued Career Pages. It is an object
// rather than a bare array so a parallel company-growth series
// (// Companies []int `json:"companies"`) can be added later without breaking
// the client.
type catalogHistoryResponse struct {
	CareerPages []int `json:"careerPages"`
}

// runStatusDTO is a run's live progress: durable counters from the run row plus
// the transient frontier size read from Redis.
type runStatusDTO struct {
	PagesCrawled  int64 `json:"pagesCrawled"`
	ListingsFound int64 `json:"listingsFound"`
	FrontierSize  int64 `json:"frontierSize"`
}

// --- Run handlers ---

// createCrawl is the legacy fused endpoint: it creates a definition and
// immediately starts a run of it. Kept working so nothing breaks mid-migration;
// new clients should POST /api/definitions then POST /api/definitions/{id}/runs.
func (h *Handler) createCrawl(w http.ResponseWriter, r *http.Request) {
	def, msg := h.decodeDefinition(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if err := h.cfg.Definitions.Create(r.Context(), def); err != nil {
		if errors.Is(err, crawler.ErrDiscoveryDefinitionExists) {
			writeError(w, http.StatusConflict, "a discovery crawl already exists")
			return
		}
		slog.Error("api: error creating crawl definition", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create crawl")
		return
	}

	run, err := h.cfg.Runner.Start(r.Context(), def.ID)
	if err != nil {
		// The definition was committed but its run never started, leaving an
		// orphan. Best-effort roll it back so the fused endpoint stays atomic.
		// The cleanup uses a context detached from the request's cancellation
		// (a cancelled request context is itself a Start failure mode) and only
		// logs if the rollback fails.
		h.deleteOrphanDefinition(r.Context(), def.ID)
		if errors.Is(err, crawler.ErrActiveRunExists) {
			writeError(w, http.StatusConflict, "a run of this crawl is already active")
			return
		}
		slog.Error("api: error starting crawl", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start crawl")
		return
	}

	writeJSON(w, http.StatusCreated, toRunDTO(run))
}

// definitionDeleter is the optional rollback capability the fused createCrawl
// endpoint uses to drop an orphaned definition when the start step fails. A
// repository that does not implement it simply skips the cleanup.
type definitionDeleter interface {
	Delete(ctx context.Context, id uuid.UUID) error
}

// deleteOrphanDefinition best-effort removes a just-created definition whose run
// failed to start. It detaches from the caller's context so a cancelled request
// (itself a start-failure cause) does not also abort the rollback, and logs but
// does not surface a failed cleanup. No-op when the repository cannot delete.
func (h *Handler) deleteOrphanDefinition(ctx context.Context, id uuid.UUID) {
	deleter, ok := h.cfg.Definitions.(definitionDeleter)
	if !ok {
		return
	}
	if err := deleter.Delete(context.WithoutCancel(ctx), id); err != nil {
		slog.Error("api: error deleting orphaned crawl definition", "err", err, "definitionId", id)
	}
}

func (h *Handler) listCrawls(w http.ResponseWriter, r *http.Request) {
	runs, err := h.cfg.Runs.List(r.Context())
	if err != nil {
		slog.Error("api: error listing crawls", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list crawls")
		return
	}

	dtos := make([]runDTO, 0, len(runs))
	for _, run := range runs {
		dto := toRunDTO(run)
		dto.FrontierSize = h.frontierSize(r.Context(), run)
		dtos = append(dtos, dto)
	}

	writeJSON(w, http.StatusOK, dtos)
}

func (h *Handler) getCrawl(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	run, err := h.cfg.Runs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "crawl not found")
			return
		}
		slog.Error("api: error getting crawl", "err", err)
		writeError(w, http.StatusInternalServerError, "could not get crawl")
		return
	}

	dto := toRunDTO(run)
	dto.FrontierSize = h.frontierSize(r.Context(), run)
	writeJSON(w, http.StatusOK, dto)
}

// frontierSize resolves a run's live frontier size (queued + in-flight URLs)
// best-effort. Frontier state is transient and lives in Redis, not the run row,
// so it is read per request rather than stored by toRunDTO. A terminal run has
// no frontier — its keys are cleaned up — so it reports 0 without touching
// Redis, which keeps listCrawls cheap since historical runs dominate it. A nil
// sizer or a transient sizer error also degrades to 0 rather than failing the
// whole response.
func (h *Handler) frontierSize(ctx context.Context, run *crawler.CrawlRun) int64 {
	if h.cfg.FrontierSizer == nil || run.Status.Terminal() {
		return 0
	}
	size, err := h.cfg.FrontierSizer(ctx, run.ID)
	if err != nil {
		slog.Error("api: error reading frontier size", "err", err, "runId", run.ID)
		return 0
	}
	return size
}

// getCrawlStatus returns a run's live progress: durable counters from Postgres
// plus the frontier size read live from Redis via the injected sizer.
func (h *Handler) getCrawlStatus(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	run, err := h.cfg.Runs.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "crawl not found")
			return
		}
		slog.Error("api: error getting crawl status", "err", err)
		writeError(w, http.StatusInternalServerError, "could not get crawl status")
		return
	}

	writeJSON(w, http.StatusOK, runStatusDTO{
		PagesCrawled:  run.Counters.PagesCrawled,
		ListingsFound: run.Counters.ListingsFound,
		FrontierSize:  h.frontierSize(r.Context(), run),
	})
}

// stopCrawl requests a stop of a run: 202 on success, 409 when the run is not
// stoppable (already terminal), 404 for an unknown id. The existence Get is
// required for the same reason as pauseCrawl/resumeCrawl: the runner cannot
// distinguish an unknown id from a non-active run, so without it a 404 for an
// unknown id would collapse into a 409.
func (h *Handler) stopCrawl(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	if _, err := h.cfg.Runs.Get(r.Context(), id); err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "crawl not found")
			return
		}
		slog.Error("api: error getting crawl before stop", "err", err)
		writeError(w, http.StatusInternalServerError, "could not stop crawl")
		return
	}

	if err := h.cfg.Runner.Stop(r.Context(), id); err != nil {
		if errors.Is(err, runner.ErrRunNotActive) {
			writeError(w, http.StatusConflict, "crawl is not running")
			return
		}
		slog.Error("api: error stopping crawl", "err", err)
		writeError(w, http.StatusInternalServerError, "could not stop crawl")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// pauseCrawl requests a pause of a running run: 202 on success, 409 when the run
// is not the live running run (any other state), 404 for an unknown id. The
// existence Get is required because the runner's active set cannot distinguish
// an unknown id from a terminal/paused run — both are simply "not active" — so a
// 404 for an unknown id would otherwise collapse into a 409.
func (h *Handler) pauseCrawl(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	if _, err := h.cfg.Runs.Get(r.Context(), id); err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "crawl not found")
			return
		}
		slog.Error("api: error getting crawl before pause", "err", err)
		writeError(w, http.StatusInternalServerError, "could not pause crawl")
		return
	}

	if err := h.cfg.Runner.Pause(r.Context(), id); err != nil {
		if errors.Is(err, runner.ErrRunNotActive) {
			writeError(w, http.StatusConflict, "crawl is not running")
			return
		}
		slog.Error("api: error pausing crawl", "err", err)
		writeError(w, http.StatusInternalServerError, "could not pause crawl")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// resumeCrawl relaunches a paused run: 202 on success, 409 from any other state
// (running/pausing/stopping/terminal), 404 for an unknown id. The existence Get is
// required for the same reason as pauseCrawl: the runner returns ErrRunNotPaused
// for both an unknown id and a non-paused run, so without it a 404 would collapse
// into a 409.
func (h *Handler) resumeCrawl(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	if _, err := h.cfg.Runs.Get(r.Context(), id); err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "crawl not found")
			return
		}
		slog.Error("api: error getting crawl before resume", "err", err)
		writeError(w, http.StatusInternalServerError, "could not resume crawl")
		return
	}

	if err := h.cfg.Runner.Resume(r.Context(), id); err != nil {
		if errors.Is(err, runner.ErrRunNotPaused) {
			writeError(w, http.StatusConflict, "crawl is not paused")
			return
		}
		slog.Error("api: error resuming crawl", "err", err)
		writeError(w, http.StatusInternalServerError, "could not resume crawl")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// --- Definition handlers ---

func (h *Handler) listDefinitions(w http.ResponseWriter, r *http.Request) {
	defs, err := h.cfg.Definitions.List(r.Context())
	if err != nil {
		slog.Error("api: error listing definitions", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list definitions")
		return
	}

	dtos := make([]definitionDTO, 0, len(defs))
	for _, def := range defs {
		dtos = append(dtos, toDefinitionDTO(def))
	}

	writeJSON(w, http.StatusOK, dtos)
}

func (h *Handler) getDefinition(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid definition id")
		return
	}

	def, err := h.cfg.Definitions.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		slog.Error("api: error getting definition", "err", err)
		writeError(w, http.StatusInternalServerError, "could not get definition")
		return
	}

	writeJSON(w, http.StatusOK, toDefinitionDTO(def))
}

// discoveryDefaultsDTO is the prefill template returned by
// GET /api/definitions/defaults?kind=discovery, matching the Discovery modal's
// editable fields: an auto-named ("discovery") Seed list and depth.
type discoveryDefaultsDTO struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	SeedURLs []string `json:"seedUrls"`
	MaxDepth int      `json:"maxDepth"`
}

// getDefinitionDefaults returns the modal prefill template for ?kind=discovery.
// Discovery is the only live kind, so an unknown or missing kind is a 400: the
// client must ask for it explicitly.
func (h *Handler) getDefinitionDefaults(w http.ResponseWriter, r *http.Request) {
	switch crawler.CrawlKind(r.URL.Query().Get("kind")) {
	case crawler.CrawlKindDiscovery:
		// Coalesce nil so seedUrls is [] rather than null, matching the rest of
		// the API's array handling.
		seeds := h.cfg.Defaults.DiscoverySeeds
		if seeds == nil {
			seeds = []string{}
		}
		writeJSON(w, http.StatusOK, discoveryDefaultsDTO{
			Name:     "discovery",
			Kind:     string(crawler.CrawlKindDiscovery),
			SeedURLs: seeds,
			MaxDepth: h.cfg.Defaults.DiscoveryMaxDepth,
		})
	default:
		writeError(w, http.StatusBadRequest, "unknown or missing kind")
	}
}

// createDefinition persists a definition without starting a run. The definition
// becomes a re-runnable library entry started via POST /api/definitions/{id}/runs.
func (h *Handler) createDefinition(w http.ResponseWriter, r *http.Request) {
	def, msg := h.decodeDefinition(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if err := h.cfg.Definitions.Create(r.Context(), def); err != nil {
		if errors.Is(err, crawler.ErrDiscoveryDefinitionExists) {
			writeError(w, http.StatusConflict, "a discovery crawl already exists")
			return
		}
		slog.Error("api: error creating definition", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create definition")
		return
	}

	writeJSON(w, http.StatusCreated, toDefinitionDTO(def))
}

// startRun launches a new run of an existing definition. A missing definition
// maps to 404 (runner.Start propagates the repository's ErrNotFound).
func (h *Handler) startRun(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid definition id")
		return
	}

	run, err := h.cfg.Runner.Start(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		if errors.Is(err, crawler.ErrActiveRunExists) {
			writeError(w, http.StatusConflict, "a run of this crawl is already active")
			return
		}
		slog.Error("api: error starting run", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start run")
		return
	}

	writeJSON(w, http.StatusCreated, toRunDTO(run))
}

type addSeedRequest struct {
	URL string `json:"url"`
}

// addSeed appends a Seed to a Discovery Definition and injects it into the live
// Run's Frontier at depth 0 (ADR-0018). Discovery-only: any non-discovery
// Definition (e.g. a retired keyword row) is refused. The durable
// append is idempotent; the live injection targets the ≤1 non-terminal Run of
// the Definition (guaranteed by the one-active-run index) and is best-effort —
// a failed injection leaves the Seed durably stored for the next restart, so it
// does not fail the request. Returns the updated definition so the client
// reflects the new Seed list.
func (h *Handler) addSeed(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid definition id")
		return
	}

	def, err := h.cfg.Definitions.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		slog.Error("api: error getting definition before seed add", "err", err)
		writeError(w, http.StatusInternalServerError, "could not add seed")
		return
	}
	if def.Kind != crawler.CrawlKindDiscovery {
		writeError(w, http.StatusBadRequest, "seeds can only be added to a discovery crawl")
		return
	}

	var req addSeedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// NewURL rejects an empty or malformed URL and normalizes the rest, so the
	// Seed stored and injected is canonical (matches the Frontier's dedup key).
	seed, err := crawler.NewURL(req.URL)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid seed url")
		return
	}

	if err := h.cfg.Definitions.AppendSeedURL(r.Context(), id, seed.RawURL); err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "definition not found")
			return
		}
		slog.Error("api: error appending seed url", "err", err)
		writeError(w, http.StatusInternalServerError, "could not add seed")
		return
	}

	// Best-effort live injection; the Seed is already durable (ADR-0018).
	if err := h.injectSeed(r.Context(), id, seed); err != nil {
		slog.Error("api: error injecting seed into live frontier", "err", err, "definitionId", id)
	}

	// Reflect the append locally, mirroring the repo's idempotent semantics,
	// rather than issuing a second Get.
	if !slices.Contains(def.SeedURLs, seed.RawURL) {
		def.SeedURLs = append(def.SeedURLs, seed.RawURL)
	}
	writeJSON(w, http.StatusOK, toDefinitionDTO(def))
}

// injectSeed adds seed at depth 0 to the live Frontier of the Definition's
// non-terminal Run, if one exists. The one-active-run index (ADR-0017) bounds
// this to at most one Run, so it injects into at most one Frontier. A nil
// FrontierSeeder or Runs repo, or no non-terminal Run, makes it a no-op.
func (h *Handler) injectSeed(ctx context.Context, definitionID uuid.UUID, seed crawler.URL) error {
	if h.cfg.FrontierSeeder == nil || h.cfg.Runs == nil {
		return nil
	}
	runs, err := h.cfg.Runs.ListByStatus(ctx,
		crawler.RunStatusRunning, crawler.RunStatusStopping,
		crawler.RunStatusPausing, crawler.RunStatusPaused,
	)
	if err != nil {
		return fmt.Errorf("listing non-terminal runs: %w", err)
	}
	for _, run := range runs {
		if run.DefinitionID == definitionID {
			return h.cfg.FrontierSeeder(ctx, run.ID, seed)
		}
	}
	return nil
}

// minCrawlDepth and maxCrawlDepth bound a definition's editable crawl depth. A
// request outside this range is rejected; an omitted depth falls back to
// Defaults.DiscoveryMaxDepth, which always sits inside the range.
const (
	minCrawlDepth = 1
	maxCrawlDepth = 20
)

// decodeDefinition parses and validates a create request into a CrawlDefinition,
// filling omitted fields from the configured defaults. It returns a non-empty
// message describing the first validation failure; "" means the definition is
// valid. Shared by the legacy createCrawl and the create-only createDefinition.
func (h *Handler) decodeDefinition(r *http.Request) (*crawler.CrawlDefinition, string) {
	var req createCrawlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return nil, "invalid request body"
	}

	if req.Name == "" {
		return nil, "name is required"
	}

	kind := crawler.CrawlKind(req.Kind)
	if kind == "" {
		kind = crawler.CrawlKindDiscovery
	}
	// Discovery is the only live kind; any other (including the retired "keyword")
	// is rejected before it can reach the factory.
	if kind != crawler.CrawlKindDiscovery {
		return nil, "unknown crawl kind"
	}
	if len(req.SeedURLs) == 0 {
		return nil, "seedUrls are required for a discovery crawl"
	}
	// Normalize each Seed to the same canonical form the add-seed path uses
	// (crawler.NewURL), so a Seed stored at creation and the same Seed
	// re-added later via /seeds dedupe against each other instead of
	// accumulating a near-duplicate (e.g. a stored trailing slash). This
	// also validates the Seeds up front.
	seedURLs := make([]string, 0, len(req.SeedURLs))
	for _, raw := range req.SeedURLs {
		u, err := crawler.NewURL(raw)
		if err != nil {
			return nil, fmt.Sprintf("invalid seed url: %q", raw)
		}
		seedURLs = append(seedURLs, u.RawURL)
	}

	maxDepth := valueOr(req.MaxDepth, h.cfg.Defaults.DiscoveryMaxDepth)
	if maxDepth < minCrawlDepth || maxDepth > maxCrawlDepth {
		return nil, fmt.Sprintf("maxDepth must be between %d and %d", minCrawlDepth, maxCrawlDepth)
	}

	def := &crawler.CrawlDefinition{
		Name:      req.Name,
		Kind:      kind,
		SeedURLs:  seedURLs,
		MaxDepth:  maxDepth,
		URLFilter: h.cfg.Defaults.URLFilter,
	}
	if req.URLFilter != nil {
		def.URLFilter = *req.URLFilter
	}

	return def, ""
}

// --- Catalog handlers ---

func (h *Handler) listCompanies(w http.ResponseWriter, r *http.Request) {
	companies, err := h.cfg.Companies.List(r.Context())
	if err != nil {
		slog.Error("api: error listing companies", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list companies")
		return
	}

	dtos := make([]companyDTO, 0, len(companies))
	for _, c := range companies {
		dtos = append(dtos, toCompanyDTO(c))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// listCareerPages returns catalogued Career Pages, optionally narrowed to one
// company via ?companyId=. An invalid companyId is a 400; an unknown-but-valid
// id simply matches nothing.
func (h *Handler) listCareerPages(w http.ResponseWriter, r *http.Request) {
	var filterCompanyID uuid.UUID
	if raw := r.URL.Query().Get("companyId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid companyId")
			return
		}
		filterCompanyID = id
	}

	pages, err := h.cfg.CareerPages.List(r.Context())
	if err != nil {
		slog.Error("api: error listing career pages", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list career pages")
		return
	}

	dtos := make([]careerPageDTO, 0, len(pages))
	for _, p := range pages {
		if filterCompanyID != uuid.Nil && p.CompanyID != filterCompanyID {
			continue
		}
		dtos = append(dtos, toCareerPageDTO(p))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// maxCatalogHistoryPoints caps the sparkline series so a long-lived Catalog does
// not stream thousands of daily points to the dashboard. The <Sparkline> renders
// into a 280×70 viewBox (see primitives.tsx), where points past roughly 90 merge
// into an indistinguishable line; the backend downsamples to keep the payload
// small while the curve's shape is preserved.
const maxCatalogHistoryPoints = 90

// catalogHistory serves the Catalog History growth sparkline: the cumulative,
// gap-filled daily count of catalogued Career Pages, reconstructed read-only from
// their first_seen timestamps (ADR-0012). Its endpoint equals the live "career
// pages catalogued" count, so the two can never drift.
func (h *Handler) catalogHistory(w http.ResponseWriter, r *http.Request) {
	counts, err := h.cfg.CareerPages.FirstSeenByDay(r.Context())
	if err != nil {
		slog.Error("api: error reading catalog history", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read catalog history")
		return
	}

	series := catalogSparkline(counts, time.Now(), maxCatalogHistoryPoints)
	writeJSON(w, http.StatusOK, catalogHistoryResponse{CareerPages: series})
}

// --- Helpers ---

// valueOr returns *p when p is non-nil, otherwise fallback.
func valueOr(p *int, fallback int) int {
	if p != nil {
		return *p
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("api: error encoding response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
