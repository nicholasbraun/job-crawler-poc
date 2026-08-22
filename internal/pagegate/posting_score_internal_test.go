// The invariants of the Posting Score that cannot be reached from outside the
// package: the shape of the two graded Score Signal tables against the detectors and
// the gate config they mirror, and the one body fold and one link count the gate's
// rungs share with the score. Everything else about the score is asserted through
// Signals and Score in the external test file.
package pagegate

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestVocabularyGroupGradesCoverEveryVocabularyGroup holds the claim
// vocabularyGroupGrades' comment makes. A sixth posting section added to ADR-0044's
// list must get its own level, or the count silently saturates at five and the score
// stops seeing the very pages the widening was for.
//
// Adding a level is a RETRAIN, not an edit: a level's name is a key in the fitted
// table, so a level with no weight behind it contributes nothing until
// "llmbench train-scorer" has run again.
func TestVocabularyGroupGradesCoverEveryVocabularyGroup(t *testing.T) {
	if len(vocabularyGroupGrades) != len(postingVocabularyGroups) {
		t.Errorf("%d grade levels for %d posting sections: every section needs a level, and adding one is a retrain rather than an edit",
			len(vocabularyGroupGrades), len(postingVocabularyGroups))
	}
}

// TestJobLinkGradesAreAllReachableAtTheShippedSaturationCount holds jobLinkGrades'
// ceiling against the rung it mirrors. Rung 7 rejects at
// ExtractJobLinkSaturationCount, so the deepest count that can reach the score is one
// below it; a level above that can never fire and would be a weight fitted on nothing.
//
// Raising the saturation count keeps this green -- the bucket simply saturates, which
// is ADR-0016's own behaviour. Lowering it trips this test, which is the intended
// prompt to retrain with a shorter table.
func TestJobLinkGradesAreAllReachableAtTheShippedSaturationCount(t *testing.T) {
	reachable := crawler.DefaultLLMGateConfig().ExtractJobLinkSaturationCount - 1
	if len(jobLinkGrades) > reachable {
		t.Errorf("%d job-link grade levels but only %d distinct links can reach the score (rung 7 rejects at %d)",
			len(jobLinkGrades), reachable, reachable+1)
	}
}

// TestTheGateSharesOneBodyFoldAndOneLinkCountWithTheScore holds the reuse ADR-0049's
// rung is only affordable with. Rung 8 folds the page body and rung 7 walks its links;
// re-deriving either at rung 9 would run the gate's two most expensive detectors twice
// on every page the veto judges -- the same doubling foldedBody's own comment forbids
// inside rung 8.
//
// It is an internal test because the property IS the shared unexported path: from
// outside the package the two readings are indistinguishable, which is exactly the
// point of the refactor and the reason it needs pinning here.
func TestTheGateSharesOneBodyFoldAndOneLinkCountWithTheScore(t *testing.T) {
	u, err := crawler.NewURL("https://acme.com/jobs/senior-go-engineer")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	// Link-heavy on purpose: countJobPostingLinks parses every entry of URLs, and only
	// the job-shaped same-host ones count toward rung 7 -- so a page like this reaches
	// rung 9 carrying a link walk nobody should pay for twice.
	content := &crawler.Content{
		Title:       "Senior Go Engineer (m/w/d)",
		MainContent: "Aufgaben und Verantwortung. Ihr Profil: mehrjährige Erfahrung. Wir bieten Vergütung nach Tarif. Jetzt bewerben.",
		URLs: []string{
			"https://acme.com/about", "https://acme.com/imprint", "https://acme.com/blog/one",
			"https://acme.com/blog/two", "https://other.com/jobs/elsewhere",
			"https://acme.com/jobs/other-role",
		},
	}

	t.Run("a fold is lazy and folds at most once", func(t *testing.T) {
		fold := newBodyFold(content)
		if fold.done {
			t.Error("newBodyFold folded eagerly; rung 8's URL and Title marks must be able to admit a page without paying for the body")
		}
		first := fold.folded()
		if want := foldedBody(content); first != want {
			t.Fatalf("folded() = %q, want %q", first, want)
		}
		if !fold.done {
			t.Error("folded() did not mark the fold done, so the next reader folds again")
		}
		if again := fold.folded(); again != first {
			t.Errorf("the second folded() returned %q, want the first fold %q", again, first)
		}
	})

	t.Run("the shared inputs produce the standalone signals", func(t *testing.T) {
		shared := signalsFrom(content, newBodyFold(content), countJobPostingLinks(u, content))
		standalone := Signals(u, content)
		if len(shared) != len(standalone) {
			t.Fatalf("signalsFrom produced %d signals, Signals produced %d", len(shared), len(standalone))
		}
		for i := range shared {
			if shared[i] != standalone[i] {
				t.Fatalf("signal %d is %q shared and %q standalone; the two readings must be one sequence", i, shared[i], standalone[i])
			}
		}
	})

	t.Run("the gate's rung reports the standalone score", func(t *testing.T) {
		cfg := crawler.DefaultLLMGateConfig()
		cfg.RequirePositiveEvidence, cfg.LearnedVeto = true, true
		v := ExtractGate(u, content, cfg)
		if !v.Scored {
			t.Fatalf("the fixture must reach rung 9, got rung %q", v.Rung)
		}
		if want := Score(u, content); v.Score != want {
			t.Errorf("ExtractGate score = %.17g, want %.17g", v.Score, want)
		}
	})
}
