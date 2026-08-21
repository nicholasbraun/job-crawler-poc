package crawler

import "strings"

// WithoutLinkTargets narrows a page's Structural Rendering to the variant the two
// LLM prompts read: every link keeps its text, none keeps its target (ADR-0046).
// One renderer produces one rendering; a human labeller reads it whole, and the
// model reads this narrowing of it.
//
// It is a pure DELETION -- the result is always a subsequence of the input -- which
// is what makes "the labeller's variant is a superset of the prompt's" a property
// rather than a claim:
//
//	"[Apply now](/apply)"                    -> "[Apply now]"
//	"[Fixed Term [12 Months]](/careers/468)" -> "[Fixed Term [12 Months]]"
//	"#\tOpen positions"                      -> unchanged, a heading's level prefix
//	"-\tGo"                                  -> unchanged, a list item's bullet
//	"|\tBerlin"                              -> unchanged, a table row's bar
//	"Redcare\t\nNewsroom"                    -> unchanged, the JOIN
//	"[input checkbox: role]"                 -> unchanged, a form control
//	"[button: Senden]"                       -> unchanged
//	"[img: 1A SMS GmbH]"                     -> unchanged
//	"already flat page text"                 -> unchanged, byte for byte
//
// Why the target goes: over the 90 committed classifier fixtures the rendering
// measures 1.253x the Flattened Text with targets and 1.081x without (ADR-0046
// measured 1.43x / 1.10x over 36 live pages). LLM_EXTRACT_MAX_CHARS is 8000, so at
// an unchanged cap the targets-carrying form would show the model roughly a quarter
// less page than today, where the narrowed form costs about a tenth -- "keep the
// targets" and "raise the cap" compound rather than compose.
//
// Why the brackets stay: the marking is the signal. A rail of "similar jobs" links
// beside a posting is only distinguishable from the posting's own prose while the
// link text is still marked as link text, and dropping the brackets buys nothing.
//
// Why it is the identity while the kill switch is off: every link marker the
// renderer writes carries "](", and zero of the 526 KB of page text in the 90
// committed fixtures contains that pair. With PARSE_STRUCTURAL_RENDERING off the
// parser's output has no markers at all, so the prompt carries exactly the bytes it
// carried before the renderer existed -- asserted over all 90 fixtures by
// TestPromptVariantIsIdentityOnFlattenedText.
//
// Only a LINK's target goes. A link rule on its own cannot express that, because
// every marker ends in "]" and so satisfies the left half of linkMarkerPattern: an
// image or a control glued to page text that opens with a parenthesis reads as a
// link with a target, and the page's own parenthetical is what gets deleted.
//
//	"#\vSales Manager[img: Acme logo](m/w/d)"  ->  "(m/w/d)" deleted, the exact
//	                                               token that marks a German role
//	"[input text: Vorname](optional) Nachname" ->  "(optional)" deleted
//
// So the scan recognises an attribute-only marker and a button FIRST and leaves
// them alone, and only what is left is read as a link (#289). Nothing persisted was
// ever at risk -- FlattenedText strips control and button markers before the link
// rule and so never had the ambiguity -- but the loss landed in the two prompts,
// which is the one place this whole change exists to improve.
//
// The residual, stated rather than hidden: page text that literally contains "]("
// loses the run between them. The empty "[]" marker of a link with no text and the
// "[[img: ...]]" of an image inside a link are left exactly as the renderer wrote
// them -- 138 and 100 occurrences across the 90 fixtures, ~4% of 3443 markers --
// because one rule with no special cases is what keeps this a deletion.
//
// Pure: no model, no network, no database.
func WithoutLinkTargets(rendering string) string {
	// Fast path, and an exact one: a link marker cannot exist without "](", so text
	// carrying none is already the narrowing of itself. It is what puts today's
	// flattened parser output on the identical path it was on before the rendering
	// existed.
	if !strings.Contains(rendering, "](") {
		return rendering
	}
	return anyMarkerOrLink.ReplaceAllStringFunc(rendering, func(marker string) string {
		groups := linkMarkerPattern.FindStringSubmatch(marker)
		if groups == nil {
			return marker // an image, a control or a button: not a link, keep it whole
		}
		return "[" + groups[1] + "]"
	})
}
