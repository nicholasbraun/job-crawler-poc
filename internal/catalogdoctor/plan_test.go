package catalogdoctor_test

import (
	"testing"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
)

// newCompany builds a Company with a fixed id and key; the remaining fields are
// irrelevant to Plan (URL-only logic).
func newCompany(id uuid.UUID, key string) *crawler.Company {
	return &crawler.Company{ID: id, CompanyKey: key}
}

// newPage builds a Career Page owned by companyID with a fresh id.
func newPage(companyID uuid.UUID, url string) *crawler.CareerPage {
	return &crawler.CareerPage{ID: uuid.New(), CompanyID: companyID, URL: url}
}

// byURL indexes a Result's page dispositions by their Career Page URL.
func byURL(t *testing.T, result catalogdoctor.Result) map[string]catalogdoctor.PageDisposition {
	t.Helper()
	m := map[string]catalogdoctor.PageDisposition{}
	for _, d := range result.Pages {
		m[d.Page.URL] = d
	}
	return m
}

// orphanKeys returns the CompanyKeys of a Result's orphaned Companies.
func orphanKeys(result catalogdoctor.Result) []string {
	keys := []string{}
	for _, c := range result.Orphans {
		keys = append(keys, c.CompanyKey)
	}
	return keys
}

func contains(keys []string, want string) bool {
	for _, k := range keys {
		if k == want {
			return true
		}
	}
	return false
}

