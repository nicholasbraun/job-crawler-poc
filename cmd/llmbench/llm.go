// LLM config plumbing for the bench verb. Reads LLM_* the same way
// cmd/server/main.go does (godotenv + os.Getenv into openrouter.Config) and is
// deliberately kept out of go test: the real classifier is only ever driven by
// the bench CLI, never by a unit test.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
)

// The bench verb drives the real classifier through the same Confirmer seam the
// discovery crawl uses -- reuse, not a reimplementation.
var _ careerpageprocessor.Confirmer = (*openrouter.CareerPageClassifier)(nil)

// envOr returns the value of environment variable key, or fallback if it is
// unset or empty. Copied from cmd/server/main.go (unexported there, no shared
// home to import) for the same reason capture.go copies userAgent.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadLLMConfig reads LLM_* into an openrouter.Config exactly like the server:
// a best-effort .env load, then LLM_TIMEOUT (default 5m), LLM_CLASSIFY_MAX_CHARS
// (default 1500), and LLM_EXTRACT_MAX_CHARS (default 8000). A malformed or
// non-positive value is a returned error the CLI maps to exit 2 -- never a
// log.Fatal. ExtractMaxChars is parsed for parity even though the classifier
// ignores it, keeping the scaffolding kind-agnostic.
func loadLLMConfig() (openrouter.Config, error) {
	_ = godotenv.Load()

	timeout, err := time.ParseDuration(envOr("LLM_TIMEOUT", "5m"))
	if err != nil {
		return openrouter.Config{}, fmt.Errorf("parse LLM_TIMEOUT: %w", err)
	}
	classifyMaxChars, err := strconv.Atoi(envOr("LLM_CLASSIFY_MAX_CHARS", "1500"))
	if err != nil || classifyMaxChars < 1 {
		return openrouter.Config{}, fmt.Errorf("parse LLM_CLASSIFY_MAX_CHARS: must be a positive integer, got %q", os.Getenv("LLM_CLASSIFY_MAX_CHARS"))
	}
	extractMaxChars, err := strconv.Atoi(envOr("LLM_EXTRACT_MAX_CHARS", "8000"))
	if err != nil || extractMaxChars < 1 {
		return openrouter.Config{}, fmt.Errorf("parse LLM_EXTRACT_MAX_CHARS: must be a positive integer, got %q", os.Getenv("LLM_EXTRACT_MAX_CHARS"))
	}

	return openrouter.Config{
		APIKey:           os.Getenv("LLM_API_KEY"),
		BaseURL:          os.Getenv("LLM_BASE_URL"),
		Model:            os.Getenv("LLM_MODEL"),
		Timeout:          timeout,
		ClassifyMaxChars: classifyMaxChars,
		ExtractMaxChars:  extractMaxChars,
	}, nil
}
