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
)

// fakeSavedSearchRepo is an in-memory SavedSearchRepository recording the
// aggregates it stored, so a test can assert the handler's normalization
// (trimmed keywords, uppercased countries, validated arrangements).
type fakeSavedSearchRepo struct {
	byID    map[uuid.UUID]*crawler.SavedSearch
	order   []uuid.UUID
	created *crawler.SavedSearch
	renamed struct {
		id   uuid.UUID
		name string
	}
	deleted uuid.UUID
}

func newFakeSavedSearchRepo() *fakeSavedSearchRepo {
	return &fakeSavedSearchRepo{byID: map[uuid.UUID]*crawler.SavedSearch{}}
}

func (f *fakeSavedSearchRepo) Create(ctx context.Context, ss *crawler.SavedSearch) error {
	ss.ID = uuid.New()
	ss.CreatedAt = time.Now().UTC()
	stored := *ss
	f.byID[ss.ID] = &stored
	f.order = append(f.order, ss.ID)
	f.created = &stored
	return nil
}

func (f *fakeSavedSearchRepo) Get(ctx context.Context, id uuid.UUID) (*crawler.SavedSearch, error) {
	ss, ok := f.byID[id]
	if !ok {
		return nil, crawler.ErrNotFound
	}
	copied := *ss
	return &copied, nil
}

func (f *fakeSavedSearchRepo) List(ctx context.Context) ([]*crawler.SavedSearch, error) {
	out := make([]*crawler.SavedSearch, 0, len(f.order))
	for _, id := range f.order {
		copied := *f.byID[id]
		out = append(out, &copied)
	}
	return out, nil
}

func (f *fakeSavedSearchRepo) Rename(ctx context.Context, id uuid.UUID, name string) error {
	ss, ok := f.byID[id]
	if !ok {
		return crawler.ErrNotFound
	}
	ss.Name = name
	f.renamed.id = id
	f.renamed.name = name
	return nil
}

func (f *fakeSavedSearchRepo) Delete(ctx context.Context, id uuid.UUID) error {
	f.deleted = id
	delete(f.byID, id)
	return nil
}

// fakeSearchRepo is a canned CorpusSearchRepository recording the last ListingQuery
// it received, so the results test can assert the handler projected the SavedSearch's
// facets (and the includeClosed toggle) into the query.
type fakeSearchRepo struct {
	listings   []*crawler.CorpusListing
	lastQ      crawler.ListingQuery
	countOpen  int
	countTotal int
}

func (f *fakeSearchRepo) SearchListings(ctx context.Context, q crawler.ListingQuery) ([]*crawler.CorpusListing, error) {
	f.lastQ = q
	if f.listings == nil {
		return []*crawler.CorpusListing{}, nil
	}
	return f.listings, nil
}

func (f *fakeSearchRepo) ListingCounts(ctx context.Context) (open int, total int, err error) {
	return f.countOpen, f.countTotal, nil
}

