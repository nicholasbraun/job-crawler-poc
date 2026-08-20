package parser_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestFlattenedTextIsIdentityOnTodaysOutput asserts crawler.FlattenedText leaves the
// parser's output alone, over the same 90 committed classifier fixtures the golden
// artefact freezes (#277). It is the evidence that routing every content consumer
// through the derivation (#278) changed nothing: the Posting Body, both SourceHash
// call sites, the Extract Gate's phrase marks, the keyword filter, the duplication
// probe and both LLM prompts all read exactly the bytes they read before.
//
// It stays meaningful after #279, when the parser starts producing a Structural
// Rendering: the golden rows remain Flattened Text, so this keeps pinning the half of
// ADR-0046's round-trip invariant that says the derivation is a no-op on text that is
// already flat -- flattening twice must never differ from flattening once.
//
// It reads the artefact rather than re-parsing the fixtures, so a parser change can
// never quietly make both sides move together.
func TestFlattenedTextIsIdentityOnTodaysOutput(t *testing.T) {
	golden := readGoldenRows(t)

	for name, want := range golden {
		t.Run(name, func(t *testing.T) {
			if got := crawler.FlattenedText(want); got != want {
				offset, gotWindow, wantWindow := firstDiff(got, want)
				t.Errorf("FlattenedText is not the identity on the Flattened Text of %s: got %d bytes, golden has %d, first difference at byte %d\n got: %s\nwant: %s\n"+
					"the derivation must leave already-flat text untouched, or routing a consumer through it re-keys the Corpus (ADR-0046)",
					name, len(got), len(want), offset, gotWindow, wantWindow)
			}
		})
	}
}
