package catalog_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestNewCompanySnapshot(t *testing.T) {
	tests := []struct {
		name      string
		companies []*crawler.Company
		want      map[string]string
	}{
		{
			name: "maps each stored CompanyKey to its Name",
			companies: []*crawler.Company{
				{CompanyKey: "acme.com", Name: "Acme Inc"},
				{CompanyKey: "greenhouse:beta", Name: "Beta Corp"},
			},
			want: map[string]string{
				"acme.com":        "Acme Inc",
				"greenhouse:beta": "Beta Corp",
			},
		},
		{
			// Imported divergence (ADR-0021): a Company's stored key differs from
			// its URL-derived one. The snapshot keys by the stored CompanyKey so it
			// matches the URL's Owner (also the stored key), not the fence Scope.
			name: "keys by the stored CompanyKey even when it diverges from the URL",
			companies: []*crawler.Company{
				{CompanyKey: "acme-imported", Name: "Acme Inc"},
			},
			want: map[string]string{
				"acme-imported": "Acme Inc",
			},
		},
		{
			name:      "empty input yields a non-nil empty map",
			companies: []*crawler.Company{},
			want:      map[string]string{},
		},
		{
			name:      "nil input yields a non-nil empty map",
			companies: nil,
			want:      map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catalog.NewCompanySnapshot(tt.companies)
			if got == nil {
				t.Fatal("want non-nil map, got nil")
			}
			if len(got) != len(tt.want) {
				t.Fatalf("want %d entries, got %d: %v", len(tt.want), len(got), got)
			}
			for key, name := range tt.want {
				if got[key] != name {
					t.Errorf("snapshot[%q]: want %q, got %q", key, name, got[key])
				}
			}
		})
	}
}
