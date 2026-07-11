package pagegate_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

func newURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("error building url: %v", err)
	}
	return u
}

func TestCareerPage(t *testing.T) {
	tests := []struct {
		name        string
		url         string
		content     *crawler.Content
		cfg         crawler.LLMGateConfig
		wantAccept  bool
		wantCertain bool
	}{
		{
			name:        "ATS board root is a certain career page",
			url:         "https://job-boards.greenhouse.io/acme",
			content:     &crawler.Content{Title: "Jobs at Acme"},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// A multi-company board is rejected outright, even though its host and
			// on-page job links would otherwise read as a career hub.
			name: "aggregator/board host is rejected outright",
			url:  "https://builtin.com/jobs",
			content: &crawler.Content{
				Title: "Jobs on Built In",
				URLs:  []string{"https://builtin.com/jobs/1", "https://builtin.com/jobs/2"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name:        "VC-portfolio board on a subdomain is rejected outright",
			url:         "https://jobsinvc.getro.com/companies/acme",
			content:     &crawler.Content{Title: "Portfolio jobs"},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name:        "ATS job posting is rejected",
			url:         "https://job-boards.greenhouse.io/acme/jobs/123",
			content:     &crawler.Content{Title: "Job Application for Engineer at Acme"},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "self-hosted career-hub path with many job links is now certain",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/careers/eng-2", "/careers/eng-3"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			name: "self-hosted career-hub path with a single opening is now certain",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/about"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			name: "self-hosted career-hub path with no static job links is now certain",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Join our team",
				URLs:  []string{"/about", "/contact"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// A labeled sub-page beneath a career section (e.g. /karriere/arbeiten-bei-uns)
			// is NOT a hub root: the career signal is not the last path segment, so it is
			// accepted only as uncertain and left to the LLM, never certain-accepted.
			name: "career sub-page is accepted but uncertain, not certain",
			url:  "https://acme.com/karriere/arbeiten-bei-uns",
			content: &crawler.Content{
				Title: "Arbeiten bei Acme",
				URLs:  []string{"/about", "/contact"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// A bare career-hub root nested under other segments still certain-accepts:
			// the career signal is the terminal segment.
			name: "nested career-hub root is still certain",
			url:  "https://acme.com/about/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/about", "/contact"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			name: "self-hosted single posting that lists no other jobs is rejected",
			url:  "https://acme.com/careers/senior-go",
			content: &crawler.Content{
				Title: "Senior Go Engineer",
				URLs:  []string{"/careers", "/about"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "reject path is dropped before the LLM",
			url:  "https://acme.com/blog/hello",
			content: &crawler.Content{
				Title: "Hello World",
				URLs:  []string{"/blog/other", "/about"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// A careerish editorial page -- its copy trips the career-keyword
			// heuristic via "join" -- is still shed by its reject-path segment
			// before any LLM classify call. This is exactly why the editorial
			// tokens were added once the URL filter stopped blocking them.
			name: "careerish editorial path is rejected before the LLM",
			url:  "https://acme.com/articles/why-you-should-join-our-team",
			content: &crawler.Content{
				Title: "Why you should join our team",
				URLs:  []string{"/articles/other", "/about"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// A neutral path (no career or reject signal, no career keyword in the
			// URL or title) falls through to the link count; outbound postings on
			// another host are not counted, so it is rejected.
			name: "outbound job links on other hosts are not counted",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Team",
				URLs:  []string{"https://other.com/jobs/1", "https://other.com/jobs/2"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		// Zero-value config reproduces the legacy behavior: no URL-path signals,
		// so a career-hub path is only accepted via the careerish/listsJobs
		// heuristic and is never certain.
		{
			name: "legacy: career-hub index with job links is accepted but uncertain",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/careers/eng-2"},
			},
			cfg:         crawler.LLMGateConfig{},
			wantAccept:  true,
			wantCertain: false,
		},
		{
			name: "legacy: career-hub with no job links is accepted but uncertain",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Join our team",
				URLs:  []string{"/about", "/contact"},
			},
			cfg:         crawler.LLMGateConfig{},
			wantAccept:  true,
			wantCertain: false,
		},
		{
			name: "legacy: blog with no career signal is rejected",
			url:  "https://acme.com/blog/hello",
			content: &crawler.Content{
				Title: "Hello World",
				URLs:  []string{"/blog/other", "/about"},
			},
			cfg:         crawler.LLMGateConfig{},
			wantAccept:  false,
			wantCertain: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accept, certain := pagegate.CareerPage(newURL(t, tt.url), tt.content, tt.cfg)
			if accept != tt.wantAccept {
				t.Errorf("CareerPage(%q) accept = %v, want %v", tt.url, accept, tt.wantAccept)
			}
			if certain != tt.wantCertain {
				t.Errorf("CareerPage(%q) certain = %v, want %v", tt.url, certain, tt.wantCertain)
			}
		})
	}
}

func TestShouldExtract(t *testing.T) {
	cfg := crawler.DefaultLLMGateConfig()
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "ATS board root is resolved without the extractor",
			url:  "https://job-boards.greenhouse.io/acme",
			want: false,
		},
		{
			name: "ATS posting reaches the extractor",
			url:  "https://job-boards.greenhouse.io/acme/jobs/123",
			want: true,
		},
		{
			name: "career-hub index is an index to crawl, not extract",
			url:  "https://acme.com/careers",
			want: false,
		},
		{
			name: "reject path is dropped before the extractor",
			url:  "https://acme.com/blog/hello",
			want: false,
		},
		{
			name: "editorial reject path is dropped before the extractor",
			url:  "https://acme.com/magazine/issue-5",
			want: false,
		},
		{
			name: "self-hosted posting with no signal reaches the extractor",
			url:  "https://acme.com/o/senior-engineer",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pagegate.ShouldExtract(newURL(t, tt.url), cfg); got != tt.want {
				t.Errorf("ShouldExtract(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
