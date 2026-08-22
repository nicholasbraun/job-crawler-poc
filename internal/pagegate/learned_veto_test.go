// This file is the Learned Veto's table (ADR-0049), the way positive_evidence_test.go
// is Positive Evidence's. Every case is stated through ExtractGate, ExtractDecision or
// ShouldExtract and never by reaching into the Posting Score: the rung's contract is a
// verdict, an attribution and -- for the pages it judged -- the score it judged them
// by, and a test that asserted the arithmetic instead would go red on a retrain that
// changed nothing anyone can observe. Where a case does name the score it names it as
// pagegate.Score of the same page, never as a literal, so it holds the reading equal to
// the computation rather than pinning either to a number a retrain will move.

package pagegate_test

import (
	"fmt"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// extractSwitches returns DefaultLLMGateConfig with the two extract kill switches set
// explicitly. Both are named at every call site because the composition of the two is
// the thing under test, and a default silently supplying one of them would make the
// table read as a claim about a configuration it never ran.
func extractSwitches(positiveEvidence, learnedVeto bool) crawler.LLMGateConfig {
	cfg := crawler.DefaultLLMGateConfig()
	cfg.RequirePositiveEvidence = positiveEvidence
	cfg.LearnedVeto = learnedVeto
	return cfg
}

// postingURLOnlyURL is a posting-shaped URL that the Positive Evidence rung admits on
// the URL alone. With empty content it emits NO Score Signals at all, so its Posting
// Score is the bare fitted intercept -- the weakest thing rung 8 accepts, and the
// least retrain-sensitive low fixture available.
const postingURLOnlyURL = "https://acme.com/jobs/senior-engineer"

// atsPostingURL is an ATS posting. It is exempted deterministically several rungs
// before the veto (ADR-0022's Fetch lane owns these boards), and its score is BELOW
// the threshold -- which is what makes the exemption case below load-bearing.
const atsPostingURL = "https://job-boards.greenhouse.io/acme/jobs/123"

// richPostingContent is a page carrying five posting sections, an apply affordance and
// a role designation: the shape the Posting Score was fitted to rank highest. It rides
// noEvidenceURL so what admits it is the content, not the URL.
func richPostingContent() *crawler.Content {
	return &crawler.Content{
		Title: "Senior Engineer (m/w/d) gesucht",
		MainContent: "Ihre Aufgaben. Ihr Profil. Wir bieten. Vollzeit. Ansprechpartner. " +
			"Jetzt bewerben. Wir freuen uns auf Ihre Bewerbung. Vergütung nach Tarif. Arbeiten bei uns.",
	}
}

// requireBelowThreshold fails the test unless the page is one the Learned Veto drops.
// A retrain that moves the weights is expected to break these preconditions before it
// breaks any verdict, which is the point: the message names the repair.
func requireBelowThreshold(t *testing.T, u crawler.URL, content *crawler.Content) {
	t.Helper()
	if got := pagegate.Score(u, content); got >= pagegate.VetoThreshold {
		t.Fatalf("fixture scores %.6f, at or above VetoThreshold %.6f: this case needs a page the veto drops. "+
			"A retrain moved the weights -- pick a weaker fixture; never move the threshold to fit a test.", got, pagegate.VetoThreshold)
	}
}

// requireAtOrAboveThreshold is requireBelowThreshold's mirror, for the cases whose
// subject is a page the veto must LET THROUGH.
func requireAtOrAboveThreshold(t *testing.T, u crawler.URL, content *crawler.Content) {
	t.Helper()
	if got := pagegate.Score(u, content); got < pagegate.VetoThreshold {
		t.Fatalf("fixture scores %.6f, below VetoThreshold %.6f: this case needs a page the veto keeps. "+
			"A retrain moved the weights -- pick a stronger fixture; never move the threshold to fit a test.", got, pagegate.VetoThreshold)
	}
}

// TestShouldExtract_LearnedVetoWithholdsTheWeakestPositiveEvidenceAccepts is the rung
// itself: of the two pages rung 8 admits today, the one the fit ranks low loses its
// extractor call and the one it ranks high keeps it. Both directions are asserted,
// because a rung that vetoed everything would satisfy the first half alone.
func TestShouldExtract_LearnedVetoWithholdsTheWeakestPositiveEvidenceAccepts(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		content    *crawler.Content
		wantVetoed bool
	}{
		{
			name:       "a posting-shaped URL with nothing behind it is not worth the call",
			url:        postingURLOnlyURL,
			content:    &crawler.Content{},
			wantVetoed: true,
		},
		{
			name:       "a page carrying the sections of a real posting keeps its call",
			url:        noEvidenceURL,
			content:    richPostingContent(),
			wantVetoed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newURL(t, tt.url)
			if tt.wantVetoed {
				requireBelowThreshold(t, u, tt.content)
			} else {
				requireAtOrAboveThreshold(t, u, tt.content)
			}
			// The page must reach rung 9 at all: a case rung 8 already sheds would pass
			// the veto assertion for the wrong reason.
			if extract, rung := pagegate.ExtractDecision(u, tt.content, extractSwitches(true, false)); !extract {
				t.Fatalf("ExtractDecision(%q) with the veto OFF = false at rung %q; this case needs a page the Positive Evidence rung admits", tt.url, rung)
			}

			extract, rung := pagegate.ExtractDecision(u, tt.content, extractSwitches(true, true))
			if tt.wantVetoed {
				if extract || rung != pagegate.RungLearnedVeto {
					t.Errorf("ExtractDecision(%q) = (%v, %q), want (false, %q): the Learned Veto must withhold this call",
						tt.url, extract, rung, pagegate.RungLearnedVeto)
				}
				return
			}
			if !extract || rung != pagegate.RungNone {
				t.Errorf("ExtractDecision(%q) = (%v, %q), want (true, RungNone): a page at or above the threshold reaches the extractor",
					tt.url, extract, rung)
			}
		})
	}
}