func TestCreateSavedSearch(t *testing.T) {
	t.Run("normalizes facets and returns 201", func(t *testing.T) {
		repo := newFakeSavedSearchRepo()
		srv := newHandler(api.Config{SavedSearches: repo})

		body, _ := json.Marshal(map[string]any{
			"name":             "  Remote Go  ",
			"keywords":         []string{" golang ", "", "backend"},
			"countries":        []string{"de", " at "},
			"workArrangements": []string{"Remote", "hybrid"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/saved-searches", bytes.NewReader(body)))

		if rec.Code != http.StatusCreated {
			t.Fatalf("status: got %d, want 201; body=%s", rec.Code, rec.Body)
		}
		if repo.created == nil {
			t.Fatal("expected a saved search to be created")
		}
		if repo.created.Name != "Remote Go" {
			t.Errorf("name should be trimmed: got %q", repo.created.Name)
		}
		if got := repo.created.Keywords; len(got) != 2 || got[0] != "golang" || got[1] != "backend" {
			t.Errorf("keywords: blanks should be dropped and trimmed, got %v", got)
		}
		if got := repo.created.Countries; len(got) != 2 || got[0] != "DE" || got[1] != "AT" {
			t.Errorf("countries: should be uppercased/trimmed, got %v", got)
		}
		if got := repo.created.WorkArrangements; len(got) != 2 ||
			got[0] != crawler.WorkArrangementRemote || got[1] != crawler.WorkArrangementHybrid {
			t.Errorf("work arrangements: got %v, want [remote hybrid]", got)
		}

		var dto map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dto["id"] == "" || dto["name"] != "Remote Go" {
			t.Errorf("response DTO: %v", dto)
		}
	})

	t.Run("missing name is 400", func(t *testing.T) {
		repo := newFakeSavedSearchRepo()
		srv := newHandler(api.Config{SavedSearches: repo})

		body, _ := json.Marshal(map[string]any{"name": "   "})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/saved-searches", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
		if repo.created != nil {
			t.Error("no saved search should be created without a name")
		}
	})

	t.Run("unknown work arrangement is 400", func(t *testing.T) {
		repo := newFakeSavedSearchRepo()
		srv := newHandler(api.Config{SavedSearches: repo})

		body, _ := json.Marshal(map[string]any{
			"name":             "bad",
			"workArrangements": []string{"remote", "telepathic"},
		})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/saved-searches", bytes.NewReader(body)))

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
		if repo.created != nil {
			t.Error("no saved search should be created with an unknown work arrangement")
		}
	})

	t.Run("non-alpha-2 country is 400", func(t *testing.T) {
		// "Germany" is a name, not the alpha-2 code the corpus stores, so it would
		// silently match nothing; reject it like an unknown arrangement.
		for _, bad := range []string{"Germany", "GER", "d"} {
			repo := newFakeSavedSearchRepo()
			srv := newHandler(api.Config{SavedSearches: repo})

			body, _ := json.Marshal(map[string]any{
				"name":      "bad",
				"countries": []string{"DE", bad},
			})
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/saved-searches", bytes.NewReader(body)))

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("country %q: status got %d, want 400; body=%s", bad, rec.Code, rec.Body)
			}
			if repo.created != nil {
				t.Errorf("country %q: no saved search should be created", bad)
			}
		}
	})
}

func TestListSavedSearches(t *testing.T) {
	repo := newFakeSavedSearchRepo()
	for _, n := range []string{"a", "b"} {
		if err := repo.Create(context.Background(), &crawler.SavedSearch{Name: n}); err != nil {
			t.Fatalf("seed create: %v", err)
		}
	}
	srv := newHandler(api.Config{SavedSearches: repo})

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/saved-searches", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	var got []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 saved searches, got %d", len(got))
	}
	// Facets must be arrays, never null.
	if _, ok := got[0]["keywords"].([]any); !ok {
		t.Errorf("keywords should serialize as an array, got %T", got[0]["keywords"])
	}
}

