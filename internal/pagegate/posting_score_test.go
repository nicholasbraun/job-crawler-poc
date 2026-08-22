package pagegate_test

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// hasSignal reports whether sigs carries the named Score Signal.
func hasSignal(sigs []string, name string) bool {
	return slices.Contains(sigs, name)
}

// countSignal counts how often sigs carries the named Score Signal. A signal is a
// presence indicator, so the only correct answers are 0 and 1; the count exists so a
// test can say that out loud.
func countSignal(sigs []string, name string) int {
	n := 0
	for _, sig := range sigs {
		if sig == name {
			n++
		}
	}
	return n
}

// groupPhrases are one phrase from each of the five posting sections
// postingVocabularyGroups holds, written out literally because the list is
// unexported -- the same thing positive_evidence_test.go already does. A body built
// from the first k of them carries exactly k distinct sections.
var groupPhrases = []string{
	"Ihre Aufgaben", "Ihr Profil", "Wir bieten", "Vollzeit", "Ansprechpartner",
}

// bodyWithGroups returns a body carrying exactly the first n posting sections.
func bodyWithGroups(n int) string {
	return strings.Join(groupPhrases[:n], ". ")
}

// jobLinkContent returns content linking n distinct same-host Job Listing URLs.
func jobLinkContent(n int) *crawler.Content {
	urls := make([]string, 0, n)
	for i := range n {
		urls = append(urls, fmt.Sprintf("https://acme.com/jobs/%d", i))
	}
	return &crawler.Content{URLs: urls}
}

func TestSignalsGradeTheStructuralSignals(t *testing.T) {
	u := newURL(t, noEvidenceURL)

	t.Run("the posting-section count enters across its full range", func(t *testing.T) {
		for groups := range len(groupPhrases) + 1 {
			t.Run(fmt.Sprintf("%d groups", groups), func(t *testing.T) {
				sigs := pagegate.Signals(u, &crawler.Content{MainContent: bodyWithGroups(groups)})
				for level := 1; level <= len(groupPhrases); level++ {
					name := fmt.Sprintf("sig:vocab_groups_ge_%d", level)
					want := level <= groups
					if got := hasSignal(sigs, name); got != want {
						t.Errorf("%s = %v, want %v (body carries %d sections)", name, got, want, groups)
					}
				}
			})
		}
	})

	t.Run("the same-host job-link count is bucketed and saturates", func(t *testing.T) {
		for _, links := range []int{0, 1, 3, 4, 7} {
			t.Run(fmt.Sprintf("%d links", links), func(t *testing.T) {
				sigs := pagegate.Signals(u, jobLinkContent(links))
				for level := 1; level <= 4; level++ {
					name := fmt.Sprintf("sig:job_links_ge_%d", level)
					want := level <= links
					if got := hasSignal(sigs, name); got != want {
						t.Errorf("%s = %v, want %v (%d links)", name, got, want, links)
					}
				}
				// The table stops at 4 because rung 7 rejects at 5: a page carrying
				// more can never reach the score, so a fifth level would be dead weight.
				if hasSignal(sigs, "sig:job_links_ge_5") {
					t.Error("sig:job_links_ge_5 exists; the bucket must saturate at the last table level")
				}
			})
		}
	})

	t.Run("a cross-host link and a repeated link change nothing", func(t *testing.T) {
		want := pagegate.Signals(u, jobLinkContent(2))
		content := jobLinkContent(2)
		content.URLs = append(content.URLs,
			"https://acme.com/jobs/1",        // the same posting again
			"https://other.example/jobs/999", // another host's board
		)
		if got := pagegate.Signals(u, content); !slices.Equal(got, want) {
			t.Errorf("signals moved: got %v, want %v", got, want)
		}
	})

	t.Run("body length is bucketed on a doubling scale", func(t *testing.T) {
		edges := []int{256, 512, 1024, 2048, 4096, 8192, 16384}
		for _, size := range []int{100, 300, 5000, 20000} {
			t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
				sigs := pagegate.Signals(u, &crawler.Content{MainContent: strings.Repeat("a", size)})
				for _, edge := range edges {
					name := fmt.Sprintf("sig:body_bytes_ge_%d", edge)
					want := size >= edge
					if got := hasSignal(sigs, name); got != want {
						t.Errorf("%s = %v, want %v (%d-byte body)", name, got, want, size)
					}
				}
			})
		}
	})

	t.Run("a lone structured-data posting is a signal, an openings index is not", func(t *testing.T) {
		tests := []struct {
			name   string
			jsonld []string
			want   bool
		}{
			{"one JobPosting node", []string{`{"@type":"JobPosting","title":"Senior Engineer"}`}, true},
			{"two JobPosting nodes", []string{`{"@type":"JobPosting","title":"Engineer"}`, `{"@type":"JobPosting","title":"Designer"}`}, false},
			{"no structured data", nil, false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				sigs := pagegate.Signals(u, &crawler.Content{JSONLD: tt.jsonld})
				if got := hasSignal(sigs, "sig:lone_posting"); got != tt.want {
					t.Errorf("sig:lone_posting = %v, want %v", got, tt.want)
				}
			})
		}
	})
}

