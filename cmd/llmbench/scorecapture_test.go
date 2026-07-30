package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// TestScoreCaptureEquivalentToExtract proves the Content-replay path (score-capture)
// yields the identical Extract Gate decision as the committed HTML-fixture path
// (extract) for every fixture. Parsing HTML to Content then scoring, vs scoring an
// already-parsed captured Content, must agree -- capture just skips the re-parse.
// This is what makes reusing the crawl's parsed bytes a faithful substitute for a
// re-fetch.
func TestScoreCaptureEquivalentToExtract(t *testing.T) {
	const gold = "extract-testdata"
	cfg := crawler.DefaultLLMGateConfig()

	// HTML path: the committed extract verb's own replay (parse -> gate).
	htmlRows, err := replayExtractGate(os.DirFS(gold), cfg)
	if err != nil {
		t.Fatalf("replayExtractGate: %v", err)
	}
	wantByURL := make(map[string]bool, len(htmlRows))
	for _, r := range htmlRows {
		wantByURL[r.URL] = r.Extract
	}

	// Content path: parse each fixture to Content, write it as a capture line, then
	// score that file through replayCaptured (gate over the captured Content).
	m, err := bench.LoadExtractManifest(os.DirFS(gold))
	if err != nil {
		t.Fatalf("LoadExtractManifest: %v", err)
	}
	p := parser.NewHTMLParser()
	capPath := filepath.Join(t.TempDir(), "cap.jsonl")
	f, err := os.Create(capPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, e := range m.Entries {
		html, err := e.ReadHTML(os.DirFS(gold))
		if err != nil {
			t.Fatalf("read %s: %v", e.File, err)
		}
		content, err := p.Parse(html)
		if err != nil {
			t.Fatalf("parse %s: %v", e.File, err)
		}
		if err := enc.Encode(captureRecord{URL: e.URL, Verdict: e.Label.Positive(), Label: e.Label, Content: *content}); err != nil {
			t.Fatalf("encode %s: %v", e.File, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	capRows, skipped, err := replayCaptured(capPath, cfg)
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}
	if skipped != 0 {
		t.Fatalf("unexpected skipped=%d", skipped)
	}
	if len(capRows) != len(htmlRows) {
		t.Fatalf("row count mismatch: html=%d capture=%d", len(htmlRows), len(capRows))
	}
	for _, r := range capRows {
		want, ok := wantByURL[r.URL]
		if !ok {
			t.Fatalf("capture row URL absent from html rows: %s", r.URL)
		}
		if r.Extract != want {
			t.Errorf("gate decision differs for %s: html=%v capture=%v", r.URL, want, r.Extract)
		}
	}
}

// TestReplayCapturedSkipsUnlabeled confirms unlabeled lines are skipped (not fatal)
// so a partially-labeled capture file scores only its labeled rows, and that a
// captured Content deserializes into a scored row.
func TestReplayCapturedSkipsUnlabeled(t *testing.T) {
	capPath := filepath.Join(t.TempDir(), "c.jsonl")
	lines := `{"url":"https://acme.com/careers/senior-go-engineer","verdict":true,"label":"detail","content":{"MainContent":"apply now","JSONLD":["{\"@type\":\"JobPosting\"}"]}}
{"url":"https://acme.com/unlabeled","verdict":false,"label":"","content":{"MainContent":"x"}}
`
	if err := os.WriteFile(capPath, []byte(lines), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	rows, skipped, err := replayCaptured(capPath, crawler.DefaultLLMGateConfig())
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}
	if skipped != 1 {
		t.Fatalf("skipped=%d, want 1", skipped)
	}
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
	if rows[0].Label != bench.ExtractDetail {
		t.Fatalf("label=%q, want detail", rows[0].Label)
	}
}
