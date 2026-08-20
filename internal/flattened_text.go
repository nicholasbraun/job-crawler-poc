package crawler

import "strings"

// FlattenedText derives a page's Flattened Text from the page content the parser
// stores in Content.MainContent (ADR-0046): the plain run of words every non-model
// consumer reads -- the Posting Body and the Corpus search index it feeds, the
// extraction-cache key, the Extract Gate's phrase marks, the keyword filters, the
// duplication probe.
//
// It is the inverse of what a Structural Rendering adds: whatever the renderer
// synthesized comes back out, and what the page itself said stays. It is DERIVED on
// demand rather than stored beside the rendering, so the two can never disagree about
// what a page says.
//
// The invariant the whole change rests on:
//
//	FlattenedText(a page's Structural Rendering) == the parser's pre-rendering output
//
// byte for byte, over the 90 committed classifier fixtures frozen in
// internal/parser/testdata/flattened_text.golden.jsonl. SourceHash is persisted on
// ~46k job_listing rows and a different value there silently re-extracts the whole
// Corpus, so the derivation is exact or the rendering does not ship (ADR-0046).
//
// Today it collapses whitespace and nothing else, because the parser still emits one
// flat run of words and this is the identity on it: consumers are routed one commit
// BEFORE the parser moves (#278), so the commit that moves it does not also have to
// edit thirteen call sites. #279 adds the stripping, and these are the rules it must
// hold -- each one a place a naive strip gets it wrong, because the renderer
// synthesizes text the flattened form never carried:
//
//	[input text: Vorname]         -> ""            the <label> already contributed "Vorname"
//	[select: Position]            -> ""            likewise
//	[button: Senden]              -> "Senden"      a button's own text IS in today's output
//	![1A SMS GmbH]                -> ""            today's output carries no image alt text
//	[Apply now](/apply)           -> "Apply now"   the link text, never its target
//	# Heading, - list item        -> the text, the prefix dropped
//	a table row's cell separators -> one space, like every other block boundary
//
// Whitespace is not an optional half of that: the line structure a rendering
// introduces IS whitespace, so every run of it collapses to a single space and the
// ends are trimmed, matching the parser's normalizeWS exactly. A non-breaking space
// counts as whitespace and folds; a zero-width space does not and is page text.
// Bytes that are not valid UTF-8 pass through unchanged -- a mis-encoded page must
// reach SourceHash as the same bytes it always did, and PostingBody owns dropping
// them (see validUTF8).
//
// Idempotent -- FlattenedText(FlattenedText(s)) == FlattenedText(s) -- so a consumer
// handed already-flattened text (a captured Extract Gold Set row, a hand-written
// Content in a test) reads it unchanged.
//
// Pure: no model, no network, no database.
func FlattenedText(rendering string) string {
	return strings.Join(strings.Fields(rendering), " ")
}
