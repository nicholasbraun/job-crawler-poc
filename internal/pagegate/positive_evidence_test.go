package pagegate_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// requirePositiveEvidence returns DefaultLLMGateConfig with the Positive Evidence
// rung enabled -- the configuration #264 makes the default. Every case in
// TestShouldExtract_PositiveEvidence runs under it; the rung ships off, so the
// existing TestShouldExtract table is deliberately untouched.
func requirePositiveEvidence() crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = true
	return cfg
}

// noEvidenceURL is a self-hosted posting-shaped URL that carries NO Positive
// Evidence of its own: it clears every reject rung (the existing TestShouldExtract
// asserts it extracts today), its host holds no job word, and its only non-terminal
// segment is "o". Every text-mark case below rides it, so what the case proves is
// the text marks alone.
const noEvidenceURL = "https://acme.com/o/senior-engineer"

func TestShouldExtract_PositiveEvidence(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		content *crawler.Content
		want    bool
	}{
		// --- Strong evidence admits alone -------------------------------------
		{
			name:    "posting-shaped URL admits with no content at all",
			url:     "https://acme.com/careers/senior-engineer",
			content: &crawler.Content{},
			want:    true,
		},
		{
			// The compound segment the SHARED posting-path predicate cannot see:
			// "jobs-karriere" is not the segment "jobs", so isJobPostingPath misses it.
			// Token matching inside the segment catches it.
			name:    "compound path segment admits (jobs-karriere)",
			url:     "https://acme.de/jobs-karriere/senior-entwickler",
			content: &crawler.Content{},
			want:    true,
		},
		{
			name:    "compound path segment admits with a firm segment between it and the role",
			url:     "https://acme.de/stellenangebote_unternehmen/bosch/senior-entwickler",
			content: &crawler.Content{},
			want:    true,
		},
		{
			name:    "job-word host with a path admits (karriere.acme.de)",
			url:     "https://karriere.acme.de/senior-entwickler",
			content: &crawler.Content{},
			want:    true,
		},
		{
			name:    "hyphenated job-word host admits (jobs-acme.de)",
			url:     "https://jobs-acme.de/senior-entwickler",
			content: &crawler.Content{},
			want:    true,
		},
		{
			name: "lone structured posting admits on a signal-less URL",
			url:  noEvidenceURL,
			content: &crawler.Content{
				JSONLD: []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
			},
			want: true,
		},
		{
			// Deliberately NOT title-gated: a lone posting node is a statement about the
			// page's SHAPE. The non-empty title ADR-0042 requires is a Free Extraction
			// condition -- what is needed to SAVE without a model -- not a condition on
			// spending a call.
			name: "lone structured posting with no title still admits",
			url:  noEvidenceURL,
			content: &crawler.Content{
				JSONLD: []string{`{"@type":"JobPosting"}`},
			},
			want: true,
		},

		// --- Weak evidence admits only in agreement ---------------------------
		{
			name:    "apply affordance alone does not admit",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Jetzt bewerben"},
			want:    false,
		},
		{
			name:    "posting vocabulary alone does not admit",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Ihre Aufgaben ... Ihr Profil ..."},
			want:    false,
		},
		{
			// One group is not "dense": a single section heading fires on hub cards and
			// careers landing copy too.
			name:    "apply affordance plus only one vocabulary group does not admit",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Jetzt bewerben ... Ihre Aufgaben ..."},
			want:    false,
		},
		{
			name:    "the weak pair admits, German",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Jetzt bewerben ... Ihre Aufgaben ... Ihr Profil ..."},
			want:    true,
		},
		{
			name:    "the weak pair admits, English",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Apply now ... Responsibilities ... Requirements ..."},
			want:    true,
		},
		{
			// Disjointness guard. "Ihre Bewerbung" reads like an offer to apply but is a
			// VOCABULARY phrase, not an apply phrase. If the two phrase sets ever
			// overlapped, this page would satisfy both marks off one phrase and the
			// tier's agreement requirement would silently collapse into a disjunction.
			name:    "a vocabulary phrase that reads like an apply offer does not count as one",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Ihre Bewerbung"},
			want:    false,
		},
		{
			// The same guard with the vocabulary mark actually SATISFIED (two distinct
			// groups): still false, because no apply phrase is present. This is the case
			// that would go green if an apply phrase leaked into a vocabulary group.
			name:    "two vocabulary groups including the apply-sounding one still need a real apply phrase",
			url:     noEvidenceURL,
			content: &crawler.Content{MainContent: "Ihre Bewerbung ... Ansprechpartner ... Ihre Aufgaben ..."},
			want:    false,
		},

		// --- No evidence -------------------------------------------------------
		{
			// Token matching, never substrings. A substring test would read the German
			// "bestellen" (to order) as the job word "stellen" and admit a webshop's
			// order pages -- and there are far more of those on the web than postings.
			name:    "a German word merely CONTAINING a job word does not admit (bestellen)",
			url:     "https://acme.de/bestellen/artikel-123",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "a word merely containing a job word does not admit (disposition)",
			url:     "https://acme.com/disposition/plan-7",
			content: &crawler.Content{},
			want:    false,
		},
		{
			// The compound segment must be FOLLOWED by a further segment. Terminal, it
			// names the index itself, not a role on it -- the same rule the shared
			// predicate keeps for its bare segments.
			name:    "a terminal compound job segment is the index, not a posting",
			url:     "https://acme.de/jobs-karriere",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "signal-less page does not admit",
			url:     noEvidenceURL,
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "single-segment slug with no evidence does not admit",
			url:     "https://acme.com/senior-go-engineer",
			content: &crawler.Content{},
			want:    false,
		},

		// --- Reject-rung precedence -------------------------------------------
		// Every reject rung still resolves BEFORE the evidence test. The rung-5/6/7
		// cases are the load-bearing ones: their URL alone would admit, so a pass
		// proves ordering rather than coincidence.
		{
			name:    "rung 2: an ATS posting stays exempt, evidence is never consulted",
			url:     "https://job-boards.greenhouse.io/acme/jobs/123",
			content: &crawler.Content{},
			want:    true,
		},
		{
			name: "rung 2b: a bare job-word-host root is still a hub, however it reads",
			url:  "https://karriere.acme.de/",
			content: &crawler.Content{
				MainContent: "Jetzt bewerben ... Ihre Aufgaben ... Ihr Profil ...",
			},
			want: false,
		},
		{
			name: "rung 2c: a terminal openings segment is still an index, however it reads",
			url:  "https://acme.com/careers/openings",
			content: &crawler.Content{
				MainContent: "Jetzt bewerben ... Ihre Aufgaben ... Ihr Profil ...",
			},
			want: false,
		},
		{
			name: "rung 3: a reject path beats a URL that would otherwise admit",
			url:  "https://acme.com/blog/jobs/senior-engineer",
			content: &crawler.Content{
				MainContent: "Apply now ... Responsibilities ... Requirements ...",
				JSONLD:      []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
			},
			want: false,
		},
		{
			name: "rung 5: an ATS embed beats a URL that would otherwise admit",
			url:  "https://acme.com/careers/senior-engineer",
			content: &crawler.Content{
				Embeds: []crawler.Embed{{Src: "https://acme.jobs.personio.de/search", IsFrame: true}},
			},
			want: false,
		},
		{
			name: "rung 6: a JSON-LD openings index beats a URL that would otherwise admit",
			url:  "https://acme.com/jobs/all",
			content: &crawler.Content{
				MainContent: "Apply now ... Responsibilities ... Requirements ...",
				JSONLD: []string{`{"@type":"ItemList","itemListElement":[
					{"@type":"ListItem","item":{"@type":"JobPosting","title":"Engineer"}},
					{"@type":"ListItem","item":{"@type":"JobPosting","title":"Designer"}}
				]}`},
			},
			want: false,
		},
		{
			name: "rung 7: job-link saturation beats a URL that would otherwise admit",
			url:  "https://acme.com/careers/senior-engineer",
			content: &crawler.Content{
				MainContent: "Apply now ... Responsibilities ... Requirements ...",
				URLs: []string{
					"/jobs/1", "/jobs/2", "/jobs/3", "/jobs/4", "/jobs/5",
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pagegate.ShouldExtract(newURL(t, tt.url), tt.content, requirePositiveEvidence())
			if got != tt.want {
				t.Errorf("ShouldExtract(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

// TestShouldExtract_PositiveEvidenceAdmitsEverySharedPostingShape locks the
// superset relation the rung is built on: every URL shape the SHARED posting-path
// predicate calls a posting is one this rung also admits. The new word list is
// allowed to be wider than jobPathSegments; it must never be narrower, or turning
// the rung on in #264 would false-drop postings the gate accepts today.
func TestShouldExtract_PositiveEvidenceAdmitsEverySharedPostingShape(t *testing.T) {
	// The shared predicate's job words, mirrored here because the test is external
	// and cannot read the unexported list. A word added there without being added to
	// extractPostingWords is exactly the drift this test exists to catch -- add it
	// here too and watch the case go red.
	sharedPostingWords := []string{
		"jobs", "job", "careers", "career", "vacancies", "vacancy",
		"positions", "position", "openings", "opening", "stellenangebote",
		"stellen", "stelle",
	}
	for _, word := range sharedPostingWords {
		t.Run(word, func(t *testing.T) {
			raw := "https://acme.com/" + word + "/senior-engineer"
			if !pagegate.ShouldExtract(newURL(t, raw), &crawler.Content{}, requirePositiveEvidence()) {
				t.Errorf("ShouldExtract(%q) = false, want true: the Positive Evidence rung must admit every shape the shared posting-path predicate calls a posting", raw)
			}
		})
	}
}

// TestShouldExtract_PositiveEvidenceIsOffByDefault is the machine-visible
// "default off" (#258 builds the rung, #264 flips it). Pages that the rung would
// drop must still extract under the committed default, which is what makes the
// flag a kill switch rather than a redeploy.
func TestShouldExtract_PositiveEvidenceIsOffByDefault(t *testing.T) {
	for _, raw := range []string{noEvidenceURL, "https://acme.com/senior-go-engineer"} {
		t.Run(raw, func(t *testing.T) {
			if !pagegate.ShouldExtract(newURL(t, raw), &crawler.Content{}, crawler.DefaultLLMGateConfig()) {
				t.Errorf("ShouldExtract(%q) = false under DefaultLLMGateConfig, want true: the Positive Evidence rung must ship off", raw)
			}
		})
	}
}

// TestPositiveEvidenceDoesNotWidenTheSharedPostingPath is the isolation guard.
// Two notions of "posting URL" live in the pagegate package on purpose: the
// Positive Evidence rung's own wider reading, and IsPostingPath, which the
// Discovery Gate's career-page veto and the Catalog Doctor's REMOVAL PLANNER
// replay over stored Catalog rows. The German shapes below are the whole point of
// the wider reading -- and each must stay invisible to the shared predicate.
//
// This test goes red the moment someone "cleans up" the duplication by widening
// jobPathSegments. That is not a cleanup: the Catalog Doctor would then
// retroactively hard-delete the Career Pages these shapes describe, and job-link
// saturation counting would shift on both gates.
func TestPositiveEvidenceDoesNotWidenTheSharedPostingPath(t *testing.T) {
	for _, raw := range []string{
		"https://acme.de/jobs-karriere/senior-entwickler",
		"https://acme.de/stellenangebote_unternehmen/bosch/senior-entwickler",
		"https://karriere.acme.de/senior-entwickler",
	} {
		t.Run(raw, func(t *testing.T) {
			u := newURL(t, raw)
			if pagegate.IsPostingPath(u) {
				t.Errorf("IsPostingPath(%q) = true, want false: the shared predicate must NOT have been widened to cover the extract rung's German long tail", raw)
			}
			if !pagegate.ShouldExtract(u, &crawler.Content{}, requirePositiveEvidence()) {
				t.Errorf("ShouldExtract(%q) = false, want true: the Positive Evidence rung reads its own wider posting-URL predicate", raw)
			}
		})
	}
}