// TestSignalsReadTheFlattenedText holds the ADR-0046 property for the score: the
// Structural Rendering kill switch changes what the parser STORES, and it must not
// change what the score reads. A rendering and the flat text it derives to must
// produce the identical Score Signals, and no marker the renderer synthesized may
// enter the Score Vocabulary as a word.
func TestSignalsReadTheFlattenedText(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	rendered := "#\vIhre Aufgaben\n-\vVollzeit\n[img: Acme Logo]\n[button: Senden]\n[Apply now](/apply)"
	flat := "Ihre Aufgaben Vollzeit Senden Apply now"

	renderedSigs := pagegate.Signals(u, &crawler.Content{MainContent: rendered})
	flatSigs := pagegate.Signals(u, &crawler.Content{MainContent: flat})
	if !slices.Equal(renderedSigs, flatSigs) {
		t.Fatalf("the rendering and its Flattened Text disagree:\n rendered %v\n flat     %v", renderedSigs, flatSigs)
	}

	for _, marker := range []string{"body:img", "body:button", "body:acme", "body:logo"} {
		if hasSignal(renderedSigs, marker) {
			t.Errorf("%s is a word of the rendering's markup, not of the page", marker)
		}
	}
	// The button's own text IS page text and survives the flattening, so it is a word.
	if !hasSignal(renderedSigs, "body:senden") {
		t.Error("body:senden missing: a button's own text is page text")
	}
}

// TestSignalsTokenizeUnicodeWordsWhole covers the tokenizer's unicode-awareness
// specifically: roughly half the Score Vocabulary's signal is German, and an ASCII
// split predicate would shatter every word carrying an umlaut into fragments that
// mean nothing.
func TestSignalsTokenizeUnicodeWordsWhole(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	german := "Ihre Aufgaben: Qualitätssicherung für Führungskräfte an der Straße"

	t.Run("in the body", func(t *testing.T) {
		sigs := pagegate.Signals(u, &crawler.Content{MainContent: german})
		for _, want := range []string{"body:qualitätssicherung", "body:führungskräfte", "body:für", "body:straße"} {
			if !hasSignal(sigs, want) {
				t.Errorf("%s missing: the word did not tokenize whole", want)
			}
		}
		for _, unwanted := range []string{"body:qualit", "body:tssicherung", "body:f", "body:hrungskr", "body:stra"} {
			if hasSignal(sigs, unwanted) {
				t.Errorf("%s present: the tokenizer split on a non-ASCII letter", unwanted)
			}
		}
	})

	t.Run("in the Title", func(t *testing.T) {
		sigs := pagegate.Signals(u, &crawler.Content{Title: german})
		if !hasSignal(sigs, "title:führungskräfte") {
			t.Error("title:führungskräfte missing: the word did not tokenize whole")
		}
		if hasSignal(sigs, "title:hrungskr") {
			t.Error("title:hrungskr present: the tokenizer split on a non-ASCII letter")
		}
	})
}

func TestSignalsSeparateTitleAndBodyWords(t *testing.T) {
	u := newURL(t, noEvidenceURL)

	tests := []struct {
		name      string
		content   *crawler.Content
		wantTitle bool
		wantBody  bool
	}{
		{"only in the Title", &crawler.Content{Title: "Engineer"}, true, false},
		{"only in the body", &crawler.Content{MainContent: "Engineer"}, false, true},
		{"in both", &crawler.Content{Title: "Engineer", MainContent: "Engineer"}, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sigs := pagegate.Signals(u, tt.content)
			if got := hasSignal(sigs, "title:engineer"); got != tt.wantTitle {
				t.Errorf("title:engineer = %v, want %v", got, tt.wantTitle)
			}
			if got := hasSignal(sigs, "body:engineer"); got != tt.wantBody {
				t.Errorf("body:engineer = %v, want %v", got, tt.wantBody)
			}
		})
	}
}

