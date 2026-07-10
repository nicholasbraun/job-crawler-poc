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

func TestOrganizationName(t *testing.T) {
	tests := []struct {
		name   string
		blocks []string
		want   string
	}{
		{"nil blocks", nil, ""},
		{"no usable node", []string{`{"@type":"WebPage"}`}, ""},
		{
			"hiringOrganization name",
			[]string{`{"@type":"JobPosting","hiringOrganization":{"@type":"Organization","name":"paretos GmbH"}}`},
			"paretos GmbH",
		},
		{
			"hiringOrganization inside @graph",
			[]string{`{"@graph":[{"@type":"WebSite"},{"@type":"JobPosting","hiringOrganization":{"name":"Acme"}}]}`},
			"Acme",
		},
		{
			"standalone Organization name",
			[]string{`{"@type":"Organization","name":"Slack"}`},
			"Slack",
		},
		{
			"hiringOrganization wins over a site Organization node",
			[]string{
				`{"@type":"Organization","name":"join.com"}`,
				`{"@type":"JobPosting","hiringOrganization":{"@type":"Organization","name":"paretos GmbH"}}`,
			},
			"paretos GmbH",
		},
		{
			"hiringOrganization as a bare string",
			[]string{`{"@type":"JobPosting","hiringOrganization":"xAI"}`},
			"xAI",
		},
		{"name is trimmed", []string{`{"@type":"Organization","name":"  Remote  "}`}, "Remote"},
		{"malformed json is skipped", []string{`{not json`, `{"@type":"Organization","name":"Globex"}`}, "Globex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := organizationName(tt.blocks); got != tt.want {
				t.Errorf("organizationName(%v) = %q, want %q", tt.blocks, got, tt.want)
			}
		})
	}
}

func TestCompanyNameFrom(t *testing.T) {
	t.Run("JSON-LD company name is preferred over the title", func(t *testing.T) {
		content := &crawler.Content{
			Title:  "Referent*in Politik (m/w/d)",
			JSONLD: []string{`{"@type":"JobPosting","hiringOrganization":{"name":"softgarden"}}`},
		}
		if got := companyNameFrom(content, "softgarden.io"); got != "softgarden" {
			t.Errorf("companyNameFrom = %q, want softgarden", got)
		}
	})

	t.Run("falls back to the title heuristic when no JSON-LD name", func(t *testing.T) {
		content := &crawler.Content{Title: "Acme Careers"}
		if got := companyNameFrom(content, "acme"); got != "Acme" {
			t.Errorf("companyNameFrom = %q, want Acme", got)
		}
	})

	t.Run("falls back to the tenant slug when nothing is usable", func(t *testing.T) {
		content := &crawler.Content{Title: "Careers"}
		if got := companyNameFrom(content, "acme"); got != "acme" {
			t.Errorf("companyNameFrom = %q, want acme", got)
		}
	})
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