// TestShouldExtract_LearnedVetoIsOffByDefault is the machine-visible "ships off"
// (ADR-0049): the configuration the crawler actually ships must still pay for the
// weakest rung-8 accept. It is the case that goes red if somebody flips the default
// instead of running the rollout the ADR describes -- a capture window, offline
// scoring, a blind confirmation of the pages the veto would drop, those labels
// committed, and only then the flip.
func TestShouldExtract_LearnedVetoIsOffByDefault(t *testing.T) {
	u := newURL(t, postingURLOnlyURL)
	requireBelowThreshold(t, u, &crawler.Content{})
	extract, rung := pagegate.ExtractDecision(u, &crawler.Content{}, crawler.DefaultLLMGateConfig())
	if !extract || rung != pagegate.RungNone {
		t.Errorf("ExtractDecision(%q) = (%v, %q) under DefaultLLMGateConfig, want (true, RungNone): the Learned Veto must ship OFF",
			postingURLOnlyURL, extract, rung)
	}
}

// TestLearnedVetoKillSwitchRestoresTheUnconditionalPositiveEvidenceAccept asserts the
// switch as BEHAVIOUR rather than as a struct field, which is the only form in which
// it is a kill switch. EXTRACT_LEARNED_VETO unset must restore rung 8's unconditional
// accept for the very page the veto drops, with no other rung consulted differently.
func TestLearnedVetoKillSwitchRestoresTheUnconditionalPositiveEvidenceAccept(t *testing.T) {
	u := newURL(t, postingURLOnlyURL)
	requireBelowThreshold(t, u, &crawler.Content{})
	extract, rung := pagegate.ExtractDecision(u, &crawler.Content{}, extractSwitches(true, false))
	if !extract || rung != pagegate.RungNone {
		t.Errorf("ExtractDecision(%q) = (%v, %q) with the veto cleared, want (true, RungNone): "+
			"the kill switch must restore the unconditional Positive Evidence accept without a deploy",
			postingURLOnlyURL, extract, rung)
	}
}

// TestLearnedVetoRunsLastAndNeverScoresAnATSPosting pins the rung's PLACE in the
// ladder, which ADR-0049 argues is the only place it can sit: it judges only the pages
// the gate was about to pay for. Every case below is a page that resolves before rung
// 9 and also scores below the threshold, so an attribution of learned_veto on any of
// them would mean the rung had moved up the ladder.
func TestLearnedVetoRunsLastAndNeverScoresAnATSPosting(t *testing.T) {
	saturated := &crawler.Content{URLs: []string{}}
	for i := 0; i < 10; i++ {
		saturated.URLs = append(saturated.URLs, fmt.Sprintf("https://acme.com/jobs/%d", i))
	}

	tests := []struct {
		name        string
		url         string
		content     *crawler.Content
		wantExtract bool
		wantRung    pagegate.ExtractRung
	}{
		{"a reject rung still resolves first", "https://acme.com/blog/hello", &crawler.Content{}, false, pagegate.RungRejectPath},
		{"job-link saturation still resolves first", noEvidenceURL, saturated, false, pagegate.RungJobLinkSaturation},
		{"a page with no Positive Evidence is still rung 8's", noEvidenceURL, &crawler.Content{}, false, pagegate.RungPositiveEvidence},
		// The ATS exemption is the load-bearing one. This posting scores below the
		// threshold like the rest, so it is only kept by the deterministic exemption two
		// rungs into the ladder -- if the veto ever ran before it, this case fails and
		// the ATS Fetch lane's postings would start losing extractor calls to a fit that
		// never saw one (zero of the Gold Set's 457 rows take this exemption).
		{"an ATS posting is never scored", atsPostingURL, &crawler.Content{}, true, pagegate.RungNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newURL(t, tt.url)
			requireBelowThreshold(t, u, tt.content)
			extract, rung := pagegate.ExtractDecision(u, tt.content, extractSwitches(true, true))
			if extract != tt.wantExtract || rung != tt.wantRung {
				t.Errorf("ExtractDecision(%q) = (%v, %q), want (%v, %q)", tt.url, extract, rung, tt.wantExtract, tt.wantRung)
			}
		})
	}
}