// TestSignalsRecordPresenceNotFrequency holds ADR-0044's measurement: what
// distinguishes a posting is the range of its sections, not how often it repeats a
// word. A repeated word is one signal, and no name may appear twice in the slice at
// all -- Score sums it, so a duplicate would silently double a weight.
func TestSignalsRecordPresenceNotFrequency(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	sigs := pagegate.Signals(u, &crawler.Content{
		Title:       "Aufgaben Aufgaben",
		MainContent: "Aufgaben aufgaben AUFGABEN Aufgaben aufgaben",
	})

	if got := countSignal(sigs, "body:aufgaben"); got != 1 {
		t.Errorf("body:aufgaben appears %d times, want 1", got)
	}
	seen := map[string]struct{}{}
	for _, sig := range sigs {
		if _, dup := seen[sig]; dup {
			t.Errorf("%s appears more than once", sig)
		}
		seen[sig] = struct{}{}
	}
}

// TestSignalsCapTheBodyScan proves the cap through the exported function rather than
// by reading the constant: a word past the cap earns no signal, the same word inside
// it does. The gate runs on every walked page and the largest captured body is
// ~757 KB, so the bound is what keeps the scan's cost flat.
func TestSignalsCapTheBodyScan(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	const sentinel = "zzsentinelzz"
	const early = "zzearlyzz"
	// Comfortably past 64 KiB, with a distinctive word planted near the front.
	filler := early + " " + strings.Repeat("lorem ipsum dolor sit amet ", 3000)

	t.Run("a word past the cap is not read", func(t *testing.T) {
		sigs := pagegate.Signals(u, &crawler.Content{MainContent: filler + sentinel})
		if hasSignal(sigs, "body:"+sentinel) {
			t.Error("body:" + sentinel + " present: the body scan is not capped")
		}
		if !hasSignal(sigs, "body:"+early) {
			t.Error("body:" + early + " missing: the cap ate text before it")
		}
	})

	t.Run("the same word inside the cap is read", func(t *testing.T) {
		sigs := pagegate.Signals(u, &crawler.Content{MainContent: sentinel + " " + filler})
		if !hasSignal(sigs, "body:"+sentinel) {
			t.Error("body:" + sentinel + " missing: the word sits well inside the cap")
		}
	})
}

// TestSignalsAreStableAcrossCalls holds the determinism Score's float sum and the
// trainer both rest on: the words come out of a map, so without the sort the order
// would follow Go's randomized map iteration.
func TestSignalsAreStableAcrossCalls(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	content := &crawler.Content{
		Title:       "Senior Engineer (m/w/d) gesucht",
		MainContent: bodyWithGroups(3) + " Wir freuen uns auf Ihre Bewerbung in Berlin.",
		URLs:        []string{"https://acme.com/jobs/1", "https://acme.com/jobs/2"},
		JSONLD:      []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
	}
	first := pagegate.Signals(u, content)
	for i := range 5 {
		if got := pagegate.Signals(u, content); !slices.Equal(got, first) {
			t.Fatalf("call %d returned a different slice:\n got  %v\n want %v", i+2, got, first)
		}
	}
}

// TestVocabularyGradeAgreesWithThePositiveEvidenceStrongTier is the standing check
// that the score reads the gate's OWN posting-section detector rather than a copy of
// it. On a page whose only possible mark is its posting sections, the fourth
// vocabulary grade and the Positive Evidence verdict are the same statement; a
// reimplemented counter that drifted by one section would show up here.
func TestVocabularyGradeAgreesWithThePositiveEvidenceStrongTier(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	for groups := range len(groupPhrases) + 1 {
		t.Run(fmt.Sprintf("%d groups", groups), func(t *testing.T) {
			content := &crawler.Content{MainContent: bodyWithGroups(groups)}
			strong := hasSignal(pagegate.Signals(u, content), "sig:vocab_groups_ge_4")
			extract := pagegate.ShouldExtract(u, content, requirePositiveEvidence())
			if strong != extract {
				t.Errorf("sig:vocab_groups_ge_4 = %v but ShouldExtract = %v; the score and rung 8 disagree about the same page", strong, extract)
			}
		})
	}
}

// TestJobLinkGradeReadsTheSameCountAsTheSaturationRung is the same standing check for
// the same-host Job Listing link count: the grade table's ceiling is one below the
// shipped saturation count, so the deepest grade fires exactly on the deepest page
// rung 7 still lets through.
func TestJobLinkGradeReadsTheSameCountAsTheSaturationRung(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	saturation := crawler.DefaultLLMGateConfig().ExtractJobLinkSaturationCount
	for links := range 7 {
		t.Run(fmt.Sprintf("%d links", links), func(t *testing.T) {
			content := jobLinkContent(links)
			_, rung := pagegate.ExtractDecision(u, content, requirePositiveEvidence())
			wantSaturated := links >= saturation
			if got := rung == pagegate.RungJobLinkSaturation; got != wantSaturated {
				t.Errorf("rung = %q, want saturation = %v", rung, wantSaturated)
			}
			if got := hasSignal(pagegate.Signals(u, content), "sig:job_links_ge_4"); got != (links >= 4) {
				t.Errorf("sig:job_links_ge_4 = %v, want %v", got, links >= 4)
			}
		})
	}
}

