package api

import (
	"log/slog"
	"net/http"
	"strconv"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

const (
	// defaultRecentListingsLimit is the recent-feed page size when ?limit is absent.
	defaultRecentListingsLimit = 12
	// maxRecentListingsLimit caps ?limit so the live feed can never scan a large page.
	maxRecentListingsLimit = 50
)

// recentListings returns the most recently discovered Open Corpus listings
// (first-seen descending), backing the Overview's live collection feed. It is a
// keywordless SortFound query over the same CorpusSearchRepository the SavedSearch
// panels use. ?limit caps the page (default defaultRecentListingsLimit, clamped to
// maxRecentListingsLimit).
func (h *Handler) recentListings(w http.ResponseWriter, r *http.Request) {
	limit := defaultRecentListingsLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		if n > maxRecentListingsLimit {
			n = maxRecentListingsLimit
		}
		limit = n
	}

	listings, err := h.cfg.Search.SearchListings(r.Context(), crawler.ListingQuery{
		Sort:  crawler.SortFound,
		Limit: limit,
	})
	if err != nil {
		slog.Error("api: error listing recent listings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not list recent listings")
		return
	}

	dtos := make([]listingDTO, 0, len(listings))
	for _, cl := range listings {
		dtos = append(dtos, toListingDTO(cl))
	}
	writeJSON(w, http.StatusOK, dtos)
}

// listingStatsDTO is the corpus-size headline: distinct listing row counts. This is
// the true corpus size, not a Collection run's ListingsFound counter (which counts
// save operations, so a re-saved/reopened listing inflates it far past the row count).
type listingStatsDTO struct {
	Open   int `json:"open"`
	Closed int `json:"closed"`
	Total  int `json:"total"`
}

// listingStats returns the distinct open/closed/total Corpus listing counts, so the
// Overview shows the real corpus size instead of the run's save-event counter.
func (h *Handler) listingStats(w http.ResponseWriter, r *http.Request) {
	open, total, err := h.cfg.Search.ListingCounts(r.Context())
	if err != nil {
		slog.Error("api: error counting corpus listings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not count corpus listings")
		return
	}
	writeJSON(w, http.StatusOK, listingStatsDTO{Open: open, Closed: total - open, Total: total})
}