// TestTheTwoExtractSwitchesComposeIndependently pins what each dial restores, which is
// the decision this rung had to make. The veto is a FLAT rung rather than one nested
// under Positive Evidence: each switch restores its own rung's prior behaviour, so
// pulling EXTRACT_LEARNED_VETO gives back the unconditional rung-8 accept, pulling
// EXTRACT_REQUIRE_POSITIVE_EVIDENCE gives back the blanket accept, and pulling both
// gives back the pre-#257 gate -- three distinct configurations, which the acceptance
// criterion's "pulling both" clause only says something about if they really differ.
//
// The two (Positive Evidence off, veto on) cells are a configuration nobody ships: the
// weights were fitted on rung-8 accepts alone, so run there the veto judges a
// population it has never seen. They are in the table because that is what the code
// does, and a reader deciding whether to nest the rung deserves to see it.
func TestTheTwoExtractSwitchesComposeIndependently(t *testing.T) {
	tests := []struct {
		name             string
		url              string
		positiveEvidence bool
		learnedVeto      bool
		wantExtract      bool
		wantRung         pagegate.ExtractRung
	}{
		{"rung-8 accept, both dials as shipped today", postingURLOnlyURL, true, false, true, pagegate.RungNone},
		{"rung-8 accept, the veto on: the call is withheld", postingURLOnlyURL, true, true, false, pagegate.RungLearnedVeto},
		{"rung-8 accept, Positive Evidence off: the pre-#257 blanket accept", postingURLOnlyURL, false, false, true, pagegate.RungNone},
		{"rung-8 accept, Positive Evidence off and the veto on: the veto still judges it", postingURLOnlyURL, false, true, false, pagegate.RungLearnedVeto},
		{"no evidence, both dials as shipped today", noEvidenceURL, true, false, false, pagegate.RungPositiveEvidence},
		{"no evidence, the veto on: rung 8 still resolves it first", noEvidenceURL, true, true, false, pagegate.RungPositiveEvidence},
		{"no evidence, Positive Evidence off: the pre-#257 blanket accept", noEvidenceURL, false, false, true, pagegate.RungNone},
		{"no evidence, Positive Evidence off and the veto on: the veto takes what rung 8 no longer sheds", noEvidenceURL, false, true, false, pagegate.RungLearnedVeto},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newURL(t, tt.url)
			extract, rung := pagegate.ExtractDecision(u, &crawler.Content{}, extractSwitches(tt.positiveEvidence, tt.learnedVeto))
			if extract != tt.wantExtract || rung != tt.wantRung {
				t.Errorf("ExtractDecision(%q) with positiveEvidence=%v learnedVeto=%v = (%v, %q), want (%v, %q)",
					tt.url, tt.positiveEvidence, tt.learnedVeto, extract, rung, tt.wantExtract, tt.wantRung)
			}
		})
	}
}

// TestTheLearnedVetoCostsNothingWhenItIsOff holds the condition the rung ships under:
// off, the Posting Score is never computed at all.
//
// The score is a pure function, so the ONLY thing an external test can see about
// whether it ran is what it allocated. Signals builds a slice, a set and a sorted word
// list per page; the gate's own rungs allocate a fixed handful. So a decision with the
// veto off must allocate strictly less than the same decision on the same page with it
// on -- which is the early return, observed rather than asserted about the source.
//
// The page is a rung-9 SURVIVOR carrying a long body, so the on-side really does the
// whole Score Vocabulary scan; the point is that the score is computed, not that it
// vetoes.
func TestTheLearnedVetoCostsNothingWhenItIsOff(t *testing.T) {
	content := richPostingContent()
	var filler strings.Builder
	filler.WriteString(content.MainContent)
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&filler, " fuellwort%s", numberWord(i))
	}
	content.MainContent = filler.String()

	u := newURL(t, noEvidenceURL)
	requireAtOrAboveThreshold(t, u, content)

	off := testing.AllocsPerRun(50, func() {
		pagegate.ShouldExtract(u, content, extractSwitches(true, false))
	})
	on := testing.AllocsPerRun(50, func() {
		pagegate.ShouldExtract(u, content, extractSwitches(true, true))
	})
	t.Logf("allocations per decision: veto off %.0f, veto on %.0f", off, on)
	if off >= on {
		t.Errorf("veto off allocates %.0f and veto on %.0f, want strictly fewer with it off: "+
			"the switch must short-circuit BEFORE Score, so a crawl not using the rung pays nothing for it", off, on)
	}
}

