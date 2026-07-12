// Package api exposes the crawl-management REST API. It depends only on the
// runner and the repository interfaces, so it can be tested without a real
// database or crawl stack.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
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
// redisfrontier.Len. A nil sizer makes the live-status endpoint report 0.
type FrontierSizer func(ctx context.Context, runID uuid.UUID) (int64, error)

// Defaults fill in the fields a create request omits, so a minimal
// {name, seedUrls} request yields a working crawl. Sourced from the built-in
// defaults wired in cmd/server (see crawler.DefaultURLFilterConfig).
type Defaults struct {
	MaxDepth   int
	MaxDomains int
	URLFilter  crawler.URLFilterConfig
}

// Config groups the Handler's dependencies. Every repository the read endpoints
// serve is required; FrontierSizer is optional (nil → the status endpoint
// reports a zero frontier size).
type Config struct {
	Runner        Runner
	Runs          crawler.CrawlRunRepository
	Definitions   crawler.CrawlDefinitionRepository
	Companies     crawler.CompanyRepository
	CareerPages   crawler.CareerPageRepository
	Listings      crawler.JobListingRepository
	FrontierSizer FrontierSizer
	Defaults      Defaults
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
	mux.HandleFunc("GET /api/definitions/{id}", h.getDefinition)
	mux.HandleFunc("POST /api/definitions", h.createDefinition)
	mux.HandleFunc("POST /api/definitions/{id}/runs", h.startRun)

	// Catalog + listings (read-only browse).
	mux.HandleFunc("GET /api/companies", h.listCompanies)
	mux.HandleFunc("GET /api/career-pages", h.listCareerPages)
	mux.HandleFunc("GET /api/listings", h.listListings)

	return mux
}

// --- Requests + DTOs ---

type createCrawlRequest struct {
	Name       string                   `json:"name"`
	SeedURLs   []string                 `json:"seedUrls"`
	Kind       string                   `json:"kind"`
	Keywords   []string                 `json:"keywords"`
	MaxDepth   *int                     `json:"maxDepth"`
	MaxDomains *int                     `json:"maxDomains"`
	URLFilter  *crawler.URLFilterConfig `json:"urlFilter"`
}

type runDTO struct {
	ID            string     `json:"id"`
	DefinitionID  string     `json:"definitionId"`
	Status        string     `json:"status"`
	PagesCrawled  int64      `json:"pagesCrawled"`
	ListingsFound int64      `json:"listingsFound"`
	StartedAt     time.Time  `json:"startedAt"`
	FinishedAt    *time.Time `json:"finishedAt"`
	Error         string     `json:"error"`
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
	ID         string                  `json:"id"`
	Name       string                  `json:"name"`
	Kind       string                  `json:"kind"`
	SeedURLs   []string                `json:"seedUrls"`
	Keywords   []string                `json:"keywords"`
	MaxDepth   int                     `json:"maxDepth"`
	MaxDomains int                     `json:"maxDomains"`
	URLFilter  crawler.URLFilterConfig `json:"urlFilter"`
	CreatedAt  time.Time               `json:"createdAt"`
}

func toDefinitionDTO(def *crawler.CrawlDefinition) definitionDTO {
	// Coalesce nil slices so the JSON is [] rather than null, keeping the
	// frontend's array handling uniform.
	seedURLs := def.SeedURLs
	if seedURLs == nil {
		seedURLs = []string{}
	}
	keywords := def.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	return definitionDTO{
		ID:         def.ID.String(),
		Name:       def.Name,
		Kind:       string(def.Kind),
		SeedURLs:   seedURLs,
		Keywords:   keywords,
		MaxDepth:   def.MaxDepth,
		MaxDomains: def.MaxDomains,
		URLFilter:  def.URLFilter,
		CreatedAt:  def.CreatedAt,
	}
}

type companyDTO struct {
	ID            string    `json:"id"`
	CompanyKey    string    `json:"companyKey"`
	ATSProvider   string    `json:"atsProvider"`
	DisplayDomain string    `json:"displayDomain"`
	Name          string    `json:"name"`
	FirstSeen     time.Time `json:"firstSeen"`
	LastSeen      time.Time `json:"lastSeen"`
}

