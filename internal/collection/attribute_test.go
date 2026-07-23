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
