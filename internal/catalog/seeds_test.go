package catalog_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestResolveSeeds(t *testing.T) {
	tests := []struct {
		name      string
		seed      crawler.CatalogSeed
		wantScope string
		wantOwner string
	}{
		{
			// Self-hosted: the URL-derived key (eTLD+1) and the stored key coincide.
			name:      "self-hosted seed keys coincide",
			seed:      crawler.CatalogSeed{URL: "https://acme.com/careers", CompanyKey: "acme.com"},
			wantScope: "acme.com",
			wantOwner: "acme.com",
		},
		{
			// ATS tenant: Identify yields the provider-qualified key from the URL.
			name:      "ats-tenant seed keys coincide",
			seed:      crawler.CatalogSeed{URL: "https://boards.greenhouse.io/acme", CompanyKey: "greenhouse:acme"},
			wantScope: "greenhouse:acme",
			wantOwner: "greenhouse:acme",
		},
		{
			// Imported divergence (ADR-0021 criterion 5): the stored key differs
			// from the URL-derived one. Scope follows the URL; Owner is the stored
			// key. A single key would silently break one of the two.
			name:      "imported divergence keeps both keys",
			seed:      crawler.CatalogSeed{URL: "https://acme.com/careers", CompanyKey: "acme-imported"},
			wantScope: "acme.com",
			wantOwner: "acme-imported",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catalog.ResolveSeeds([]crawler.CatalogSeed{tt.seed})
			if len(got) != 1 {
				t.Fatalf("want 1 resolved seed, got %d: %v", len(got), got)
			}
			if got[0].URL != tt.seed.URL {
				t.Errorf("URL: want %q, got %q", tt.seed.URL, got[0].URL)
			}
			if got[0].Scope != tt.wantScope {
				t.Errorf("Scope: want %q, got %q", tt.wantScope, got[0].Scope)
			}
			if got[0].Owner != tt.wantOwner {
				t.Errorf("Owner: want %q, got %q", tt.wantOwner, got[0].Owner)
			}
		})
	}

	t.Run("drops unparseable urls but keeps the rest", func(t *testing.T) {
		got := catalog.ResolveSeeds([]crawler.CatalogSeed{
			{URL: "", CompanyKey: "empty"},
			{URL: "https://acme.com/careers", CompanyKey: "acme.com"},
		})
		if len(got) != 1 {
			t.Fatalf("want the unparseable seed dropped, 1 survivor, got %d: %v", len(got), got)
		}
		if got[0].URL != "https://acme.com/careers" {
			t.Errorf("survivor URL: want the acme seed, got %q", got[0].URL)
		}
		// Every returned seed must carry a real Scope: an empty Scope means "roam".
		if got[0].Scope == "" {
			t.Error("resolved seed must carry a non-empty Scope")
		}
	})

	t.Run("returns non-nil empty slice for empty input", func(t *testing.T) {
		got := catalog.ResolveSeeds(nil)
		if got == nil {
			t.Fatal("want non-nil slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("want empty slice, got %v", got)
		}
	})
}