func TestPlan(t *testing.T) {
	t.Run("single posting is deleted", func(t *testing.T) {
		id := uuid.New()
		co := newCompany(id, "sybillatechnologies.com")
		page := newPage(id, "https://sybillatechnologies.com/career/senior-role")

		result := catalogdoctor.Plan([]*crawler.CareerPage{page}, []*crawler.Company{co})

		got := byURL(t, result)[page.URL]
		if got.Action != catalogdoctor.Delete {
			t.Errorf("action = %s, want delete", got.Action)
		}
	})

	t.Run("terminal-hub deep path is kept", func(t *testing.T) {
		id := uuid.New()
		co := newCompany(id, "datarobot.com")
		page := newPage(id, "https://datarobot.com/careers/open-positions")

		result := catalogdoctor.Plan([]*crawler.CareerPage{page}, []*crawler.Company{co})

		got := byURL(t, result)[page.URL]
		if got.Action != catalogdoctor.Keep {
			t.Errorf("action = %s, want keep (terminal-hub deep path is a real hub)", got.Action)
		}
		if len(result.Orphans) != 0 {
			t.Errorf("orphans = %v, want none", orphanKeys(result))
		}
	})

	t.Run("aggregator row is deleted and its company orphaned", func(t *testing.T) {
		id := uuid.New()
		co := newCompany(id, "eu-startups.com")
		page := newPage(id, "https://eu-startups.com/directory")

		result := catalogdoctor.Plan([]*crawler.CareerPage{page}, []*crawler.Company{co})

		got := byURL(t, result)[page.URL]
		if got.Action != catalogdoctor.Delete {
			t.Errorf("action = %s, want delete", got.Action)
		}
		if !contains(orphanKeys(result), "eu-startups.com") {
			t.Errorf("orphans = %v, want to include eu-startups.com", orphanKeys(result))
		}
	})

	t.Run("ats job listing is deleted", func(t *testing.T) {
		id := uuid.New()
		co := newCompany(id, "join.com")
		page := newPage(id, "https://join.com/companies/zara/16405887-role")

		result := catalogdoctor.Plan([]*crawler.CareerPage{page}, []*crawler.Company{co})

		got := byURL(t, result)[page.URL]
		if got.Action != catalogdoctor.Delete {
			t.Errorf("action = %s, want delete", got.Action)
		}
	})

	t.Run("join.com host-company tenants are re-attributed and the host orphaned", func(t *testing.T) {
		hostID := uuid.New()
		host := newCompany(hostID, "join.com")
		zara := newPage(hostID, "https://join.com/companies/zara")
		accenture := newPage(hostID, "https://join.com/companies/accenture")
		samsonite := newPage(hostID, "https://join.com/companies/samsonite")
		posting := newPage(hostID, "https://join.com/companies/zara/16405887-role")

		result := catalogdoctor.Plan(
			[]*crawler.CareerPage{zara, accenture, samsonite, posting},
			[]*crawler.Company{host},
		)
		got := byURL(t, result)

		for _, tc := range []struct {
			page    *crawler.CareerPage
			wantKey string
		}{
			{zara, "join:zara"},
			{accenture, "join:accenture"},
			{samsonite, "join:samsonite"},
		} {
			d := got[tc.page.URL]
			if d.Action != catalogdoctor.Reattribute {
				t.Errorf("%s: action = %s, want reattribute", tc.page.URL, d.Action)
				continue
			}
			if d.Target == nil || d.Target.CompanyKey != tc.wantKey {
				t.Errorf("%s: target key = %v, want %s", tc.page.URL, d.Target, tc.wantKey)
			}
			if d.Target != nil && d.Target.ID != uuid.Nil {
				t.Errorf("%s: target id = %s, want nil (new company)", tc.page.URL, d.Target.ID)
			}
		}

		if d := got[posting.URL]; d.Action != catalogdoctor.Delete {
			t.Errorf("posting action = %s, want delete", d.Action)
		}
		if !contains(orphanKeys(result), "join.com") {
			t.Errorf("orphans = %v, want to include join.com", orphanKeys(result))
		}
	})

	t.Run("canonical-duplicate rows merge onto one survivor", func(t *testing.T) {
		id := uuid.New()
		co := newCompany(id, "acme.com")
		secure := newPage(id, "https://acme.com/careers")
		insecure := newPage(id, "http://acme.com/careers")

		result := catalogdoctor.Plan(
			[]*crawler.CareerPage{secure, insecure},
			[]*crawler.Company{co},
		)
		got := byURL(t, result)

		if d := got[secure.URL]; d.Action != catalogdoctor.Keep {
			t.Errorf("https row action = %s, want keep", d.Action)
		}
		merge := got[insecure.URL]
		if merge.Action != catalogdoctor.Merge {
			t.Fatalf("http row action = %s, want merge", merge.Action)
		}
		if merge.MergeInto != secure.ID {
			t.Errorf("merge into = %s, want survivor id %s", merge.MergeInto, secure.ID)
		}
		if contains(orphanKeys(result), "acme.com") {
			t.Errorf("acme.com must not be orphaned; it still owns the survivor")
		}
	})

	t.Run("re-attributing to an existing target does not orphan it", func(t *testing.T) {
		hostID := uuid.New()
		host := newCompany(hostID, "join.com")
		destID := uuid.New()
		dest := newCompany(destID, "join:zara") // already in the Catalog, owns no pages yet
		zara := newPage(hostID, "https://join.com/companies/zara")

		result := catalogdoctor.Plan(
			[]*crawler.CareerPage{zara},
			[]*crawler.Company{host, dest},
		)

		d := byURL(t, result)[zara.URL]
		if d.Action != catalogdoctor.Reattribute {
			t.Fatalf("action = %s, want reattribute", d.Action)
		}
		if d.Target == nil || d.Target.ID != destID {
			t.Errorf("target = %v, want the existing dest company %s", d.Target, destID)
		}
		if contains(orphanKeys(result), "join:zara") {
			t.Errorf("existing re-attribution target join:zara must not be orphaned")
		}
		if !contains(orphanKeys(result), "join.com") {
			t.Errorf("host join.com should be orphaned once its tenant moved")
		}
	})

	t.Run("an already-clean catalog yields all keep and no orphans", func(t *testing.T) {
		acmeID := uuid.New()
		acme := newCompany(acmeID, "acme.com")
		ghID := uuid.New()
		gh := newCompany(ghID, "greenhouse:acme")
		hub := newPage(acmeID, "https://acme.com/careers")
		board := newPage(ghID, "https://job-boards.greenhouse.io/acme")

		result := catalogdoctor.Plan(
			[]*crawler.CareerPage{hub, board},
			[]*crawler.Company{acme, gh},
		)

		for _, d := range result.Pages {
			if d.Action != catalogdoctor.Keep {
				t.Errorf("%s: action = %s, want keep (clean catalog is idempotent)", d.Page.URL, d.Action)
			}
		}
		if len(result.Orphans) != 0 {
			t.Errorf("orphans = %v, want none", orphanKeys(result))
		}
	})
}
