package catalogdoctor_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
)

// fakeStore is an in-memory catalogdoctor.Store that records an ordered op log
// plus per-op detail, and assigns a fresh id on every UpsertCompany (writing it
// back into the Company) so a re-attribution can read the generated id.
type fakeStore struct {
	ops          []string
	upserted     map[string]uuid.UUID    // company key -> assigned id
	reattr       map[uuid.UUID]uuid.UUID // page id -> new company id
	deletedPages map[uuid.UUID]bool
	deletedCos   map[uuid.UUID]bool
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		upserted:     map[string]uuid.UUID{},
		reattr:       map[uuid.UUID]uuid.UUID{},
		deletedPages: map[uuid.UUID]bool{},
		deletedCos:   map[uuid.UUID]bool{},
	}
}

var _ catalogdoctor.Store = (*fakeStore)(nil)

func (s *fakeStore) UpsertCompany(_ context.Context, c *crawler.Company) error {
	id := uuid.New()
	c.ID = id
	s.upserted[c.CompanyKey] = id
	s.ops = append(s.ops, "upsert:"+c.CompanyKey)
	return nil
}

func (s *fakeStore) DeleteCompany(_ context.Context, id uuid.UUID) error {
	s.deletedCos[id] = true
	s.ops = append(s.ops, "delco:"+id.String())
	return nil
}

func (s *fakeStore) DeleteCareerPage(_ context.Context, id uuid.UUID) error {
	s.deletedPages[id] = true
	s.ops = append(s.ops, "delpage:"+id.String())
	return nil
}

func (s *fakeStore) ReattributeCareerPage(_ context.Context, id, companyID uuid.UUID) error {
	s.reattr[id] = companyID
	s.ops = append(s.ops, "reattr:"+id.String())
	return nil
}

func countOps(ops []string, prefix string) int {
	n := 0
	for _, op := range ops {
		if strings.HasPrefix(op, prefix) {
			n++
		}
	}
	return n
}

// lastIndex returns the index of the last op with prefix, or -1.
func lastIndex(ops []string, prefix string) int {
	last := -1
	for i, op := range ops {
		if strings.HasPrefix(op, prefix) {
			last = i
		}
	}
	return last
}

// firstIndex returns the index of the first op with prefix, or len(ops).
func firstIndex(ops []string, prefix string) int {
	for i, op := range ops {
		if strings.HasPrefix(op, prefix) {
			return i
		}
	}
	return len(ops)
}

