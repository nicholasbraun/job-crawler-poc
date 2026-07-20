package careerpageprocessor

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestTitleName(t *testing.T) {
	tests := []struct {
		name  string
		title string
		want  string
	}{
		// A structural cue (connector / separator / suffix / leading word) fired,
		// so the title yields a name.
		{"strip 'X at Y' prefix", "Current openings at Remote", "Remote"},
		{"strip 'Jobs at' prefix", "Jobs at xAI", "xAI"},
		{"strip trailing Careers", "Acme Careers", "Acme"},
		{"strip 'Join' leading word", "Join Globex", "Globex"},
		{"separator with boilerplate on the right", "Careers - PostHog", "PostHog"},
		{"separator with boilerplate on the left", "PostHog | Careers", "PostHog"},
		{"separator dropping multi-word boilerplate", "Open Positions – Tailscale", "Tailscale"},
		{"strip leading article after connector", "Karriere bei der Commerzbank", "Commerzbank"},
		{"collapse newline and strip leading article", "der IHK Berlin\n- IHK Berlin", "IHK Berlin"},
		{"collapse internal whitespace then strip suffix", "Acme\n\tCareers", "Acme"},

		// A bare title carries no structural cue, so the ladder cannot tell a real
		// name from junk: it abstains ("") to the domain rung (ADR-0025).
		{"bare single-word title abstains", "Remote", ""},
		{"bare multi-word title abstains", "Rocket Internet", ""},
		{"bare name sharing a boilerplate word abstains", "Landing AI", ""},
		{"empty title abstains", "", ""},

		// A cue fired but left only boilerplate -> "".
		{"leading-word strip leaves nothing", "Careers", ""},

		// Whole-title boilerplate with no cue abstains.
		{"German 'Offene Stellen' abstains", "Offene Stellen", ""},
		{"German 'Stellenausschreibung' abstains", "Stellenausschreibung", ""},
		{"placeholder 'Landing Page' abstains", "Landing Page", ""},
		{"nav 'Internships' abstains", "Internships", ""},
		{"nav 'Deals' abstains", "Deals", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := titleName(tt.title); got != tt.want {
				t.Errorf("titleName(%q) = %q, want %q", tt.title, got, tt.want)
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

func TestDeriveName(t *testing.T) {
	selfHosted := catalog.Identity{ATSProvider: "", CompanyKey: "acme.com"}
	ats := catalog.Identity{ATSProvider: "greenhouse", CompanyKey: "greenhouse:acme"}

	tests := []struct {
		name       string
		content    *crawler.Content
		identity   catalog.Identity
		llmName    string
		wantName   string
		wantSource crawler.NameSource
	}{
		{
			name: "jsonld wins over meta, llm, and title",
			content: &crawler.Content{
				Title:    "Careers at Acme",
				SiteName: "Acme Site",
				JSONLD:   []string{`{"@type":"Organization","name":"Slack"}`},
			},
			identity:   selfHosted,
			llmName:    "Evil",
			wantName:   "Slack",
			wantSource: crawler.NameSourceJSONLD,
		},
		{
			name:       "meta wins for a self-hosted company when no jsonld",
			content:    &crawler.Content{Title: "Careers at Acme", SiteName: "Süddeutsche Zeitung"},
			identity:   selfHosted,
			llmName:    "Acme GmbH",
			wantName:   "Süddeutsche Zeitung",
			wantSource: crawler.NameSourceMeta,
		},
		{
			name:       "meta abstains on an ATS board (site name is the ATS brand)",
			content:    &crawler.Content{Title: "Jobs at Acme", SiteName: "Greenhouse"},
			identity:   ats,
			wantName:   "Acme",
			wantSource: crawler.NameSourceTitle,
		},
		{
			name:       "llm wins over the title when no jsonld or meta",
			content:    &crawler.Content{Title: "Careers at Acme"},
			identity:   selfHosted,
			llmName:    "Acme GmbH",
			wantName:   "Acme GmbH",
			wantSource: crawler.NameSourceLLM,
		},
		{
			name:       "cued title wins when nothing higher fired",
			content:    &crawler.Content{Title: "Jobs at Acme"},
			identity:   selfHosted,
			wantName:   "Acme",
			wantSource: crawler.NameSourceTitle,
		},
		{
			name:       "everything abstains -> self-hosted domain fallback",
			content:    &crawler.Content{Title: "Remote"},
			identity:   selfHosted,
			wantName:   "acme.com",
			wantSource: crawler.NameSourceDomain,
		},
		{
			name:       "everything abstains -> ATS tenant-slug fallback",
			content:    &crawler.Content{Title: "Remote"},
			identity:   ats,
			wantName:   "acme",
			wantSource: crawler.NameSourceDomain,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, source := deriveName(tt.content, tt.identity, tt.llmName)
			if name != tt.wantName || source != tt.wantSource {
				t.Errorf("deriveName = (%q, %q), want (%q, %q)", name, source, tt.wantName, tt.wantSource)
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
