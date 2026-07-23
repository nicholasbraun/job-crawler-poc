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
			// A single stray same-host posting never certain-accepts: keyword (0.5) plus a
			// strong "Careers" title (0.5) plus one same-host Job Listing link
			// (1.0·min(1/5,1)=0.2) = 1.2, below certainθ 1.25, so it stays uncertain and
			// still reaches the LLM (ADR-0016, #98).
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
			// Dense same-host openings index: a career keyword (0.5), a strong "Careers"
			// title (0.5), and a saturated same-host Job Listing set (1.0) reach 2.0 >=
			// certainθ 1.25, so the default config certain-accepts it with no LLM call
			// (ADR-0016, #98).
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
			// do NOT reach certainθ 1.25 without a career keyword, so a culture/about page
			// that densely links same-host /careers/* siblings but carries no career keyword
			// (an "About Us" page — no keyword, no strong title) stays uncertain, never a
			// False-Certain.
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
		// Lever D + title-strength final-rung signal (ADR-0016, #101). Neutral/compound
		// paths so no earlier rung fires; signals are driven by Title/URLs and the
		// verdict comes from the raised rejectθ 0.75 / lowered certainθ 1.25 bands.
		{
			// Lever D headline: a weak-keyword-only page auto-rejects. The URL substring
			// "karriere" scores the weak keyword (0.5), but "karriere-bei-acme" is a
			// compound slug (not an exact segment) and "Arbeiten bei Acme" is no strong
			// title, so title strength stays silent → 0.5 <= rejectθ 0.75 → reject.
			name: "final rung: weak-keyword-only page auto-rejects (Lever D)",
			url:  "https://acme.com/karriere-bei-acme/team",
			content: &crawler.Content{
				Title: "Arbeiten bei Acme",
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// Zero-Leak & no-False-Certain guard: a career keyword (0.5) plus a strong
			// careers title ("Jobs & Karriere" leads with "jobs", 0.5) = 1.0 clears
			// rejectθ 0.75 but stays below certainθ 1.25 → uncertain. Lexical evidence
			// alone never certain-accepts (certain still needs a Structural Signal).
			name: "final rung: keyword + strong careers title stays uncertain",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title: "Jobs & Karriere",
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// The URL-segment lift (kfw shape): a career token as an exact NON-terminal
			// path segment scores title strength even with a plain title. Keyword (0.5,
			// "karriere" substring) + segment strength (0.5) = 1.0 → uncertain. Proves
			// the raised rejectθ does not leak a real career sub-page.
			name: "final rung: keyword + exact non-terminal career segment stays uncertain",
			url:  "https://acme.com/karriere/studierende",
			content: &crawler.Content{
				Title: "Praktikum",
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// Accumulation past the lowered certainθ: a career keyword ("Careers" title,
			// 0.5) + title strength (0.5) + a dense same-host index (1.0) = 2.0 >=
			// certainθ 1.25 → certain, no LLM call.
			name: "final rung: keyword + strong title + dense same-host index certain-accepts",
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
			// alone clears certainθ (1.5 >= 1.25), so a hub annotated only by JSON-LD
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
			// credit, so a keyword page carrying one rides only its lexical evidence
			// (keyword 0.5 + strong "Careers" title 0.5 = 1.0) -- uncertain, never a
			// False-Certain. A single Job Listing must never certain-accept.
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
			// page in exactly the band it would reach without it -- here lexical evidence
			// (keyword 0.5 + strong "Careers" title 0.5 = 1.0), still uncertain. Never a
			// new Leak or False-Certain.
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
		// ATS Embed final-rung signal (ADR-0016, #100). All use the neutral path
		// /team so no earlier rung fires; the verdict comes purely from the embed
		// term (weight 1.5 >= certainθ 1.25). Embeds/ElementIDs are set directly —
		// the Gate seam, testing interpretation independent of the parser.
		{
			// An iframe to a known ATS host is a page-specific board, so it fires
			// with no marker (Personio embeds via {tenant}.jobs.personio.de).
			name: "iframe to an ATS host alone certain-accepts",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Team",
				Embeds: []crawler.Embed{{Src: "https://acme.jobs.personio.de/search", IsFrame: true}},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			name: "greenhouse script with its board container certain-accepts",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:      "Team",
				Embeds:     []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
				ElementIDs: []string{"grnhse_app"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			name: "ashby script with ashby_embed container certain-accepts",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:      "Team",
				Embeds:     []crawler.Embed{{Src: "https://jobs.ashbyhq.com/acme/embed"}},
				ElementIDs: []string{"ashby_embed"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// The BambooHR marker is literally "BambooHR": element ids are
			// case-sensitive, so hasElementID matches it exactly.
			name: "bamboohr script with BambooHR container certain-accepts",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:      "Team",
				Embeds:     []crawler.Embed{{Src: "https://acme.bamboohr.com/js/embed.js"}},
				ElementIDs: []string{"BambooHR"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// False-Certain guard: a site-wide embed script with no rendered board
			// container fires nothing, so the page rides only its lexical evidence
			// (keyword 0.5 + strong "Careers" title 0.5 = 1.0) — uncertain, never
			// certain-accepted.
			name: "site-wide embed script with no board container stays out of certain",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Careers",
				Embeds: []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// The same site-wide script with no other signal contributes 0 -> reject.
			name: "site-wide embed script with no other signal rejects",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Team",
				Embeds: []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// The marker must match the script's OWN provider: a Greenhouse script
			// with only Ashby's container present fires nothing.
			name: "script to an ATS host with the wrong provider's marker stays out of certain",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:      "Careers",
				Embeds:     []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
				ElementIDs: []string{"ashby_embed"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// Fail-safe: an unrecognized embed host earns no credit even when a
			// real provider marker happens to be present.
			name: "unrecognized embed host earns no credit even with a marker present",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:      "Careers",
				Embeds:     []crawler.Embed{{Src: "https://cdn.example.com/widget.js"}},
				ElementIDs: []string{"grnhse_app"},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  true,
			wantCertain: false,
		},
		{
			// An iframe to an unrecognized host (a YouTube embed) is not an ATS
			// board: 0 credit -> reject.
			name: "iframe to an unrecognized host earns no credit",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:  "Team",
				Embeds: []crawler.Embed{{Src: "https://www.youtube.com/embed/xyz", IsFrame: true}},
			},
			cfg:         crawler.DefaultLLMGateConfig(),
			wantAccept:  false,
			wantCertain: false,
		},
		{
			// Lever has no curated embed marker (it is handled by the hosted-board
			// Classify, not the embed detector), so a Lever script fires nothing
			// even with an unrelated provider's marker present.
			name: "lever script earns no embed credit (no curated marker)",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Title:      "Careers",
				Embeds:     []crawler.Embed{{Src: "https://jobs.lever.co/acme"}},
				ElementIDs: []string{"grnhse_app"},
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
			// With the path signals cleared this reaches the final rung and now scores
			// a career keyword (0.5) + title strength ("Careers" leads, 0.5) + a thin
			// same-host index (1.0·min(2/5,1)=0.4) = 1.4 >= certainθ 1.25 → certain. In
			// production /careers never reaches the final rung (careerHubRoot certain-
			// accepts it first), so this only re-characterizes the synthetic isolation.
			name: "legacy: career-hub index with job links now certain-accepts",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Careers",
				URLs:  []string{"/careers/eng-1", "/careers/eng-2"},
			},
			cfg:         finalRungConfig(),
			wantAccept:  true,
			wantCertain: true,
		},
		{
			// Lever D (#101): a weak-keyword-only page auto-rejects. With signals
			// cleared the URL substring "careers" scores the weak keyword (0.5) but
			// "Join our team" is no strong title (leads with "join", excluded) and the
			// cleared segments add nothing → 0.5 <= rejectθ 0.75 → reject.
			name: "legacy: weak-keyword-only page auto-rejects (Lever D)",
			url:  "https://acme.com/careers",
			content: &crawler.Content{
				Title: "Join our team",
				URLs:  []string{"/about", "/contact"},
			},
			cfg:         finalRungConfig(),
			wantAccept:  false,
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
	// cfg is a pointer so a case can override the gate config (the saturation
	// fail-safe case zeroes ExtractJobLinkSaturationCount); a nil cfg uses
	// DefaultLLMGateConfig. content is always set (the live call sites parse the
	// page first); the URL-rung cases use empty content because a URL rung resolves
	// them before any content rung reads it.
	tests := []struct {
		name    string
		url     string
		content *crawler.Content
		cfg     *crawler.LLMGateConfig
		want    bool
	}{
		{
			name:    "ATS board root is resolved without the extractor",
			url:     "https://job-boards.greenhouse.io/acme",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "ATS posting reaches the extractor",
			url:     "https://job-boards.greenhouse.io/acme/jobs/123",
			content: &crawler.Content{},
			want:    true,
		},
		{
			name:    "career-hub index is an index to crawl, not extract",
			url:     "https://acme.com/careers",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "reject path is dropped before the extractor",
			url:     "https://acme.com/blog/hello",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "editorial reject path is dropped before the extractor",
			url:     "https://acme.com/magazine/issue-5",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "self-hosted posting with no signal reaches the extractor",
			url:     "https://acme.com/o/senior-engineer",
			content: &crawler.Content{},
			want:    true,
		},
		{
			// Rung 2 exempts an ATS posting BEFORE any content rung, so its saturated
			// "more openings" sidebar and JSON-LD openings index cannot drop it.
			name: "ATS posting with a saturated sidebar and a JSON-LD index still extracts",
			url:  "https://job-boards.greenhouse.io/acme/jobs/123",
			content: &crawler.Content{
				URLs: []string{
					"/acme/jobs/1", "/acme/jobs/2", "/acme/jobs/3",
					"/acme/jobs/4", "/acme/jobs/5", "/acme/jobs/6",
				},
				JSONLD: []string{`{"@type":"ItemList","itemListElement":[
					{"@type":"ListItem","item":{"@type":"JobPosting","title":"Engineer"}},
					{"@type":"ListItem","item":{"@type":"JobPosting","title":"Designer"}}]}`},
			},
			want: true,
		},
		{
			// A /jobs/all hub is a job-posting path, so rung 4 does NOT reject it
			// (IsPostingPath is not blanket-exempt); its JSON-LD ItemList is caught by
			// rung 6 instead.
			name: "/jobs/all hub with a JSON-LD openings index rejects",
			url:  "https://acme.com/jobs/all",
			content: &crawler.Content{
				JSONLD: []string{`{"@type":"ItemList","itemListElement":[
					{"@type":"ListItem","item":{"@type":"JobPosting","title":"Engineer"}}]}`},
			},
			want: false,
		},
		{
			// Rung 5: a page embedding a whole ATS board (an iframe to a known ATS
			// host) is a hub, not a single posting.
			name: "ATS embed rejects",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				Embeds: []crawler.Embed{{Src: "https://boards.greenhouse.io/acme", IsFrame: true}},
			},
			want: false,
		},
		{
			// Rung 6: two standalone JobPosting nodes are a structured-data openings
			// index (postings >= 2), so the page is a hub.
			name: "two JobPosting nodes reject",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`, `{"@type":"JobPosting","title":"Designer"}`},
			},
			want: false,
		},
		{
			// Rung 7: five distinct same-host job links saturate the extract count
			// (K=5), marking a jobs index.
			name: "saturated same-host job links reject",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				URLs: []string{
					"/careers/role-1", "/careers/role-2", "/careers/role-3",
					"/careers/role-4", "/careers/role-5",
				},
			},
			want: false,
		},
		{
			// A real self-hosted posting: a lone JobPosting and a sidebar of only two
			// sibling links (below K=5) trip none of the content rungs, so it extracts.
			// This is the false-drop protection the calibration buys.
			name: "self-hosted posting with a lone JobPosting and a small sidebar extracts",
			url:  "https://acme.com/careers/senior-engineer",
			content: &crawler.Content{
				JSONLD: []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
				URLs:   []string{"/careers/role-1", "/careers/role-2"},
			},
			want: true,
		},
		{
			// The k-1 saturation boundary (K=5): a real self-hosted posting whose "more
			// openings" sidebar links FOUR distinct same-host siblings is the LAST count
			// that must still extract -- jobLinkSaturation(4, 5) = 0.8 < 1, so rung 7
			// stays silent. This pins the "up to 4 sibling links of headroom" that the
			// ExtractJobLinkSaturationCount comment and ADR-0019 promise: without it the
			// gold set draws the saturation line only from the hub side (5, test 11) and
			// a degenerate posting side (2, above), never the headroom between -- so
			// lowering the count below 5 would false-drop this posting with the whole
			// suite staying green.
			name: "self-hosted posting with a four-link sidebar still extracts (k-1 boundary)",
			url:  "https://acme.com/careers/senior-engineer",
			content: &crawler.Content{
				JSONLD: []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
				URLs: []string{
					"/careers/role-1", "/careers/role-2",
					"/careers/role-3", "/careers/role-4",
				},
			},
			want: true,
		},
		{
			// Saturation fail-safe: zeroing ExtractJobLinkSaturationCount silences
			// rung 7, so the same saturated page falls through to the extractor.
			name: "saturation fail-safe: a zero extract count silences rung 7",
			url:  "https://acme.com/team",
			content: &crawler.Content{
				URLs: []string{
					"/careers/role-1", "/careers/role-2", "/careers/role-3",
					"/careers/role-4", "/careers/role-5",
				},
			},
			cfg:  zeroExtractSaturation(),
			want: true,
		},
		{
			// rung 2b: a careers-landing / homepage at a bare domain root is never a
			// single posting -- the extractor would otherwise key a whole jobs-landing
			// page to a root URL that collides in the Corpus.
			name:    "bare domain root rejects (careers landing keyed to root)",
			url:     "https://karriere.hanwag.de/",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "bare domain root without trailing slash rejects",
			url:     "https://www.demecan.de",
			content: &crawler.Content{},
			want:    false,
		},
		{
			// rung 2b: a locale-only root (/en, /de-de) is still a section root.
			name:    "locale-only root rejects",
			url:     "https://acme.com/en",
			content: &crawler.Content{},
			want:    false,
		},
		{
			// rung 2c: a terminal jobs-index word is a hub, not a posting.
			name:    "terminal index word rejects (job-offers)",
			url:     "https://careers.greentube.com/job-offers",
			content: &crawler.Content{},
			want:    false,
		},
		{
			// rung 2c: the same, served as a static .html page (common on DE sites).
			name:    "terminal index word with a .html suffix rejects",
			url:     "https://firma.de/stellenangebote.html",
			content: &crawler.Content{},
			want:    false,
		},
		{
			// rung 2c catches a posting-path URL whose terminal is a hub word, which
			// rung 4 misses (its parent "careers" makes isJobPostingPath true).
			name:    "posting-path openings index rejects (rung 4 misses it)",
			url:     "https://acme.com/careers/openings",
			content: &crawler.Content{},
			want:    false,
		},
		{
			// Guard: a real single-segment posting slug must still extract -- it is not
			// a locale and not an index word.
			name:    "single-segment posting slug still extracts",
			url:     "https://acme.com/senior-go-engineer",
			content: &crawler.Content{},
			want:    true,
		},
		{
			// Guard: a locale-prefixed real posting (deeper than one segment) still
			// extracts -- rung 2b only fires on a locale-ONLY root.
			name:    "locale-prefixed posting still extracts",
			url:     "https://acme.com/en/o/senior-engineer",
			content: &crawler.Content{},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := crawler.DefaultLLMGateConfig()
			if tt.cfg != nil {
				cfg = *tt.cfg
			}
			if got := pagegate.ShouldExtract(newURL(t, tt.url), tt.content, cfg); got != tt.want {
				t.Errorf("ShouldExtract(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// zeroExtractSaturation returns DefaultLLMGateConfig with the Extract Gate's
// saturation count zeroed, exercising the rung-7 fail-safe (an unset count leaves
// the saturation reject silent).
func zeroExtractSaturation() *crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.ExtractJobLinkSaturationCount = 0
	return &cfg
}
