package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
)

func TestRecentListings(t *testing.T) {
	t.Run("returns a SortFound, open-only query with the default limit", func(t *testing.T) {
		search := &fakeSearchRepo{listings: []*crawler.CorpusListing{{Title: "Backend Engineer"}}}
		srv := newHandler(api.Config{Search: search})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings/recent", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if search.lastQ.Sort != crawler.SortFound {
			t.Errorf("sort: got %q, want %q", search.lastQ.Sort, crawler.SortFound)
		}
		if search.lastQ.IncludeClosed {
			t.Error("recent feed must be open-only")
		}
		if search.lastQ.Limit != 12 {
			t.Errorf("default limit: got %d, want 12", search.lastQ.Limit)
		}

		var dtos []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &dtos); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(dtos) != 1 || dtos[0]["title"] != "Backend Engineer" {
			t.Errorf("body: %v", dtos)
		}
	})

	t.Run("clamps ?limit to the max", func(t *testing.T) {
		search := &fakeSearchRepo{}
		srv := newHandler(api.Config{Search: search})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings/recent?limit=999", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if search.lastQ.Limit != 50 {
			t.Errorf("limit should clamp to 50, got %d", search.lastQ.Limit)
		}
	})

	t.Run("rejects a non-positive ?limit with 400", func(t *testing.T) {
		for _, bad := range []string{"0", "-3", "abc"} {
			search := &fakeSearchRepo{}
			srv := newHandler(api.Config{Search: search})

			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings/recent?limit="+bad, nil))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("limit=%q: status got %d, want 400", bad, rec.Code)
			}
		}
	})
}

func TestListingStats(t *testing.T) {
	search := &fakeSearchRepo{countOpen: 7137, countTotal: 7149}
	srv := newHandler(api.Config{Search: search})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/listings/stats", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got struct {
		Open   int `json:"open"`
		Closed int `json:"closed"`
		Total  int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Open != 7137 || got.Total != 7149 || got.Closed != 12 {
		t.Errorf("stats: got %+v, want {open:7137 closed:12 total:7149}", got)
	}
}
