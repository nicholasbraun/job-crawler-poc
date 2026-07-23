package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// savedSearchDTO is a SavedSearch as the dashboard reads it (ADR-0037): its stored
// facets plus identity/creation time. The facet arrays are always non-nil ([] not
// null), matching the API's uniform array handling.
type savedSearchDTO struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Keywords         []string  `json:"keywords"`
	Countries        []string  `json:"countries"`
	WorkArrangements []string  `json:"workArrangements"`
	CreatedAt        time.Time `json:"createdAt"`
}

// savedSearchRequest is the create body: a name plus the three query facets. The
// facets are normalized server-side (trimmed, uppercased countries, validated
// arrangements) before persisting.
type savedSearchRequest struct {
	Name             string   `json:"name"`
	Keywords         []string `json:"keywords"`
	Countries        []string `json:"countries"`
	WorkArrangements []string `json:"workArrangements"`
}

// renameSavedSearchRequest is the PATCH body: the only mutable field in v1.
type renameSavedSearchRequest struct {
	Name string `json:"name"`
}

// listingDTO is the full-detail Corpus listing a SavedSearch panel renders
// (ADR-0037): every corpus field a reader needs, with closedAt nil while Open.
type listingDTO struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Company         string     `json:"company"`
	Department      string     `json:"department"`
	Location        string     `json:"location"`
	Country         string     `json:"country"`
	WorkArrangement string     `json:"workArrangement"`
	URL             string     `json:"url"`
	Source          string     `json:"source"`
	FirstSeen       time.Time  `json:"firstSeen"`
	LastSeen        time.Time  `json:"lastSeen"`
	ClosedAt        *time.Time `json:"closedAt"`
}

// toSavedSearchDTO projects a SavedSearch for the wire, coalescing nil facet slices
// to [] and mapping WorkArrangement values to their underlying strings.
func toSavedSearchDTO(ss *crawler.SavedSearch) savedSearchDTO {
	arrangements := make([]string, 0, len(ss.WorkArrangements))
	for _, w := range ss.WorkArrangements {
		arrangements = append(arrangements, string(w))
	}
	keywords := ss.Keywords
	if keywords == nil {
		keywords = []string{}
	}
	countries := ss.Countries
	if countries == nil {
		countries = []string{}
	}
	return savedSearchDTO{
		ID:               ss.ID.String(),
		Name:             ss.Name,
		Keywords:         keywords,
		Countries:        countries,
		WorkArrangements: arrangements,
		CreatedAt:        ss.CreatedAt,
	}
}

func toListingDTO(cl *crawler.CorpusListing) listingDTO {
	return listingDTO{
		ID:              cl.ID.String(),
		Title:           cl.Title,
		Description:     cl.Description,
		Company:         cl.Company,
		Department:      cl.Department,
		Location:        cl.Location,
		Country:         cl.Country,
		WorkArrangement: string(cl.WorkArrangement),
		URL:             cl.URL,
		Source:          string(cl.Source),
		FirstSeen:       cl.FirstSeen,
		LastSeen:        cl.LastSeen,
		ClosedAt:        cl.ClosedAt,
	}
}

