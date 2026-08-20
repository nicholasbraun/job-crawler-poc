package openrouter

import (
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// PromptForm is the page text both LLM prompts carry: the page's Structural
// Rendering narrowed to the variant the model reads -- link targets dropped, the
// brackets kept -- and then capped to maxChars RUNES (ADR-0046).
//
// The order is load-bearing, and it is why this is one function rather than two
// calls repeated at each prompt site. Narrowing BEFORE capping means a cut can
// never land inside an href and leave a dangling "](" in the prompt, and it means
// the cap bounds what is actually sent rather than a longer form that is shortened
// afterwards. With PARSE_STRUCTURAL_RENDERING off the parser produces Flattened
// Text, which carries no markers at all, so the narrowing is the identity and the
// prompt is byte-identical to the one that predates the renderer.
//
// It is exported so the offline rendering A/B (cmd/llmbench score-rendering, #282)
// measures the bytes the extractor really sends instead of a re-implementation of
// them that can drift away from it.
func PromptForm(mainContent string, maxChars int) string {
	return capChars(crawler.WithoutLinkTargets(mainContent), maxChars)
}
