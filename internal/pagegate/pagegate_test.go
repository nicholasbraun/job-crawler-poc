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

// withThresholds returns cfg with its final-rung thresholds overridden, so a
// band case can place certainθ/rejectθ where the seeded signals cross them —
// DefaultLLMGateConfig's CertainThreshold is deliberately unreachable from the
// final rung.
func withThresholds(cfg crawler.LLMGateConfig, certain, reject float64) crawler.LLMGateConfig {
	cfg.CertainThreshold = certain
	cfg.RejectThreshold = reject
	return cfg
}

// finalRungConfig is DefaultLLMGateConfig's Confidence Score floats with the
// path-signal lists cleared, so a case exercises the final-rung score bands in
// isolation from the earlier career-hub-root and reject-path rungs.
func finalRungConfig() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.CareerPathSignals = nil
	cfg.RejectPathSignals = nil
	return cfg
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
			// Singular editorial forms are covered too (per-segment exact match
			// means "posts" does not catch "/post/..."). A careerish post is
			// still shed before the LLM.
			name: "singular editorial path is rejected before the LLM",
			url:  "https://acme.com/post/why-we-are-hiring",
			content: &crawler.Content{
				Title: "Why we are hiring",
				URLs:  []string{"/post/other", "/about"},
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
		{
			// The posting-path veto (ADR-0010) runs ahead of the link-count
			// heuristic: a single posting that links sibling postings can no longer
			// re-admit itself. Without the reorder this would be accept=true.
			name: "single posting linking sibling postings is still rejected (veto beats link count)",
			url:  "https://acme.com/careers/senior-engineer",
			content: &crawler.Content{
				Title: "Senior Engineer",
				URLs:  []string{"/careers/eng-1", "/careers/eng-2", "/careers/eng-3"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "numeric posting slug that lists jobs is rejected",
			url:  "https://careers.moove.io/jobs/8041975-director-head-of-fleet",
			content: &crawler.Content{
				Title: "Director",
				URLs:  []string{"/jobs/1", "/jobs/2"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// A deep career URL whose terminal segment is a Terminal-Hub Word is a
			// real deep hub: exempted from the veto and accepted (uncertain, since
			// "open-positions" is not a certain CareerPathSignal).
			name: "terminal-hub-word deep path is accepted (synonym, uncertain)",
			url:  "https://acme.com/careers/open-positions",
			content: &crawler.Content{
				Title: "Open Positions",
				URLs:  []string{"/careers/eng-1"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			name: "terminal-hub-word nested deep path is accepted (alle-jobs)",
			url:  "https://jobs.example.de/karriere/jobs/alle-jobs",
			content: &crawler.Content{
				Title: "Alle Jobs",
				URLs:  []string{},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// A deep path whose terminal segment is BOTH a Terminal-Hub Word and a
			// CareerPathSignal is exempted from the veto and then certain-accepts as
			// a hub root.
			name: "terminal career-signal deep path is certain (exempt + hub root)",
			url:  "https://acme.com/careers/vacancies",
			content: &crawler.Content{
				Title: "Vacancies",
				URLs:  []string{},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// A software-docs page ending in a career token would be certain-accepted
			// by the career-hub rule; the new `docs` reject-path sheds it first.
			name: "software-docs path ending in jobs is rejected, not certain-accepted",
			url:  "https://acme.com/docs/advanced/jobs",
			content: &crawler.Content{
				Title: "Jobs API",
				URLs:  []string{"/docs/advanced/queue"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "media tag path is rejected",
			url:  "https://acme.com/tag/careers",
			content: &crawler.Content{
				Title: "Careers tag",
				URLs:  []string{},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "media category path is rejected",
			url:  "https://acme.com/category/jobs",
			content: &crawler.Content{
				Title: "Jobs category",
				URLs:  []string{},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		// Final-rung Confidence Score band mapping (ADR-0016). All four use the
		// neutral path /team so no earlier rung fires (not an aggregator/ATS host,
		// not a reject/posting/career-signal segment); signals are driven purely by
		// title/links, and the verdict comes from the final rung's score bands.
		{
			name: "final rung: no signal lands in reject",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Team",
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			name: "final rung: one signal stays uncertain (accept)",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Careers",
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// A single stray same-host posting never certain-accepts: keyword (0.5) plus one
			// same-host Job Listing link (1.0·min(1/5,1)=0.2) = 0.7, below certainθ 1.4, so
			// it stays uncertain and still reaches the LLM (ADR-0016, #98).
			name: "final rung: keyword + a single same-host job link stays uncertain",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/jobs/eng-1"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// Dense same-host openings index: a career keyword (0.5) plus a saturated
			// same-host Job Listing set (1.0) reaches 1.5 >= certainθ 1.4, so the default
			// config certain-accepts it with no LLM call (ADR-0016, #98).
			name: "final rung: keyword + dense same-host index certain-accepts under the default",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/jobs/1", "/jobs/2", "/jobs/3", "/jobs/4", "/jobs/5", "/jobs/6"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// The load-bearing invariant (ADR-0016): saturated same-host links alone (1.0)
			// do NOT reach certainθ 1.4 without a career keyword, so a culture/about page
			// that densely links same-host /careers/* siblings but carries no career keyword
			// (e.g. an "About Us" page) stays uncertain, never a False-Certain.
			name: "final rung: dense same-host index without a career keyword stays uncertain",
			url:  "https://acme.com/company",
			content: &crawler.Content{
				Title: "About Us",
				URLs:  []string{"/careers/1", "/careers/2", "/careers/3", "/careers/4", "/careers/5", "/careers/6"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// A Terminal-Hub-Word deep hub (exempt from the posting-path veto) that links a
			// dense set of same-host postings now certain-accepts from the final rung -- the
			// openings-index flip this ticket buys (mirrors the datarobot/playlist fixtures).
			name: "dense same-host openings index on a terminal-hub-word deep path is certain",
			url:  "https://acme.com/careers/open-positions",
			content: &crawler.Content{
				Title: "Open Positions",
				URLs: []string{
					"/careers/open-positions/job/1", "/careers/open-positions/job/2",
					"/careers/open-positions/job/3", "/careers/open-positions/job/4",
					"/careers/open-positions/job/5", "/careers/open-positions/job/6",
				},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// A structured-data openings index (ItemList wrapping JobPostings)
			// alone clears certainθ (1.5 >= 1.4), so a hub annotated only by JSON-LD
			// certain-accepts with no LLM call (ADR-0016, #99).
			name: "final rung: JSON-LD ItemList of JobPosting alone certain-accepts",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Team",
				JSONLD: []string{`{"@context":"https://schema.org","@type":"ItemList","itemListElement":[
					{"@type":"ListItem","position":1,"item":{"@type":"JobPosting","title":"Engineer"}},
					{"@type":"ListItem","position":2,"item":{"@type":"JobPosting","title":"Designer"}}]}`},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// Two standalone JobPosting nodes (no ItemList) are a structured-data
			// openings index too: postings >= 2 fires the signal.
			name: "final rung: two standalone JobPosting nodes certain-accept",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Team",
				JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`, `{"@type":"JobPosting","title":"Designer"}`},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// The load-bearing guard (ADR-0016, #99): a LONE JobPosting earns no hub
			// credit, so a keyword page carrying one stays at 0.5 -- uncertain, never
			// a False-Certain. A single Job Listing must never certain-accept.
			name: "final rung: lone JobPosting earns no hub credit, keyword page stays uncertain",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Careers",
				JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// A lone JobPosting with no other signal scores 0 -> reject: it adds zero,
			// not negative, and does not sneak the page into the uncertain band.
			name: "final rung: lone JobPosting with no other signal rejects",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Team",
				JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// An ItemList of NON-JobPosting items (site-nav links) fires nothing --
			// the guard against a generic ItemList (e.g. a SiteNavigationElement menu)
			// certain-accepting a non-hub. Mirrors the basecamp/about Gold-Set page.
			name: "final rung: ItemList of non-JobPosting items earns no credit",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Team",
				JSONLD: []string{`{"@type":"ItemList","itemListElement":[
					{"@type":"SiteNavigationElement","name":"Pricing"},
					{"@type":"SiteNavigationElement","name":"Features"}]}`},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// Fail-safe (ADR-0016, #99): unparseable JSON-LD is skipped, leaving the
			// page in exactly the band it would reach without it -- here a lone career
			// keyword (0.5), still uncertain. Never a new Leak or False-Certain.
			name: "final rung: unparseable JSON-LD leaves the band unchanged",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Careers",
				JSONLD: []string{`{"@type":"ItemList", broken`},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		// Cleared path signals with the seeded floats: these exercise the final-rung
		// score in isolation from the career-hub-root and reject-path rungs (which
		// don't fire without configured path signals). A zero-value config can no
		// longer stand in — its zeroed thresholds would certain-accept every page.
		{
			name: "legacy: career-hub index with job links is accepted but uncertain",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/careers/eng-2"},
			},
			cfg:         finalRungConfig(),
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
			cfg:         finalRungConfig(),
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
			cfg:         finalRungConfig(),
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

// TestIsPostingPath locks the URL-only posting-path predicate the Catalog Doctor
// reuses (ADR-0010): it makes the terminalHubWords exemption explicit and proves
// the veto reads only the URL, no page content.
func TestIsPostingPath(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		// Exempted deep hubs -- every Terminal-Hub Word as a terminal segment.
		{"open-positions terminal is a hub", "https://acme.com/careers/open-positions", false},
		{"open-jobs terminal is a hub", "https://acme.com/careers/open-jobs", false},
		{"opportunities terminal is a hub", "https://acme.com/careers/opportunities", false},
		{"openings terminal is a hub", "https://acme.com/jobs/openings", false},
		{"positions terminal is a hub", "https://acme.com/jobs/positions", false},
		{"vacancies terminal is a hub", "https://acme.com/careers/vacancies", false},
		{"job-board terminal is a hub", "https://acme.com/careers/job-board", false},
		{"alle-jobs terminal is a hub", "https://acme.com/karriere/jobs/alle-jobs", false},
		{"all-jobs terminal is a hub", "https://acme.com/jobs/all-jobs", false},
		{"jobsearch terminal is a hub", "https://acme.com/careers/jobsearch", false},
		{"job-search terminal is a hub", "https://acme.com/careers/job-search", false},
		{"offene-stellen terminal is a hub", "https://acme.com/karriere/offene-stellen", false},
		{"karriere terminal is a hub", "https://acme.com/de/careers/berlin/karriere", false},
		// Vetoed single postings / deep sub-pages (role slug or culture leaf).
		{"role slug is a posting", "https://acme.com/careers/senior-engineer", true},
		{"numeric id is a posting", "https://acme.com/jobs/8041975-director", true},
		{"singular job segment posting", "https://acme.com/job/leitung-entwicklung", true},
		{"culture sub-page under careers is vetoed", "https://acme.com/careers/how-we-hire", true},
		// Non-posting shapes: bare hubs and unrelated paths.
		{"bare careers root is not a posting", "https://acme.com/careers", false},
		{"nested careers root is not a posting", "https://acme.com/about/careers", false},
		{"blog path is not a posting", "https://acme.com/blog/hello", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pagegate.IsPostingPath(newURL(t, tt.url)); got != tt.want {
				t.Errorf("IsPostingPath(%q) = %v, want %v", tt.url, got, tt.want)
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