// numberWord spells an index in letters, because the Score Vocabulary tokenizer treats
// digits as separators -- a numbered filler word would collapse to one repeated token
// and the body would scan far cheaper than a real page's.
func numberWord(n int) string {
	const digits = "abcdefghij"
	if n == 0 {
		return digits[:1]
	}
	var out []byte
	for n > 0 {
		out = append([]byte{digits[n%10]}, out...)
		n /= 10
	}
	return string(out)
}

// TestExtractGateReportsThePostingScoreItJudgedBy is the Scored contract, stated at the
// gate's own seam: the widest reading tells a caller not only what the rung decided but
// whether the rung RAN, and the two cannot be conflated.
//
// Scored is not derivable from Rung. On a reject it would be -- Rung is learned_veto
// exactly when the rung vetoed -- but RungNone covers three different accepts: the rung-2
// ATS exemption (never scored), every accept at all on a crawl with the veto off (never
// scored), and a rung-9 survivor (scored). Zero is a legitimate Posting Score, so without
// Scored the walk would file the un-judged accepts' zeros into the live distribution and
// report a stream that scores lowest of all.
func TestExtractGateReportsThePostingScoreItJudgedBy(t *testing.T) {
	tests := []struct {
		name             string
		url              string
		content          *crawler.Content
		positiveEvidence bool
		learnedVeto      bool
		wantExtract      bool
		wantRung         pagegate.ExtractRung
		wantScored       bool
	}{
		{
			name:             "a vetoed page carries the score that dropped it",
			url:              postingURLOnlyURL,
			content:          &crawler.Content{},
			positiveEvidence: true,
			learnedVeto:      true,
			wantExtract:      false,
			wantRung:         pagegate.RungLearnedVeto,
			wantScored:       true,
		},
		{
			name:             "a survivor carries the score that saved it",
			url:              noEvidenceURL,
			content:          richPostingContent(),
			positiveEvidence: true,
			learnedVeto:      true,
			wantExtract:      true,
			wantRung:         pagegate.RungNone,
			wantScored:       true,
		},
		{
			name:             "the veto off scores nothing at all",
			url:              postingURLOnlyURL,
			content:          &crawler.Content{},
			positiveEvidence: true,
			learnedVeto:      false,
			wantExtract:      true,
			wantRung:         pagegate.RungNone,
			wantScored:       false,
		},
		{
			name:             "a page an earlier rung rejected is never scored",
			url:              noEvidenceURL,
			content:          &crawler.Content{},
			positiveEvidence: true,
			learnedVeto:      true,
			wantExtract:      false,
			wantRung:         pagegate.RungPositiveEvidence,
			wantScored:       false,
		},
		{
			// Load-bearing: an ATS posting scores below the threshold, so only the
			// deterministic rung-2 exemption keeps it. Reporting it as Scored would feed the
			// ATS Fetch lane's postings into a distribution fitted on a population that
			// contains none of them (ADR-0049 counts zero such rows in the Gold Set).
			name:             "an ATS posting is never scored",
			url:              atsPostingURL,
			content:          &crawler.Content{},
			positiveEvidence: true,
			learnedVeto:      true,
			wantExtract:      true,
			wantRung:         pagegate.RungNone,
			wantScored:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u := newURL(t, tt.url)
			if tt.wantRung == pagegate.RungLearnedVeto {
				requireBelowThreshold(t, u, tt.content)
			} else if tt.wantScored {
				requireAtOrAboveThreshold(t, u, tt.content)
			}

			v := pagegate.ExtractGate(u, tt.content, extractSwitches(tt.positiveEvidence, tt.learnedVeto))
			if v.Extract != tt.wantExtract || v.Rung != tt.wantRung {
				t.Errorf("ExtractGate(%q) = (%v, %q), want (%v, %q)", tt.url, v.Extract, v.Rung, tt.wantExtract, tt.wantRung)
			}
			if v.Scored != tt.wantScored {
				t.Errorf("ExtractGate(%q).Scored = %v, want %v: the verdict must say whether the Learned Veto rung ran",
					tt.url, v.Scored, tt.wantScored)
			}
			if tt.wantScored {
				if want := pagegate.Score(u, tt.content); v.Score != want {
					t.Errorf("ExtractGate(%q).Score = %.6f, want %.6f: the verdict reports the score the rung judged by, not an approximation of it",
						tt.url, v.Score, want)
				}
				return
			}
			if v.Score != 0 {
				t.Errorf("ExtractGate(%q).Score = %.6f on an unscored page, want 0: a page the rung never judged must carry no score to record",
					tt.url, v.Score)
			}
		})
	}
}
