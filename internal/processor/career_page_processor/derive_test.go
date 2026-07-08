package careerpageprocessor

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestCompanyName(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		fallback string
		want     string
	}{
		{"strip 'X at Y' prefix", "Current openings at Remote", "remotecom", "Remote"},
		{"strip 'Jobs at' prefix", "Jobs at xAI", "xai", "xAI"},
		{"strip trailing Careers", "Acme Careers", "acme", "Acme"},
		{"strip 'Join' leading word", "Join Globex", "globex", "Globex"},
		{"separator with boilerplate on the right", "Careers - PostHog", "posthog.com", "PostHog"},
		{"separator with boilerplate on the left", "PostHog | Careers", "posthog.com", "PostHog"},
		{"separator dropping multi-word boilerplate", "Open Positions – Tailscale", "tailscale.com", "Tailscale"},
		{"plain company name kept", "Remote", "remotecom", "Remote"},
		{"empty title falls back", "", "remotecom", "remotecom"},
		{"boilerplate-only title falls back", "Careers", "acme", "acme"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := companyName(tt.title, tt.fallback); got != tt.want {
				t.Errorf("companyName(%q, %q) = %q, want %q", tt.title, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestHasJobPostingJSONLD(t *testing.T) {
	tests := []struct {
		name   string
		blocks []string
		want   bool
	}{
		{"nil blocks", nil, false},
		{"no json-ld", []string{}, false},
		{"bare JobPosting node", []string{`{"@type":"JobPosting","title":"Engineer"}`}, true},
		{"schema.org URL @type", []string{`{"@type":"https://schema.org/JobPosting"}`}, true},
		{"@type array containing JobPosting", []string{`{"@type":["Thing","JobPosting"]}`}, true},
		{"JobPosting inside @graph", []string{`{"@graph":[{"@type":"WebPage"},{"@type":"JobPosting"}]}`}, true},
		{"top-level array of nodes", []string{`[{"@type":"WebSite"},{"@type":"JobPosting"}]`}, true},
		{"unrelated types only", []string{`{"@type":"Organization"}`, `{"@type":"WebPage"}`}, false},
		{"malformed json is skipped", []string{`{not json`, `{"@type":"JobPosting"}`}, true},
		{"only malformed json", []string{`{not json`}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasJobPostingJSONLD(tt.blocks); got != tt.want {
				t.Errorf("hasJobPostingJSONLD(%v) = %v, want %v", tt.blocks, got, tt.want)
			}
		})
	}
}

func TestCompanyDomain(t *testing.T) {
	t.Run("self-hosted uses the eTLD+1 CompanyKey", func(t *testing.T) {
		id := catalog.Identity{ATSProvider: "", CompanyKey: "acme.com"}
		if got := companyDomain(id, &crawler.Content{}); got != "acme.com" {
			t.Errorf("companyDomain = %q, want acme.com", got)
		}
	})

	t.Run("ATS extracts registrable domain from Organization JSON-LD", func(t *testing.T) {
		id := catalog.Identity{ATSProvider: "greenhouse", CompanyKey: "greenhouse:remotecom"}
		content := &crawler.Content{JSONLD: []string{
			`{"@type":"Organization","name":"Remote","url":"https://remote.com/about"}`,
		}}
		if got := companyDomain(id, content); got != "remote.com" {
			t.Errorf("companyDomain = %q, want remote.com", got)
		}
	})

	t.Run("ATS reads hiringOrganization inside a JobPosting graph", func(t *testing.T) {
		id := catalog.Identity{ATSProvider: "greenhouse", CompanyKey: "greenhouse:acme"}
		content := &crawler.Content{JSONLD: []string{
			`{"@type":"JobPosting","hiringOrganization":{"@type":"Organization","sameAs":"https://www.acme.io"}}`,
		}}
		if got := companyDomain(id, content); got != "acme.io" {
			t.Errorf("companyDomain = %q, want acme.io", got)
		}
	})

	t.Run("ATS with no Organization JSON-LD is empty", func(t *testing.T) {
		id := catalog.Identity{ATSProvider: "greenhouse", CompanyKey: "greenhouse:acme"}
		content := &crawler.Content{JSONLD: []string{`{"@type":"WebPage"}`}}
		if got := companyDomain(id, content); got != "" {
			t.Errorf("companyDomain = %q, want empty", got)
		}
	})
}
