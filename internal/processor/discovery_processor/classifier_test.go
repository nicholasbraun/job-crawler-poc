package discoveryprocessor_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	discoveryprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/discovery_processor"
)

func TestGate(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		content     *crawler.Content
		wantAccept  bool
		wantCertain bool
	}{
		{
			name:        "ATS board root is a certain career page",
			url:         "https://job-boards.greenhouse.io/acme",
			content:     &crawler.Content{Title: "Jobs at Acme"},
			wantAccept:  true,
			wantCertain: true,
		},
		{
			name:        "ATS job posting is rejected",
			url:         "https://job-boards.greenhouse.io/acme/jobs/123",
			content:     &crawler.Content{Title: "Job Application for Engineer at Acme"},
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "self-hosted index with many job links is an uncertain career page",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/careers/eng-2", "/careers/eng-3"},
			},
			wantAccept:  true,
			wantCertain: false,
		},
		{
			name: "self-hosted careers page with a single opening is still accepted",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/about"},
			},
			wantAccept:  true,
			wantCertain: false,
		},
		{
			name: "self-hosted careers hub with no static job links is still accepted",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Join our team",
				URLs:  []string{"/about", "/contact"},
			},
			wantAccept:  true,
			wantCertain: false,
		},
		{
			name: "self-hosted single posting that lists no other jobs is rejected",
			url:  "https://acme.com/careers/senior-go",
			content: &crawler.Content{
				Title: "Senior Go Engineer",
				URLs:  []string{"/careers", "/about"},
			},
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "unrelated page with no career signal is rejected",
			url:  "https://acme.com/blog/hello",
			content: &crawler.Content{
				Title: "Hello World",
				URLs:  []string{"/blog/other", "/about"},
			},
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "outbound job links on other hosts are not counted",
			url:  "https://acme.com/blog",
			content: &crawler.Content{
				Title: "Blog",
				URLs:  []string{"https://other.com/jobs/1", "https://other.com/jobs/2", "https://other.com/jobs/3"},
			},
			wantAccept:  false,
			wantCertain: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accept, certain := discoveryprocessor.Gate(newURL(t, tt.url), tt.content)
			if accept != tt.wantAccept {
				t.Errorf("Gate(%q) accept = %v, want %v", tt.url, accept, tt.wantAccept)
			}
			if certain != tt.wantCertain {
				t.Errorf("Gate(%q) certain = %v, want %v", tt.url, certain, tt.wantCertain)
			}
		})
	}
}
