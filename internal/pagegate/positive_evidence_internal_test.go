// The one invariant of the Positive Evidence rung that cannot be reached from
// outside the package: the two weak-tier phrase lists must not overlap. Everything
// else about the rung is asserted through ShouldExtract in the external test file.
package pagegate

import (
	"strings"
	"testing"
)

// TestApplyPhrasesAndVocabularyGroupsAreDisjoint holds the invariant applyPhrases'
// comment states. Both lists are matched with strings.Contains, so disjointness has
// to hold under SUBSTRING containment in BOTH directions, not just as sets: a
// vocabulary phrase inside an apply phrase ("your application" inside "submit your
// application") makes one apply button score the vocabulary group as well, and the
// weak tier's agreement requirement -- dense vocabulary AND a corroborator --
// collapses into a disjunction for that phrase. A careers page with one real section
// and an apply button would then admit, which is the shape ADR-0044's measurement
// rejects.
func TestApplyPhrasesAndVocabularyGroupsAreDisjoint(t *testing.T) {
	for _, phrase := range applyPhrases {
		for i, group := range postingVocabularyGroups {
			for _, vocab := range group {
				if strings.Contains(phrase, vocab) {
					t.Errorf("apply phrase %q contains vocabulary phrase %q (group %d): the apply affordance would score that group too",
						phrase, vocab, i)
				}
				if strings.Contains(vocab, phrase) {
					t.Errorf("vocabulary phrase %q (group %d) contains apply phrase %q: the group would score the apply affordance too",
						vocab, i, phrase)
				}
			}
		}
	}
}
