package main

import (
	"path/filepath"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// The Free Extraction (ADR-0042) saves a Job Listing with NO model veto anywhere
// in the loop, so its two failure modes are silent and would reach the Corpus at
// scale: it fires on a page that is not a single posting, or it fires correctly but
// mis-reads a field. Neither shows up in any live metric -- a wrong listing looks
// exactly like a right one. The tests below make both fatal at merge time, scored
// over the real captured pages of the Extract Gold Set with a stub in place of the
// model, so they run under `go test ./...` with no network, no model and no Docker.
//
// The expected values they score against are an INDEPENDENT reading of each page's
// own JSON-LD (scripts/propose-expected.sh, jq + python3), not the Go traversal
// under test. Generating them from that traversal would make this a tautology,
// able to catch only a future regression and never a bug that exists today.

// freeExtractionFires is how many committed rows the Free Extraction serves. It is
// EXACT, not a bound: this number is the mechanism's realized coverage on the
// substrate, and any movement in it -- up or down -- is a behaviour change that
// must be seen in a diff rather than absorbed silently. It is a regression ratchet
// on a committed file, never a quality threshold on the mechanism (ADR-0020 keeps
// coverage soft).
const freeExtractionFires = 70

// acceptedFreeOnResidue is how many of those rows are labelled residue and carry an
// explicit, per-row acceptance of the fire. Sixteen are ads whose posting was
// withdrawn while their JobPosting JSON-LD kept being served -- the Free Extraction
// reads their fields correctly and the job is gone, which is ADR-0035's liveness
// territory rather than a schema-traversal failure. Three are evergreen
// "spontaneous application" pages with no specific role: those are genuine
// precision failures and the strongest argument for narrowing ADR-0042.
//
// All 19 are AGENT-PROPOSED AND UNCONFIRMED. RATCHET: lower it as the human
// withdraws an acceptance, never raise it -- a new unexcused fire must go red.
const acceptedFreeOnResidue = 19

// TestCommittedGoldSetFreeExtractionFidelity is the acceptance guard: it replays
// the real decorator over every committed row and fails the build on either silent
// failure mode, naming the offending URL (and, for a divergence, the field and both
// values) so a failure says exactly what broke.
func TestCommittedGoldSetFreeExtractionFidelity(t *testing.T) {
	rows, skipped, err := replayFreeExtraction(t.Context(), filepath.Join("extract-goldset", goldSetFile))
	if err != nil {
		t.Fatalf("replayFreeExtraction: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped %d unlabeled rows, want 0 (every committed row carries a label)", skipped)
	}

	sc := bench.ScoreFreeExtraction(rows).Free
	for _, url := range sc.FiredOnNonPosting {
		t.Errorf("a Free Extraction saved %s, which is not a posting, with no model in the loop", url)
	}
	for _, d := range sc.FieldDivergences {
		t.Errorf("%s [%s]: Free Extraction read %q, want %q", d.URL, d.Field, d.Got, d.Want)
	}
	for _, url := range sc.MissingExpectation {
		t.Errorf("a Free Extraction fired on %s with no expected extraction to score it against", url)
	}
	for _, url := range sc.UnusedExpectation {
		t.Errorf("%s carries an expected extraction but the Free Extraction did not fire on it", url)
	}

	if sc.Free != freeExtractionFires {
		t.Errorf("the Free Extraction served %d rows, want exactly %d; set freeExtractionFires to %d and say why in the commit", sc.Free, freeExtractionFires, sc.Free)
	}
	if got := len(sc.AcceptedFires); got != acceptedFreeOnResidue {
		t.Errorf("%d residue fires carry a per-row acceptance, want exactly %d; lower acceptedFreeOnResidue to %d as acceptances are withdrawn", got, acceptedFreeOnResidue, got)
	}

	t.Logf("free-rate %.4f, stream-free-share %.4f (weighted), coverage %.4f over %d detail rows, weighted coverage %.4f",
		sc.FreeRate, sc.StreamFreeShare, sc.Coverage, sc.DetailTotal, sc.WeightedCoverage)
}

// TestCommittedGoldSetFreeExtractionMatchesTheLonePostingStratum is the structural
// invariant behind the whole file: the sampler stratifies with its own walk over
// the page's JSON-LD (stratumOf) while the decorator reads through the domain's
// (crawler.LonePosting). The set of rows the mechanism serves must be EXACTLY the
// lone-posting stratum; any drift between the two walks would silently change what
// the gold set is a sample of.
func TestCommittedGoldSetFreeExtractionMatchesTheLonePostingStratum(t *testing.T) {
	rows, _, err := replayFreeExtraction(t.Context(), filepath.Join("extract-goldset", goldSetFile))
	if err != nil {
		t.Fatalf("replayFreeExtraction: %v", err)
	}
	fired := map[string]bool{}
	for _, row := range rows {
		fired[row.URL] = row.Free
	}

	for _, row := range loadCommittedGoldSet(t) {
		lone := row.Stratum == stratumLonePosting
		switch {
		case fired[row.URL] && !lone:
			t.Errorf("%s: the Free Extraction fired but the sampler put it in stratum %q", row.URL, row.Stratum)
		case !fired[row.URL] && lone:
			t.Errorf("%s: sampled as lone-posting but the Free Extraction did not fire", row.URL)
		}
	}
}

// TestReplayObservesNoModelCall guards the wiring the whole measurement rests on:
// "served free" is observed from a model stub that was never called, not read off
// the extraction's own Free marker. Two synthetic rows are enough -- a lone
// structured posting and a page with none -- so the guard does not need the 2.2 MB
// committed file to prove it.
func TestReplayObservesNoModelCall(t *testing.T) {
	rows := []goldRow{
		{
			URL: "https://a.test/jobs/1", Label: bench.ExtractDetail, Stratum: stratumLonePosting, Weight: 1,
			Content: crawler.Content{Title: "page", JSONLD: []string{lonePostingLD}},
		},
		{
			URL: "https://a.test/about", Label: bench.ExtractResidue, Stratum: stratumNoPosting, Weight: 1,
			Content: crawler.Content{Title: "page", MainContent: "our culture"},
		},
	}
	path := filepath.Join(t.TempDir(), goldSetFile)
	if err := writeGoldSet(path, rows); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}

	got, skipped, err := replayFreeExtraction(t.Context(), path)
	if err != nil {
		t.Fatalf("replayFreeExtraction: %v", err)
	}
	if skipped != 0 || len(got) != 2 {
		t.Fatalf("replayed %d rows (skipped %d), want 2", len(got), skipped)
	}
	// writeGoldSet sorts by URL, so index rather than assume the input order.
	byURL := map[string]bench.FreeExtractionRow{}
	for _, row := range got {
		byURL[row.URL] = row
	}
	posting, other := byURL["https://a.test/jobs/1"], byURL["https://a.test/about"]
	if !posting.Free {
		t.Error("the lone structured posting was handed to the model stub, so it was not served free")
	}
	if posting.Got.Title != "Senior Go Engineer" {
		t.Errorf("free extraction title = %q, want the posting's declared title", posting.Got.Title)
	}
	if other.Free {
		t.Error("a page with no structured data was reported free, but the model stub must have been called")
	}
}
