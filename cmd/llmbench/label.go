// The label verb hardens Gold-Set labels: a stronger LABELER_* model proposes a
// category (and a derived label) for every unverified fixture, recording it as
// proposed_label/proposed_category and seeding label/category only when the
// committed label is empty. It is idempotent (skips verified:true) and never
// overwrites a human hand-edit. Like llm.go this file is deliberately kept out
// of go test: the labeler model is only ever driven by the label CLI.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// loadLabelerConfig reads LABELER_* the same way loadLLMConfig reads LLM_*.
// LABELER_MODEL is required and must differ from LLM_MODEL (a stronger model,
// structurally distinct from the model under test); distinctness is a
// model-string comparison only. Timeout/ClassifyMaxChars are left zero so
// openrouter's defaults (5m / 1500) apply.
func loadLabelerConfig() (openrouter.Config, error) {
	_ = godotenv.Load()

	model := os.Getenv("LABELER_MODEL")
	if model == "" {
		return openrouter.Config{}, fmt.Errorf("LABELER_MODEL is required (the stronger labeler model, distinct from LLM_MODEL)")
	}
	if under := os.Getenv("LLM_MODEL"); under != "" && under == model {
		return openrouter.Config{}, fmt.Errorf("LABELER_MODEL must differ from LLM_MODEL (a labeler must be structurally distinct from the model under test), both are %q", model)
	}

	return openrouter.Config{
		APIKey:  os.Getenv("LABELER_API_KEY"),
		BaseURL: os.Getenv("LABELER_BASE_URL"),
		Model:   model,
	}, nil
}

// runLabel proposes a category+label for every unverified manifest entry via the
// LABELER_* model, recording proposed_label/proposed_category (and seeding
// label/category when the committed label is empty), skipping verified:true.
// Per-entry model/parse errors are logged and skipped so partial progress
// persists; only config and manifest read/write failures are hard errors.
// Returns the process exit code (2 config/usage, 1 IO error).
func runLabel(args []string) int {
	fs := flag.NewFlagSet("label", flag.ExitOnError)
	gold := fs.String("gold", "cmd/llmbench/testdata", "Gold-Set directory holding manifest.json and pages/*.html")
	_ = fs.Parse(args)

	cfg, err := loadLabelerConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench label: %v\n", err)
		return 2
	}
	proposer := openrouter.NewCategoryProposer(cfg)

	manifestPath := *gold + "/manifest.json"
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench label: read manifest: %v\n", err)
		return 1
	}
	// Read raw (not LoadManifest): capture stubs have empty labels that would
	// fail validation, and this verb's job is to fill them in.
	entries := []bench.Entry{}
	if err := json.Unmarshal(data, &entries); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench label: parse manifest: %v\n", err)
		return 1
	}

	fsys := os.DirFS(*gold)
	p := parser.NewHTMLParser()
	ctx := context.Background()

	labeled, skipped := 0, 0
	for i := range entries {
		if entries[i].Verified {
			skipped++
			continue
		}
		html, err := entries[i].ReadHTML(fsys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench label: %v\n", err)
			continue
		}
		content, err := p.Parse(html)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench label: parse %q: %v\n", entries[i].File, err)
			continue
		}
		raw, err := proposer.Propose(ctx, entries[i].URL, content)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench label: propose %q: %v\n", entries[i].URL, err)
			continue
		}
		cat := bench.Category(raw)
		if !cat.Valid() {
			fmt.Fprintf(os.Stderr, "llmbench label: %q: labeler returned unknown category %q, skipping\n", entries[i].URL, raw)
			continue
		}
		lbl := bench.LabelForCategory(cat)
		entries[i].ProposedCategory = cat
		entries[i].ProposedLabel = lbl
		if entries[i].Label == "" {
			entries[i].Label = lbl
			entries[i].Category = cat
		}
		labeled++
	}

	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench label: encode manifest: %v\n", err)
		return 1
	}
	out = append(out, '\n')
	if err := os.WriteFile(manifestPath, out, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "llmbench label: write manifest: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stdout, "labeled %d, skipped %d verified\n", labeled, skipped)
	return 0
}
