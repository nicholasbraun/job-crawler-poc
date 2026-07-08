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
}

// Defaults fill in the fields a create request omits, so a minimal
// {name, seedUrls} request behaves like today's CLI. Sourced from config.json.
type Defaults struct {
	MaxDepth   int
	MaxDomains int
	URLFilter  crawler.URLFilterConfig
}

type Handler struct {
	runner   Runner
	runs     crawler.CrawlRunRepository
	defs     crawler.CrawlDefinitionRepository
	defaults Defaults
}

func New(r Runner, runs crawler.CrawlRunRepository, defs crawler.CrawlDefinitionRepository, defaults Defaults) *Handler {
	return &Handler{runner: r, runs: runs, defs: defs, defaults: defaults}
}

// Routes returns the API mux. All routes are under /api.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/crawls", h.createCrawl)
	mux.HandleFunc("GET /api/crawls", h.listCrawls)
	mux.HandleFunc("GET /api/crawls/{id}", h.getCrawl)
	mux.HandleFunc("POST /api/crawls/{id}/stop", h.stopCrawl)
	return mux
}

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

func (h *Handler) createCrawl(w http.ResponseWriter, r *http.Request) {
	var req createCrawlRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
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
			writeError(w, http.StatusBadRequest, "keywords are required for a keyword crawl")
			return
		}
	case crawler.CrawlKindDiscovery:
		if len(req.SeedURLs) == 0 {
			writeError(w, http.StatusBadRequest, "seedUrls are required for a discovery crawl")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "unknown crawl kind")
		return
	}

	def := &crawler.CrawlDefinition{
		Name:       req.Name,
		Kind:       kind,
		SeedURLs:   req.SeedURLs,
		Keywords:   req.Keywords,
		MaxDepth:   valueOr(req.MaxDepth, h.defaults.MaxDepth),
		MaxDomains: valueOr(req.MaxDomains, h.defaults.MaxDomains),
		URLFilter:  h.defaults.URLFilter,
	}
	if req.URLFilter != nil {
		def.URLFilter = *req.URLFilter
	}

	if err := h.defs.Create(r.Context(), def); err != nil {
		slog.Error("api: error creating crawl definition", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create crawl")
		return
	}

	run, err := h.runner.Start(r.Context(), def.ID)
	if err != nil {
		slog.Error("api: error starting crawl", "err", err)
		writeError(w, http.StatusInternalServerError, "could not start crawl")
		return
	}

	writeJSON(w, http.StatusCreated, toRunDTO(run))
}

func (h *Handler) listCrawls(w http.ResponseWriter, r *http.Request) {
	runs, err := h.runs.List(r.Context())
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

	run, err := h.runs.Get(r.Context(), id)
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

func (h *Handler) stopCrawl(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid crawl id")
		return
	}

	if err := h.runner.Stop(r.Context(), id); err != nil {
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
