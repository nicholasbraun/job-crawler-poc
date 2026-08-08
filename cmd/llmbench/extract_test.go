package main

import (
	"os"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// The committed reject-rung regression set baseline, produced by running the real
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
// leaks were the structurally-silent residue pages (about/our-culture, life,
// culture/values) -- the deferred-L2 population the ADR-0020 content confirm targeted.
//
// #264 turned the Positive Evidence rung on by default, and it SHEDS EXACTLY THOSE
// THREE: extract-calls 13->10, leaks 3->0, false-drop still 0. On this set the gate
// now extracts its ten `detail` fixtures and nothing else -- precision and recall both
// 1.0. That is a small independent corroboration that ADR-0044 was right to close the
// deferred L2 confirm rather than leave it pending: the population L2 was reserved for
// is the population this rung sheds.
//
// These fixtures remain ADR-0043 "evidence of nothing" -- they are invented, and a
// gate that reads structured data scores perfectly on them by construction. The real
// scoring is `score-capture` over the Extract Gold Set, guarded by
// TestExtractGoldSetFalseDropGuard.
const (
	baselineExtractCalls    = 10     // was 13: the Positive Evidence rung sheds the 3 residue leaks
	baselineExtractCallRate = 0.3846 // was 0.5 = round(13/26); now round(10/26)
	baselineLeaks           = 0      // was 3: the structurally-silent culture pages carry no Positive Evidence
	baselineDetailFixtures  = 10     // unchanged -- every detail still extracts (false-drop = 0)
)

// TestExtractGate_CommittedSetNoFalseDrop is the automated counterpart to the
// manual `go run ./cmd/llmbench extract`: it drives the SAME live pipeline
// (replayExtractGate: parser.Parse -> pagegate.ShouldExtract) over the committed
// reject-rung regression set so the false-drop hard guard the ticket exists to protect
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
		t.Fatal("replayExtractGate produced no rows over the committed reject-rung regression set")
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

	// The soft baseline snapshot: locks where the gate leaks non-postings so accidental
	// gate or fixture drift is caught. Since #264 turned the Positive Evidence rung on
	// there are no leaks left on this synthetic set at all; a mismatch here is a signal
	// to re-baseline, never a false-drop failure.
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
