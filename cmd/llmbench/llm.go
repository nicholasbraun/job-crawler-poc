// LLM config plumbing for the bench verb. Reads LLM_* through the same
// openrouter.ConfigFromEnv the server uses, so a bench run and a crawl cannot
// disagree on a default -- a benchmark scored against different prompt caps or a
// different model than production is measuring the wrong thing. Deliberately
// kept out of go test: the real classifier is only ever driven by the bench CLI,
// never by a unit test.
package main

import (
	"github.com/joho/godotenv"
	"github.com/nicholasbraun/job-crawler-poc/internal/env"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
)

// The bench verb drives the real classifier through the same Confirmer seam the
// discovery crawl uses -- reuse, not a reimplementation.
var _ careerpageprocessor.Confirmer = (*openrouter.CareerPageClassifier)(nil)

// loadLLMConfig reads LLM_* into an openrouter.Config after a best-effort .env
// load. A malformed value is a returned error the CLI maps to exit 2 -- never a
// log.Fatal, which is why the Loader hands the error back instead of acting on
// it (ADR-0045).
func loadLLMConfig() (openrouter.Config, error) {
	_ = godotenv.Load()

	var ld env.Loader
	cfg := openrouter.ConfigFromEnv(&ld)
	if err := ld.Err(); err != nil {
		return openrouter.Config{}, err
	}
	return cfg, nil
}
