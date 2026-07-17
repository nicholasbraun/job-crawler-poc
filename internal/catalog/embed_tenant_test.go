package catalog_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestATSEmbedTenant(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		wantProvider string
		wantTenant   string
		wantOK       bool
	}{
		{
			name:         "greenhouse script embed carries tenant in ?for",
			src:          "https://boards.greenhouse.io/embed/job_board/js?for=acme",
			wantProvider: "greenhouse",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:         "greenhouse iframe embed carries tenant in ?for",
			src:          "https://boards.greenhouse.io/embed/job_board?for=acme",
			wantProvider: "greenhouse",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:         "greenhouse EU board host carries tenant in ?for",
			src:          "https://job-boards.eu.greenhouse.io/embed/job_board/js?for=acme",
			wantProvider: "greenhouse",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:         "greenhouse ?for tenant is lowercased",
			src:          "https://boards.greenhouse.io/embed/job_board/js?for=Acme",
			wantProvider: "greenhouse",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:   "greenhouse embed missing ?for has no tenant",
			src:    "https://boards.greenhouse.io/embed/job_board/js",
			wantOK: false,
		},
		{
			name:         "personio subdomain iframe carries tenant in the label",
			src:          "https://acme.jobs.personio.de/search",
			wantProvider: "personio",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:         "recruitee subdomain carries tenant in the label",
			src:          "https://acme.recruitee.com",
			wantProvider: "recruitee",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:         "ashby path embed carries tenant in the first path segment",
			src:          "https://jobs.ashbyhq.com/acme/embed",
			wantProvider: "ashby",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:         "bamboohr subdomain script carries tenant in the label",
			src:          "https://acme.bamboohr.com/js/embed.js",
			wantProvider: "bamboohr",
			wantTenant:   "acme",
			wantOK:       true,
		},
		{
			name:   "unrecognized host resolves nothing",
			src:    "https://cdn.example.com/widget.js",
			wantOK: false,
		},
		{
			name:   "bare board host with no tenant path resolves nothing",
			src:    "https://jobs.lever.co/",
			wantOK: false,
		},
		{
			name:   "bare subdomain board host with no tenant label resolves nothing",
			src:    "https://jobs.personio.de/",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, tenant, ok := catalog.ATSEmbedTenant(tt.src)
			if ok != tt.wantOK {
				t.Fatalf("ATSEmbedTenant(%q) ok = %v, want %v", tt.src, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if provider != tt.wantProvider {
				t.Errorf("provider = %q, want %q", provider, tt.wantProvider)
			}
			if tenant != tt.wantTenant {
				t.Errorf("tenant = %q, want %q", tenant, tt.wantTenant)
			}
		})
	}
}
