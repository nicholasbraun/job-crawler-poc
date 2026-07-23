package main

import (
	"os"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// The committed Extract Gold Set baseline, produced by running the real
// parser -> ShouldExtract pipeline over cmd/llmbench/extract-testdata with the
// default gate config (see the extract-testdata README and #114). The false-drop
// count is the AC-critical hard guard; the leak count and extract-call rate are
// the soft baseline the reject rungs (#115) are calibrated against.
//
// #115 landed the content reject rungs: the saturation rung (K=5) now rejects the
// three hub-index openings-index leaks (each carrying 5 same-host job links),
// cutting extract-calls 17->14 and leaks 7->4 while holding false-drop = 0.
//
// The extract-gate URL rungs (root/locale reject + terminal-index reject) then
// rejected the /work-with-us careers-landing leak (a terminal index word), cutting
// extract-calls 14->13 and leaks 4->3, still false-drop = 0. The three remaining
// leaks are the structurally-silent residue pages (about/our-culture, life,
// culture/values) -- the deferred-L2 population the ADR-0020 content confirm targets.
const (
	baselineExtractCalls    = 13  // was 14: the /work-with-us landing leak now rejects via the terminal-index rung
	baselineExtractCallRate = 0.5 // was 0.5385 = round(13/26)
	baselineLeaks           = 3   // was 4: only the 3 structurally-silent culture pages still leak
	baselineDetailFixtures  = 10  // unchanged -- every detail still extracts (false-drop = 0)
)

// TestExtractGate_CommittedSetNoFalseDrop is the automated counterpart to the
// manual `go run ./cmd/llmbench extract`: it drives the SAME live pipeline
// (replayExtractGate: parser.Parse -> pagegate.ShouldExtract) over the committed
// Extract Gold Set so the false-drop hard guard the ticket exists to protect
// (#114 AC7) runs in `go test`, not only by hand. Without it, adding a `detail`
// fixture the gate skips -- e.g. a German /karriere/<slug> posting -- or changing
// jobPathSegments/CareerPathSignals would silently redden the baseline while the
// normal suite stayed green (there is no CI to run the verb).
func TestExtractGate_CommittedSetNoFalseDrop(t *testing.T) {
	rows, err := replayExtractGate(os.DirFS("extract-testdata"), crawler.DefaultLLMGateConfig())
	if err != nil {
		t.Fatalf("replayExtractGate(extract-testdata): %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("replayExtractGate produced no rows over the committed Extract Gold Set")
	}

	report := bench.ScoreExtract(rows)
	e := report.Extract

	// The hard guard: not a single detail-labelled fixture may be skipped. A
	// false-drop is a real single posting the extractor never sees.
	if report.Failed() {
		t.Errorf("Failed() = true, want false; false-drops: %v", e.FalseDrops)
	}
	if len(e.FalseDrops) != 0 {
		t.Errorf("FalseDrops = %v, want none (a detail-labelled page the gate rejected)", e.FalseDrops)
	}
	detail := e.ByClass[bench.ExtractDetail]
	if detail.FN != 0 {
		t.Errorf("detail false-negatives = %d, want 0 (every detail fixture must extract)", detail.FN)
	}
	if detail.Recall != 1 {
		t.Errorf("detail recall = %v, want 1 (the recall-safety number the guard protects)", detail.Recall)
	}
	if detail.Total != baselineDetailFixtures {
		t.Errorf("detail fixtures = %d, want %d", detail.Total, baselineDetailFixtures)
	}

	// The soft baseline snapshot: locks where the gate still leaks non-postings so
	// accidental gate or fixture drift is caught. Since #115 landed the content
	// reject rungs, the four remaining leaks are the structurally-silent residue
	// pages (the deferred-L2 population); a mismatch here is a signal to re-baseline,
	// never a false-drop failure.
	if len(e.Leaks) != baselineLeaks {
		t.Errorf("leaks = %d %v, want baseline %d (re-baseline if the gate changed under #115)", len(e.Leaks), e.Leaks, baselineLeaks)
	}
	if e.ExtractCalls != baselineExtractCalls {
		t.Errorf("extract-calls = %d, want baseline %d", e.ExtractCalls, baselineExtractCalls)
	}
	if e.ExtractCallRate != baselineExtractCallRate {
		t.Errorf("extract-call-rate = %v, want baseline %v", e.ExtractCallRate, baselineExtractCallRate)
	}
}