func TestRenameSavedSearch(t *testing.T) {
	seed := func(t *testing.T) (*fakeSavedSearchRepo, uuid.UUID) {
		t.Helper()
		repo := newFakeSavedSearchRepo()
		ss := &crawler.SavedSearch{Name: "old"}
		if err := repo.Create(context.Background(), ss); err != nil {
			t.Fatalf("seed create: %v", err)
		}
		return repo, ss.ID
	}

	patch := func(t *testing.T, srv http.Handler, id string, name string) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"name": name})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/api/saved-searches/"+id, bytes.NewReader(body)))
		return rec
	}

	t.Run("renames and returns the updated DTO", func(t *testing.T) {
		repo, id := seed(t)
		srv := newHandler(api.Config{SavedSearches: repo})

		rec := patch(t, srv, id.String(), "new name")
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if repo.renamed.id != id || repo.renamed.name != "new name" {
			t.Errorf("repo not renamed as expected: %+v", repo.renamed)
		}
		var dto map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if dto["name"] != "new name" {
			t.Errorf("response name: got %v, want %q", dto["name"], "new name")
		}
	})

	t.Run("blank name is 400", func(t *testing.T) {
		repo, id := seed(t)
		srv := newHandler(api.Config{SavedSearches: repo})
		if rec := patch(t, srv, id.String(), "  "); rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("unknown id is 404", func(t *testing.T) {
		repo := newFakeSavedSearchRepo()
		srv := newHandler(api.Config{SavedSearches: repo})
		if rec := patch(t, srv, uuid.New().String(), "x"); rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("bad uuid is 400", func(t *testing.T) {
		srv := newHandler(api.Config{})
		if rec := patch(t, srv, "not-a-uuid", "x"); rec.Code != http.StatusBadRequest {
			t.Fatalf("status: got %d, want 400; body=%s", rec.Code, rec.Body)
		}
	})
}

func TestDeleteSavedSearch(t *testing.T) {
	del := func(t *testing.T, srv http.Handler, id string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/saved-searches/"+id, nil))
		return rec
	}

	t.Run("existing search deletes with 204", func(t *testing.T) {
		repo := newFakeSavedSearchRepo()
		ss := &crawler.SavedSearch{Name: "gone"}
		if err := repo.Create(context.Background(), ss); err != nil {
			t.Fatalf("seed create: %v", err)
		}
		srv := newHandler(api.Config{SavedSearches: repo})

		rec := del(t, srv, ss.ID.String())
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status: got %d, want 204; body=%s", rec.Code, rec.Body)
		}
		if repo.deleted != ss.ID {
			t.Errorf("repo deleted %v, want %v", repo.deleted, ss.ID)
		}
	})

	t.Run("unknown id is still 204 (idempotent)", func(t *testing.T) {
		repo := newFakeSavedSearchRepo()
		srv := newHandler(api.Config{SavedSearches: repo})
		if rec := del(t, srv, uuid.New().String()); rec.Code != http.StatusNoContent {
			t.Fatalf("status: got %d, want 204; body=%s", rec.Code, rec.Body)
		}
	})
}

func TestSavedSearchResults(t *testing.T) {
	closedAt := time.Now().UTC()
	canned := []*crawler.CorpusListing{
		{
			ID:              uuid.New(),
			Title:           "Platform Engineer",
			Description:     "run the platform",
			Company:         "acme",
			Department:      "Platform",
			Location:        "Berlin, DE",
			Country:         "DE",
			WorkArrangement: crawler.WorkArrangementRemote,
			URL:             "https://acme.example/jobs/1",
			Source:          crawler.SourceLaneATS,
			ClosedAt:        &closedAt,
		},
	}

	seed := func(t *testing.T) (*fakeSavedSearchRepo, *fakeSearchRepo, uuid.UUID) {
		t.Helper()
		repo := newFakeSavedSearchRepo()
		ss := &crawler.SavedSearch{
			Name:             "search",
			Keywords:         []string{"platform"},
			Countries:        []string{"DE"},
			WorkArrangements: []crawler.WorkArrangement{crawler.WorkArrangementRemote},
		}
		if err := repo.Create(context.Background(), ss); err != nil {
			t.Fatalf("seed create: %v", err)
		}
		search := &fakeSearchRepo{listings: canned}
		return repo, search, ss.ID
	}

	t.Run("unknown id is 404", func(t *testing.T) {
		srv := newHandler(api.Config{SavedSearches: newFakeSavedSearchRepo(), Search: &fakeSearchRepo{}})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/saved-searches/"+uuid.New().String()+"/results", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status: got %d, want 404; body=%s", rec.Code, rec.Body)
		}
	})

	t.Run("open-only by default, projects the SavedSearch facets", func(t *testing.T) {
		repo, search, id := seed(t)
		srv := newHandler(api.Config{SavedSearches: repo, Search: search})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/saved-searches/"+id.String()+"/results", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		// The query the repo received must be the SavedSearch's projection: its
		// facets, open-only (IncludeClosed false) by default.
		if search.lastQ.IncludeClosed {
			t.Error("default results query must be open-only (IncludeClosed false)")
		}
		if len(search.lastQ.Keywords) != 1 || search.lastQ.Keywords[0] != "platform" {
			t.Errorf("keywords not projected into query: %v", search.lastQ.Keywords)
		}
		if len(search.lastQ.Countries) != 1 || search.lastQ.Countries[0] != "DE" {
			t.Errorf("countries not projected into query: %v", search.lastQ.Countries)
		}
		if len(search.lastQ.WorkArrangements) != 1 || search.lastQ.WorkArrangements[0] != crawler.WorkArrangementRemote {
			t.Errorf("work arrangements not projected into query: %v", search.lastQ.WorkArrangements)
		}

		var listings []map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &listings); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(listings) != 1 {
			t.Fatalf("want 1 listing, got %d", len(listings))
		}
		// department is wired end-to-end through the listing DTO.
		if listings[0]["department"] != "Platform" {
			t.Errorf("department not surfaced in listing DTO: %v", listings[0]["department"])
		}
		if listings[0]["workArrangement"] != "remote" {
			t.Errorf("workArrangement: got %v, want remote", listings[0]["workArrangement"])
		}
		if listings[0]["source"] != "ats" {
			t.Errorf("source: got %v, want ats", listings[0]["source"])
		}
	})

	t.Run("includeClosed=true flips the query", func(t *testing.T) {
		repo, search, id := seed(t)
		srv := newHandler(api.Config{SavedSearches: repo, Search: search})

		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/saved-searches/"+id.String()+"/results?includeClosed=true", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
		}
		if !search.lastQ.IncludeClosed {
			t.Error("includeClosed=true should set ListingQuery.IncludeClosed")
		}
	})
}
