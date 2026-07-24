package collection_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

func TestNewAttributor(t *testing.T) {
	acmeEngID := uuid.New()
	acmeSalesID := uuid.New()
	globexID := uuid.New()

	// Company "acme.com" owns two pages; "globex.com" owns one. companyKeyByID maps
	// each page's CompanyID back to the owning CompanyKey.
	acmeCompany := uuid.New()
	globexCompany := uuid.New()
	companyKeyByID := map[uuid.UUID]string{
		acmeCompany:   "acme.com",
		globexCompany: "globex.com",
	}
	// List order is most-recently-seen first; acme/eng is the freshest acme page.
	pages := []*crawler.CareerPage{
		{ID: acmeEngID, CompanyID: acmeCompany, URL: "https://acme.com/careers/eng", LastSeen: time.Now()},
		{ID: acmeSalesID, CompanyID: acmeCompany, URL: "https://acme.com/careers/sales", LastSeen: time.Now().Add(-time.Hour)},
		{ID: globexID, CompanyID: globexCompany, URL: "https://globex.com/jobs", LastSeen: time.Now()},
	}
	attribute := collection.NewAttributor(pages, companyKeyByID)

	tests := []struct {
		name       string
		companyKey string
		postingURL string
		want       uuid.UUID
	}{
		{"single-page company: always that page", "globex.com", "https://globex.com/jobs/123", globexID},
		{"multi-page: longest path prefix wins", "acme.com", "https://acme.com/careers/sales/42", acmeSalesID},
		{"multi-page: the other subtree", "acme.com", "https://acme.com/careers/eng/7", acmeEngID},
		{"no prefix match falls back to most-recently-seen", "acme.com", "https://acme.com/other/999", acmeEngID},
		{"query params never defeat the match", "globex.com", "https://globex.com/jobs/9?utm=x", globexID},
		{"unknown company yields Nil", "nobody.com", "https://nobody.com/j/1", uuid.Nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attribute(tt.companyKey, tt.postingURL); got != tt.want {
				t.Errorf("attribute(%q, %q) = %v, want %v", tt.companyKey, tt.postingURL, got, tt.want)
			}
		})
	}
}

// TestNewAttributorPrefixBoundary pins the path-segment boundary: a page at a short
// path (".../car") must not raw-prefix-match a posting under a longer sibling
// segment (".../careers/..."), while a page ending exactly on a segment boundary
// still matches.
func TestNewAttributorPrefixBoundary(t *testing.T) {
	company := uuid.New()
	companyKeyByID := map[uuid.UUID]string{company: "acme.com"}

	teamID := uuid.New()
	carID := uuid.New()
	careersID := uuid.New()

	t.Run("short page does not spuriously prefix-match a longer segment", func(t *testing.T) {
		// team is freshest (candidates[0], the fallback); /car would raw-prefix-match
		// "/careers/…" but must not, so attribution falls back to /team, NOT /car.
		pages := []*crawler.CareerPage{
			{ID: teamID, CompanyID: company, URL: "https://acme.com/team", LastSeen: time.Now()},
			{ID: carID, CompanyID: company, URL: "https://acme.com/car", LastSeen: time.Now().Add(-time.Hour)},
		}
		attribute := collection.NewAttributor(pages, companyKeyByID)
		if got := attribute("acme.com", "https://acme.com/careers/eng/7"); got != teamID {
			t.Errorf("posting under /careers must not attribute to the /car page; got %v, want fallback %v", got, teamID)
		}
	})

	t.Run("prefix ending on a segment boundary still matches", func(t *testing.T) {
		pages := []*crawler.CareerPage{
			{ID: teamID, CompanyID: company, URL: "https://acme.com/team", LastSeen: time.Now()},
			{ID: careersID, CompanyID: company, URL: "https://acme.com/careers", LastSeen: time.Now().Add(-time.Hour)},
		}
		attribute := collection.NewAttributor(pages, companyKeyByID)
		if got := attribute("acme.com", "https://acme.com/careers/eng/7"); got != careersID {
			t.Errorf("posting under /careers must attribute to the /careers page; got %v, want %v", got, careersID)
		}
	})

	t.Run("exact URL match attributes to that page", func(t *testing.T) {
		pages := []*crawler.CareerPage{
			{ID: teamID, CompanyID: company, URL: "https://acme.com/team", LastSeen: time.Now()},
			{ID: careersID, CompanyID: company, URL: "https://acme.com/careers", LastSeen: time.Now().Add(-time.Hour)},
		}
		attribute := collection.NewAttributor(pages, companyKeyByID)
		if got := attribute("acme.com", "https://acme.com/careers"); got != careersID {
			t.Errorf("exact-match posting must attribute to the /careers page; got %v, want %v", got, careersID)
		}
	})
}
