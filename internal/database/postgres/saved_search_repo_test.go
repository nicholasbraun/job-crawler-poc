package postgres_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

// TestSavedSearchCreateGet asserts a round-trip preserves every facet — populated
// and empty — and that the DB stamps ID and CreatedAt back onto the aggregate.
func TestSavedSearchCreateGet(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewSavedSearchRepository(pool)

	t.Run("all facets round-trip", func(t *testing.T) {
		ss := &crawler.SavedSearch{
			Name:      "Remote Go in DE",
			Keywords:  []string{"golang", "backend"},
			Countries: []string{"DE", "AT"},
			WorkArrangements: []crawler.WorkArrangement{
				crawler.WorkArrangementRemote, crawler.WorkArrangementHybrid,
			},
		}
		if err := repo.Create(t.Context(), ss); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if ss.ID == uuid.Nil {
			t.Error("Create should stamp a generated ID")
		}
		if ss.CreatedAt.IsZero() {
			t.Error("Create should stamp CreatedAt")
		}

		got, err := repo.Get(t.Context(), ss.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != ss.Name {
			t.Errorf("name: got %q, want %q", got.Name, ss.Name)
		}
		if !equalStrings(got.Keywords, ss.Keywords) {
			t.Errorf("keywords: got %v, want %v", got.Keywords, ss.Keywords)
		}
		if !equalStrings(got.Countries, ss.Countries) {
			t.Errorf("countries: got %v, want %v", got.Countries, ss.Countries)
		}
		if len(got.WorkArrangements) != 2 ||
			got.WorkArrangements[0] != crawler.WorkArrangementRemote ||
			got.WorkArrangements[1] != crawler.WorkArrangementHybrid {
			t.Errorf("work arrangements: got %v, want [remote hybrid]", got.WorkArrangements)
		}
	})

	t.Run("empty facets round-trip as empty slices", func(t *testing.T) {
		ss := &crawler.SavedSearch{Name: "anything"}
		if err := repo.Create(t.Context(), ss); err != nil {
			t.Fatalf("Create: %v", err)
		}
		got, err := repo.Get(t.Context(), ss.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(got.Keywords) != 0 || len(got.Countries) != 0 || len(got.WorkArrangements) != 0 {
			t.Errorf("empty facets should round-trip empty: %+v", got)
		}
	})

	t.Run("unknown id is ErrNotFound", func(t *testing.T) {
		_, err := repo.Get(t.Context(), uuid.New())
		if !errors.Is(err, crawler.ErrNotFound) {
			t.Errorf("Get(unknown): got %v, want ErrNotFound", err)
		}
	})
}

// TestSavedSearchListOrder asserts List returns oldest-first and never nil.
func TestSavedSearchListOrder(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewSavedSearchRepository(pool)

	t.Run("empty table yields a non-nil empty slice", func(t *testing.T) {
		got, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if got == nil {
			t.Fatal("List must never return nil")
		}
		if len(got) != 0 {
			t.Errorf("want 0 saved searches, got %d", len(got))
		}
	})

	t.Run("oldest first", func(t *testing.T) {
		names := []string{"first", "second", "third"}
		for _, n := range names {
			if err := repo.Create(t.Context(), &crawler.SavedSearch{Name: n}); err != nil {
				t.Fatalf("Create %q: %v", n, err)
			}
		}
		got, err := repo.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("want 3, got %d", len(got))
		}
		for i, n := range names {
			if got[i].Name != n {
				t.Errorf("order[%d]: got %q, want %q (full: %v)", i, got[i].Name, n, listNames(got))
			}
		}
	})
}

// TestSavedSearchRename asserts the success path changes the name and an unknown id
// returns ErrNotFound.
func TestSavedSearchRename(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewSavedSearchRepository(pool)

	ss := &crawler.SavedSearch{Name: "before"}
	if err := repo.Create(t.Context(), ss); err != nil {
		t.Fatalf("Create: %v", err)
	}

	t.Run("renames an existing search", func(t *testing.T) {
		if err := repo.Rename(t.Context(), ss.ID, "after"); err != nil {
			t.Fatalf("Rename: %v", err)
		}
		got, err := repo.Get(t.Context(), ss.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Name != "after" {
			t.Errorf("name: got %q, want %q", got.Name, "after")
		}
	})

	t.Run("unknown id is ErrNotFound", func(t *testing.T) {
		if err := repo.Rename(t.Context(), uuid.New(), "x"); !errors.Is(err, crawler.ErrNotFound) {
			t.Errorf("Rename(unknown): got %v, want ErrNotFound", err)
		}
	})
}

// TestSavedSearchDeleteIdempotent asserts a double-delete is not an error and the
// row is gone afterward.
func TestSavedSearchDeleteIdempotent(t *testing.T) {
	pool := newTestPool(t)
	repo := postgres.NewSavedSearchRepository(pool)

	ss := &crawler.SavedSearch{Name: "doomed"}
	if err := repo.Create(t.Context(), ss); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.Delete(t.Context(), ss.ID); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	if err := repo.Delete(t.Context(), ss.ID); err != nil {
		t.Fatalf("second Delete (idempotent) should not error: %v", err)
	}
	if _, err := repo.Get(t.Context(), ss.ID); !errors.Is(err, crawler.ErrNotFound) {
		t.Errorf("Get after Delete: got %v, want ErrNotFound", err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func listNames(searches []*crawler.SavedSearch) []string {
	out := make([]string, len(searches))
	for i, s := range searches {
		out[i] = s.Name
	}
	return out
}