func (h *Handler) listSavedSearches(w http.ResponseWriter, r *http.Request) {
	searches, err := h.cfg.SavedSearches.List(r.Context())
	if err != nil {
		slog.Error("api: error listing saved searches", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list saved searches")
		return
	}

	dtos := make([]savedSearchDTO, 0, len(searches))
	for _, ss := range searches {
		dtos = append(dtos, toSavedSearchDTO(ss))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// createSavedSearch validates and persists a new SavedSearch: the name is required
// (trimmed), keywords are trimmed with blanks dropped, countries are uppercased, and
// each work-arrangement string must be one of remote/onsite/hybrid (an unknown value
// is a 400 rather than a facet that silently matches nothing).
func (h *Handler) createSavedSearch(w http.ResponseWriter, r *http.Request) {
	var req savedSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	arrangements, ok := parseWorkArrangements(req.WorkArrangements)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown work arrangement")
		return
	}

	ss := &crawler.SavedSearch{
		Name:             name,
		Keywords:         normalizeKeywords(req.Keywords),
		Countries:        normalizeCountries(req.Countries),
		WorkArrangements: arrangements,
	}
	if err := h.cfg.SavedSearches.Create(r.Context(), ss); err != nil {
		slog.Error("api: error creating saved search", "err", err)
		writeError(w, http.StatusInternalServerError, "could not create saved search")
		return
	}

	writeJSON(w, http.StatusCreated, toSavedSearchDTO(ss))
}

// renameSavedSearch updates a SavedSearch's name and returns the updated DTO. An
// unknown id is a 404 (Rename reports ErrNotFound); a blank name is a 400.
func (h *Handler) renameSavedSearch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid saved search id")
		return
	}

	var req renameSavedSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	if err := h.cfg.SavedSearches.Rename(r.Context(), id, name); err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "saved search not found")
			return
		}
		slog.Error("api: error renaming saved search", "err", err)
		writeError(w, http.StatusInternalServerError, "could not rename saved search")
		return
	}

	// Re-read so the response carries the persisted row (name + unchanged facets),
	// letting the client reflect the rename without a refetch.
	ss, err := h.cfg.SavedSearches.Get(r.Context(), id)
	if err != nil {
		slog.Error("api: error reloading renamed saved search", "err", err)
		writeError(w, http.StatusInternalServerError, "could not rename saved search")
		return
	}
	writeJSON(w, http.StatusOK, toSavedSearchDTO(ss))
}

// deleteSavedSearch removes a SavedSearch. Delete is idempotent, so an unknown id
// still returns 204 (deleting an already-absent search is not an error).
func (h *Handler) deleteSavedSearch(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid saved search id")
		return
	}

	if err := h.cfg.SavedSearches.Delete(r.Context(), id); err != nil {
		slog.Error("api: error deleting saved search", "err", err)
		writeError(w, http.StatusInternalServerError, "could not delete saved search")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// savedSearchResults runs a SavedSearch against the Corpus and returns the matching
// listings in full detail (ADR-0037). The stored facets project to a ListingQuery via
// SavedSearch.Query (open-only, relevance-ranked, default page). ?includeClosed=true
// surfaces Closed listings too. An unknown id is a 404.
func (h *Handler) savedSearchResults(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid saved search id")
		return
	}

	ss, err := h.cfg.SavedSearches.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, crawler.ErrNotFound) {
			writeError(w, http.StatusNotFound, "saved search not found")
			return
		}
		slog.Error("api: error getting saved search", "err", err)
		writeError(w, http.StatusInternalServerError, "could not get saved search")
		return
	}

	q := ss.Query()
	if r.URL.Query().Get("includeClosed") == "true" {
		q.IncludeClosed = true
	}

	listings, err := h.cfg.Search.SearchListings(r.Context(), q)
	if err != nil {
		slog.Error("api: error searching saved search results", "err", err)
		writeError(w, http.StatusInternalServerError, "could not run saved search")
		return
	}

	dtos := make([]listingDTO, 0, len(listings))
	for _, cl := range listings {
		dtos = append(dtos, toListingDTO(cl))
	}

	writeJSON(w, http.StatusOK, dtos)
}

// normalizeKeywords trims each keyword and drops blanks. Never nil.
func normalizeKeywords(raw []string) []string {
	out := []string{}
	for _, k := range raw {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// normalizeCountries trims, drops blanks, and uppercases each country to the stored
// ISO-3166-1 alpha-2 form. Never nil.
func normalizeCountries(raw []string) []string {
	out := []string{}
	for _, c := range raw {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, strings.ToUpper(c))
		}
	}
	return out
}

// parseWorkArrangements validates each string against the filterable arrangements
// (remote/onsite/hybrid), returning ok=false on the first unknown value. Blanks are
// dropped. Unspecified is intentionally not accepted as a filter facet — a search
// filters toward a positively-stated mode, not the honest default (ADR-0030).
func parseWorkArrangements(raw []string) ([]crawler.WorkArrangement, bool) {
	out := []crawler.WorkArrangement{}
	for _, s := range raw {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		switch crawler.WorkArrangement(strings.ToLower(s)) {
		case crawler.WorkArrangementRemote, crawler.WorkArrangementOnsite, crawler.WorkArrangementHybrid:
			out = append(out, crawler.WorkArrangement(strings.ToLower(s)))
		default:
			return nil, false
		}
	}
	return out, true
}
