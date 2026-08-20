package openrouter

import "github.com/nicholasbraun/job-crawler-poc/internal/env"

// ConfigFromEnv reads the LLM_* knobs into a Config. It lives here, beside the
// defaults it falls back to, rather than in each binary's main: the crawler
// (cmd/server) and the gate benchmarks (cmd/llmbench) must drive the model
// through identical settings, or a bench score stops describing the crawl it
// claims to measure -- and gate changes in this repo are settled by that score.
// Before ADR-0045 the three defaults below were spelled out in both mains as
// literals, one copy-paste away from disagreeing silently.
//
// Leave LLM_BASE_URL / LLM_MODEL unset for OpenRouter's hosted API, or point
// them at any OpenAI-compatible endpoint, e.g. a local Ollama at
// http://localhost:11434/v1/chat/completions with LLM_MODEL=qwen2.5:3b. Use a
// non-reasoning instruct model there: a reasoning model (e.g. qwen3.5) runs a
// hidden think phase the crawler discards, a large latency tax for a one-line
// verdict.
//
// LLM_TIMEOUT bounds one request end-to-end and defaults generously because a
// local model generates serially, so a request can sit in the server's queue
// before its first token. LLM_CLASSIFY_MAX_CHARS / LLM_EXTRACT_MAX_CHARS cap the
// page text (in runes) sent to each call; the signal sits near the top of a
// page, so capping keeps a local model fast and avoids timeouts on huge pages.
// What they bound is what the prompt actually carries: with
// PARSE_STRUCTURAL_RENDERING on that is the Structural Rendering with link
// targets omitted, ~1.1x the Flattened Text, so the same cap shows the model
// about a tenth less page in exchange for its structure (ADR-0046).
//
// Failures are recorded on ld, not returned: the caller reads the rest of its
// configuration and then decides what a bad knob costs (see env.Loader).
func ConfigFromEnv(ld *env.Loader) Config {
	return Config{
		// The credential knobs have no default and no validation here. An empty
		// APIKey is legitimate -- a local Ollama needs none -- and an empty BaseURL
		// or Model means "take the hosted default", applied by withDefaults.
		APIKey:  ld.String("LLM_API_KEY", ""),
		BaseURL: ld.String("LLM_BASE_URL", ""),
		Model:   ld.String("LLM_MODEL", ""),

		Timeout:          ld.PositiveDuration("LLM_TIMEOUT", DefaultTimeout),
		ClassifyMaxChars: ld.PositiveInt("LLM_CLASSIFY_MAX_CHARS", defaultClassifyMaxChars),
		ExtractMaxChars:  ld.PositiveInt("LLM_EXTRACT_MAX_CHARS", defaultExtractMaxChars),
	}
}