func TestApply(t *testing.T) {
	t.Run("executes a full plan: materialise, re-attribute, delete, sweep", func(t *testing.T) {
		store := newFakeStore()

		oldOwner := &crawler.Company{ID: uuid.New(), CompanyKey: "join.com"}
		newTarget := &crawler.Company{CompanyKey: "join:zara", ATSProvider: "join", Name: "zara"} // ID zero: must be created
		reattributed := newPage(oldOwner.ID, "https://join.com/companies/zara")
		posting := newPage(oldOwner.ID, "https://join.com/companies/zara/16405887-role")
		mergeLoser := newPage(uuid.New(), "http://acme.com/careers")

		result := catalogdoctor.Result{
			Pages: []catalogdoctor.PageDisposition{
				{Page: reattributed, Action: catalogdoctor.Reattribute, Target: newTarget},
				{Page: posting, Action: catalogdoctor.Delete},
				{Page: mergeLoser, Action: catalogdoctor.Merge, MergeInto: uuid.New()},
			},
			Orphans: []*crawler.Company{oldOwner},
		}

		if err := catalogdoctor.Apply(t.Context(), store, result); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		newID, ok := store.upserted["join:zara"]
		if !ok {
			t.Fatalf("re-attribution target join:zara was not upserted")
		}
		if store.reattr[reattributed.ID] != newID {
			t.Errorf("re-attribute used company %s, want the generated id %s", store.reattr[reattributed.ID], newID)
		}
		if !store.deletedPages[posting.ID] {
			t.Errorf("posting page %s should be deleted", posting.ID)
		}
		if !store.deletedPages[mergeLoser.ID] {
			t.Errorf("merge-loser page %s should be deleted", mergeLoser.ID)
		}
		if !store.deletedCos[oldOwner.ID] {
			t.Errorf("orphan company %s should be deleted", oldOwner.ID)
		}
	})

	t.Run("deletes companies only after their pages move or leave (FK-safe order)", func(t *testing.T) {
		store := newFakeStore()

		oldOwner := &crawler.Company{ID: uuid.New(), CompanyKey: "join.com"}
		newTarget := &crawler.Company{CompanyKey: "join:zara", ATSProvider: "join", Name: "zara"}
		reattributed := newPage(oldOwner.ID, "https://join.com/companies/zara")
		posting := newPage(oldOwner.ID, "https://join.com/companies/zara/16405887-role")

		result := catalogdoctor.Result{
			Pages: []catalogdoctor.PageDisposition{
				{Page: reattributed, Action: catalogdoctor.Reattribute, Target: newTarget},
				{Page: posting, Action: catalogdoctor.Delete},
			},
			Orphans: []*crawler.Company{oldOwner},
		}

		if err := catalogdoctor.Apply(t.Context(), store, result); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		firstDelco := firstIndex(store.ops, "delco:")
		if got := lastIndex(store.ops, "reattr:"); got >= firstDelco {
			t.Errorf("reattr op at %d must precede first delco at %d (ops=%v)", got, firstDelco, store.ops)
		}
		if got := lastIndex(store.ops, "delpage:"); got >= firstDelco {
			t.Errorf("delpage op at %d must precede first delco at %d (ops=%v)", got, firstDelco, store.ops)
		}
	})

	t.Run("upserts a shared new target once and reuses its id", func(t *testing.T) {
		store := newFakeStore()

		target1 := &crawler.Company{CompanyKey: "join:zara", ATSProvider: "join", Name: "zara"}
		target2 := &crawler.Company{CompanyKey: "join:zara", ATSProvider: "join", Name: "zara"} // distinct pointer, same key
		pageA := newPage(uuid.New(), "https://join.com/companies/zara")
		pageB := newPage(uuid.New(), "https://join.eu/companies/zara")

		result := catalogdoctor.Result{
			Pages: []catalogdoctor.PageDisposition{
				{Page: pageA, Action: catalogdoctor.Reattribute, Target: target1},
				{Page: pageB, Action: catalogdoctor.Reattribute, Target: target2},
			},
		}

		if err := catalogdoctor.Apply(t.Context(), store, result); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if n := countOps(store.ops, "upsert:join:zara"); n != 1 {
			t.Errorf("upsert count = %d, want 1 (shared target created once)", n)
		}
		if store.reattr[pageA.ID] != store.reattr[pageB.ID] {
			t.Errorf("both pages should re-attribute to the same id, got %s and %s",
				store.reattr[pageA.ID], store.reattr[pageB.ID])
		}
		if store.reattr[pageA.ID] == uuid.Nil {
			t.Errorf("re-attribute id must be the generated target id, got nil")
		}
	})

	t.Run("an all-keep result with no orphans issues zero mutations", func(t *testing.T) {
		store := newFakeStore()

		result := catalogdoctor.Result{
			Pages: []catalogdoctor.PageDisposition{
				{Page: newPage(uuid.New(), "https://acme.com/careers"), Action: catalogdoctor.Keep},
				{Page: newPage(uuid.New(), "https://job-boards.greenhouse.io/acme"), Action: catalogdoctor.Keep},
			},
		}

		if err := catalogdoctor.Apply(t.Context(), store, result); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if len(store.ops) != 0 {
			t.Errorf("all-keep result should issue no ops, got %v", store.ops)
		}
	})
}