func toCompanyDTO(c *crawler.Company) companyDTO {
	return companyDTO{
		ID:            c.ID.String(),
		CompanyKey:    c.CompanyKey,
		ATSProvider:   c.ATSProvider,
		DisplayDomain: c.DisplayDomain,
		Name:          c.Name,
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

type listingDTO struct {
	URL         string   `json:"url"`
	Title       string   `json:"title"`
	Company     string   `json:"company"`
	Location    string   `json:"location"`
	Remote      bool     `json:"remote"`
	TechStack   []string `json:"techStack"`
	Description string   `json:"description"`
}

func toListingDTO(jl *crawler.JobListing) listingDTO {
	techStack := jl.TechStack
	if techStack == nil {
		techStack = []string{}
	}
	return listingDTO{
		URL:         jl.URL,
		Title:       jl.Title,
		Company:     jl.Company,
		Location:    jl.Location,
		Remote:      jl.Remote,
		TechStack:   techStack,
		Description: jl.Description,
	}
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
		slog.Error("api: error creating crawl definition", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create crawl")
		return
	}

	run, err := h.cfg.Runner.Start(r.Context(), def.ID)
	if err != nil {
		slog.Error("api: error starting crawl", "err", err)
		// The definition was committed but its run never started, leaving an
		// orphan. Best-effort roll it back so the fused endpoint stays atomic.
		// The cleanup uses a context detached from the request's cancellation
		// (a cancelled request context is itself a Start failure mode) and only
		// logs if the rollback fails.
		h.deleteOrphanDefinition(r.Context(), def.ID)
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
		dtos = append(dtos, toRunDTO(run))
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

	writeJSON(w, http.StatusOK, toRunDTO(run))
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

	// Frontier size is best-effort: a transient Redis error should not fail the
	// whole status poll, so it is logged and reported as 0. A run whose frontier
	// keys are already gone (terminal + cleaned up) legitimately reports 0 too.
	var frontierSize int64
	if h.cfg.FrontierSizer != nil {
		frontierSize, err = h.cfg.FrontierSizer(r.Context(), id)
		if err != nil {
			slog.Error("api: error reading frontier size", "err", err, "runId", id)
			frontierSize = 0
		}
	}

	writeJSON(w, http.StatusOK, runStatusDTO{
		PagesCrawled:  run.Counters.PagesCrawled,
		ListingsFound: run.Counters.ListingsFound,
		FrontierSize:  frontierSize,
	})
}

func (h *Handler) stopCrawl(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
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

// createDefinition persists a definition without starting a run. The definition
// becomes a re-runnable library entry started via POST /api/definitions/{id}/runs.
func (h *Handler) createDefinition(w http.ResponseWriter, r *http.Request) {
	def, msg := h.decodeDefinition(r)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}

	if err := h.cfg.Definitions.Create(r.Context(), def); err != nil {
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
		slog.Error("api: error starting run", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start run")
		return
	}

	writeJSON(w, http.StatusCreated, toRunDTO(run))
}

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
	switch kind {
	case crawler.CrawlKindKeyword:
		// Seeds come from the Catalog, not the request; keywords drive the
		// relevance filter, so an empty set would reject every page.
		if len(req.Keywords) == 0 {
			return nil, "keywords are required for a keyword crawl"
		}
	case crawler.CrawlKindDiscovery:
		if len(req.SeedURLs) == 0 {
			return nil, "seedUrls are required for a discovery crawl"
		}
	default:
		return nil, "unknown crawl kind"
	}

	def := &crawler.CrawlDefinition{
		Name:       req.Name,
		Kind:       kind,
		SeedURLs:   req.SeedURLs,
		Keywords:   req.Keywords,
		MaxDepth:   valueOr(req.MaxDepth, h.cfg.Defaults.MaxDepth),
		MaxDomains: valueOr(req.MaxDomains, h.cfg.Defaults.MaxDomains),
		URLFilter:  h.cfg.Defaults.URLFilter,
	}
	if req.URLFilter != nil {
		def.URLFilter = *req.URLFilter
	}

	return def, ""
}

// --- Catalog + listing handlers ---

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

// listListings returns the job listings extracted under a definition, filtered
// by the required ?definitionId= and an optional ?keyword= substring.
func (h *Handler) listListings(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("definitionId")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "definitionId is required")
		return
	}
	definitionID, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid definitionId")
		return
	}
	keyword := r.URL.Query().Get("keyword")

	listings, err := h.cfg.Listings.FindByDefinition(r.Context(), definitionID, keyword)
	if err != nil {
		slog.Error("api: error listing job listings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list listings")
		return
	}

	dtos := make([]listingDTO, 0, len(listings))
	for _, jl := range listings {
		dtos = append(dtos, toListingDTO(jl))
	}

	writeJSON(w, http.StatusOK, dtos)
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