// TestScoreIsAFunctionOfTheScoreSignalsAlone: two pages built differently but
// carrying the same Score Signals must score the same. It is the property that keeps
// the score unable to depend on anything it has not declared -- and, once the table
// is fitted, the reason a Structural Rendering deploy cannot move a verdict.
func TestScoreIsAFunctionOfTheScoreSignalsAlone(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	tests := []struct {
		name string
		a, b *crawler.Content
	}{
		{
			name: "a Structural Rendering and its Flattened Text",
			a:    &crawler.Content{MainContent: "#\vIhre Aufgaben\n-\vVollzeit\n[button: Senden]"},
			b:    &crawler.Content{MainContent: "Ihre Aufgaben Vollzeit Senden"},
		},
		{
			name: "the same words in a different order",
			a:    &crawler.Content{MainContent: "Ihre Aufgaben. Wir bieten."},
			b:    &crawler.Content{MainContent: "Wir bieten. Ihre Aufgaben."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, want := pagegate.Signals(u, tt.a), pagegate.Signals(u, tt.b); !slices.Equal(got, want) {
				t.Fatalf("the two pages carry different Score Signals:\n a %v\n b %v", got, want)
			}
			if got, want := pagegate.Score(u, tt.a), pagegate.Score(u, tt.b); got != want {
				t.Errorf("equal Score Signals scored differently: %v vs %v", got, want)
			}
		})
	}

	t.Run("the score is stable across calls", func(t *testing.T) {
		content := &crawler.Content{Title: "Senior Engineer", MainContent: bodyWithGroups(4)}
		first := pagegate.Score(u, content)
		for i := range 5 {
			if got := pagegate.Score(u, content); got != first {
				t.Fatalf("call %d scored %v, want %v", i+2, got, first)
			}
		}
	})
}

// scoreSpread is the set of pages every whole-score property below is asserted over:
// an empty page, a rich posting, a link-dense hub just under rung 7's cut, and a body
// far past the vocabulary scan cap.
func scoreSpread(t *testing.T) map[string]*crawler.Content {
	t.Helper()
	hub := jobLinkContent(4)
	hub.Title = "Open positions"
	hub.MainContent = "Wir bieten. Ansprechpartner."
	return map[string]*crawler.Content{
		"an empty page": {},
		"a rich posting": {
			Title:       "Senior Engineer (m/w/d) gesucht",
			MainContent: bodyWithGroups(5) + " Jetzt bewerben in Berlin.",
			JSONLD:      []string{`{"@type":"JobPosting","title":"Senior Engineer"}`},
		},
		"a link-dense hub": hub,
		"a 100 KB body":    {MainContent: strings.Repeat("lorem ipsum dolor sit amet ", 4000)},
	}
}

// TestScoreIsBounded holds the range VetoThreshold and the live histogram are both
// expressed in. The logistic squash is what buys it, and it is why the Posting Score
// is a number a human can read rather than an unbounded sum.
func TestScoreIsBounded(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	for name, content := range scoreSpread(t) {
		t.Run(name, func(t *testing.T) {
			score := pagegate.Score(u, content)
			if math.IsNaN(score) {
				t.Fatal("score is NaN")
			}
			if score < 0 || score > 1 {
				t.Errorf("score = %v, want it within [0,1]", score)
			}
		})
	}
}

// TestSeededWeightsScoreEveryPageAlikeAndVetoNothing asserts the SEED, not the rule:
// while posting_score_weights_gen.go ships empty, no page carries a weighted Score
// Signal, so every page scores identically and no page can fall under the threshold.
// That is what makes this ticket incapable of moving a gate verdict.
//
// It holds by construction of the seed and is deliberately temporary: the training
// run that commits fitted weights DELETES it, and from then on the regenerability
// guard and TestExtractGoldSetFalseDropGuard hold this ground instead.
func TestSeededWeightsScoreEveryPageAlikeAndVetoNothing(t *testing.T) {
	u := newURL(t, noEvidenceURL)
	var want float64
	first := true
	for name, content := range scoreSpread(t) {
		score := pagegate.Score(u, content)
		if first {
			want, first = score, false
		}
		if score != want {
			t.Errorf("%s scored %v, want %v: the seeded table must score every page alike", name, score, want)
		}
		if score < pagegate.VetoThreshold {
			t.Errorf("%s scored %v, below VetoThreshold %v: the seeded artifact must veto nothing", name, score, pagegate.VetoThreshold)
		}
	}
}
