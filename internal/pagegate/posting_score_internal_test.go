// The invariants of the Posting Score that cannot be reached from outside the
// package: the shape of the two graded Score Signal tables against the detectors and
// the gate config they mirror. Everything else about the score is asserted through
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
