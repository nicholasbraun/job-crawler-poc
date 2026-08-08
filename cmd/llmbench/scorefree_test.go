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
const freeExtractionFires = 54

// acceptedFreeOnResidue is how many of those rows are labelled residue and carry an
// explicit, per-row acceptance of the fire.
//
// Sixteen of the nineteen fires this number once excused were ads withdrawn while
// their JobPosting JSON-LD kept being served; the predicate was narrowed to refuse
// them (crawler.RendersDeclaredPosting) rather than excuse them, and they no longer
// fire at all.
//
// The three that remain are evergreen talent-pool pages -- "General Application",
// "Spontaneous Application", an internship-enquiry page -- which carry datePosted,
// employmentType, identifier and hiringOrganization exactly as a real posting does.
// No structural signal separates them; only reading them does, which is what the
// model is for. Narrowing further to catch them would start costing real postings,
// so they are accepted as a known, bounded cost: each carries a written reason and
// a human confirmer in the substrate, reviewable in the diff.
//
// RATCHET: only a human may raise this, and only alongside a free_ok note in their
// own words -- scripts/propose-expected.sh never sets one, which is what makes the
// FIRED-ON-NON-POSTING condition real rather than vacuous.
const acceptedFreeOnResidue = 3

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

// TestCommittedGoldSetFreeExtractionIsContainedInTheLonePostingStratum is the
// structural invariant behind the whole file: the sampler stratifies with its own
// walk over the page's JSON-LD (stratumOf) while the decorator reads through the
// domain's (crawler.LonePosting). Every row the mechanism serves must therefore lie
// INSIDE the lone-posting stratum -- a fire outside it means the two walks have
// drifted, and the gold set is no longer a sample of what it claims.
//
// Containment, not equality. The stratum is structural (the page carries one titled
// posting node); firing additionally requires the page to still render the ad it
// declares (ADR-0042). The gap between them is exactly the withdrawn ads, which the
// stratum still samples -- deliberately, since they are what the narrowing must keep
// refusing -- and withdrawnInLonePostingStratum pins how many there are.
const withdrawnInLonePostingStratum = 16

func TestCommittedGoldSetFreeExtractionIsContainedInTheLonePostingStratum(t *testing.T) {
	rows, _, err := replayFreeExtraction(t.Context(), filepath.Join("extract-goldset", goldSetFile))
	if err != nil {
		t.Fatalf("replayFreeExtraction: %v", err)
	}
	fired := map[string]bool{}
	for _, row := range rows {
		fired[row.URL] = row.Free
	}

	var refused int
	for _, row := range loadCommittedGoldSet(t) {
		lone := row.Stratum == stratumLonePosting
		if fired[row.URL] && !lone {
			t.Errorf("%s: the Free Extraction fired but the sampler put it in stratum %q", row.URL, row.Stratum)
		}
		if !fired[row.URL] && lone {
			refused++
		}
	}
	if refused != withdrawnInLonePostingStratum {
		t.Errorf("the mechanism refused %d lone-posting rows, want exactly %d; a change here moves what the Free Extraction covers and must be seen in a diff",
			refused, withdrawnInLonePostingStratum)
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
