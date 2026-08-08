package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// The Extract Gold Set (cmd/llmbench/extract-goldset/goldset.jsonl) is a
// committed sample of pages the LIVE extract stage actually decided on, stored as
// the parsed Content the pipeline itself produced (ADR-0043) -- never re-fetched
// HTML, because a page re-fetched months later is a different page and the drift
// corrupts the label.
//
// It holds TWO DRAWINGS. They share a row format and a file; they share nothing
// else, and their weights are never pooled (goldStratum.Drawing).
//
// Drawing 1 -- STRUCTURAL (#254), 149 rows. From the extract-decision tap's local
// capture (EXTRACT_CAPTURE_PATH=<repo>/capture/extract-capture.jsonl, gitignored),
// 5669 records captured 2026-07-24T22:51Z .. 2026-08-06T21:27Z across three
// collection sessions; deduped by URL to 4271 pages (latest capture wins, 3
// oversized lines dropped); sampled 2026-08-07 by `llmbench goldset-sample -seed
// extract-goldset-v1` under samplePlan, stratified on the ADR-0042 lone-posting
// predicate, accept share 0.3432. Labels were PROPOSED BY AN LLM (the #254
// delivery agent) from each page's own title, text and link structure with the
// structured data deliberately withheld, and are confirmed by a human on the
// lone-posting stratum -- the rows a later guard is decided on.
//
// Drawing 2 -- RANDOM (#262), 120 rows. From the SAME capture file, grown to
// 11772 records, narrowed to the faithful frame at or after 2026-08-07T21:13Z (the
// first record after `fceaf87`, the last parser change; earlier windows were parsed
// by a parser that no longer exists). That frame is 5204 distinct URLs over
// 2026-08-07T21:13:23Z .. 2026-08-08T11:43:46Z; minus the 42 URLs drawing 1 already
// committed it is 5162 = 1762 accept / 3400 abstain. Sampled by `llmbench
// goldset-sample-random -seed extract-goldset-random-v1 -since 2026-08-07T21:13:00Z
// -accept-share 0.0753` under randomSamplePlan, whose only cell is the verdict, at
// 40 accept / 80 abstain -- 2.27% and 2.35% of their cells, so it is within a
// percentage point of a simple random sample and the weights carry the per-verdict
// cap correction and nothing else. The accept share is #261's census measurement of
// the live stream (285 accepts in 3785 decisions, pooled over the capture's six
// process frames' pre-cap prefixes), never the file's own 0.4265 mix.
//
// ESTIMAND. The capture tap sits DOWNSTREAM of the Extract Gate, so the population
// the random drawing describes is the stream of extractor decisions the Collection
// Crawl produced in that window -- pages that had ALREADY cleared the gate and been
// paid for. Every weighted number estimates today's extract bill. It says nothing
// about pages the gate already rejects: they never reach the tap.
//
// Each row records its labelling in label_provenance. The structural drawing's
// lone-posting stratum is fully human-confirmed (pendingHumanConfirmations); the
// random stratum is SPOT-CHECKED instead (randomSpotChecks), which is what ADR-0043
// asks of it.

// pendingHumanConfirmations is how many lone-posting rows still carry an
// LLM-proposed label no human has confirmed. #254 requires 0.
//
// It is 1. The confirmation pass found three pages carrying a closure banner above an
// otherwise complete posting; they were relabelled residue and the mechanism now
// refuses them by that banner, so their labels are confirmed and they no longer fire.
// What remains is one open-application page labelled detail where the same shape is
// labelled residue elsewhere in the set -- a labelling inconsistency the set should
// answer the same way twice, written up in the Extract Gold Set README. RATCHET:
// lower it as confirmations land, never raise it.
const pendingHumanConfirmations = 1

// pendingExpectedConfirmations is how many rows carry an agent-proposed expected
// extraction that no human has confirmed. #256 requires 0: the values were read off
// each page's own JSON-LD by scripts/propose-expected.sh and a human confirms them
// at the review gate. It is 1, the same row pendingHumanConfirmations holds back: an
// expected extraction cannot be confirmed while the label it is scored under is in
// question. Like pendingHumanConfirmations it is a RATCHET -- lower it as
// confirmations land, never raise it.
const pendingExpectedConfirmations = 1

const (
	// structuralStratumRows and randomStratumRows are the two drawings' exact row
	// counts. They are pinned rather than bounded because a drawing is a fixed act of
	// sampling: a row appearing or vanishing changes what every weighted number
	// estimates, and must be seen in a diff.
	structuralStratumRows = 149
	randomStratumRows     = 120
	// randomStreamAcceptRate is the accept share the random drawing's weights were
	// built on -- #261's census measurement of the live extract stream, not the
	// capture file's own mix. TestCommittedRandomStratumIsWeightedToTheStream
	// reconstructs it from the committed weights, which is what proves they map the
	// sample back to the recorded stream rather than to the file.
	randomStreamAcceptRate = 0.0753
)

// randomSpotChecks is how many random-stratum rows a human has actually read and
// confirmed. ADR-0043 asks this stratum to be SPOT-CHECKED rather than fully
// confirmed, so unlike pendingHumanConfirmations this is a ratchet that RISES: it is
// the machine-visible record of what has been checked, and what is therefore still
// owed. It is 0 -- the labels ship LLM-proposed, and extract-goldset/README.md tells
// the reviewer exactly which 20 rows to read and how to raise it.
const randomSpotChecks = 0

// loadCommittedGoldSet reads the committed Extract Gold Set. The working directory
// under `go test` is the package directory, so the relative path resolves without
// configuration.
func loadCommittedGoldSet(t *testing.T) []goldRow {
	t.Helper()
	rows, err := readGoldSet(filepath.Join("extract-goldset", goldSetFile))
	if err != nil {
		t.Fatalf("read committed gold set: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("committed gold set is empty, want the sampled rows")
	}
	return rows
}

// capturedPage builds one extract-capture JSONL line the way the live tap writes
// it, so the sampler tests read the same shape production produces.
func capturedPage(t *testing.T, url string, verdict bool, ts string, jsonLD []string, mainContent string) string {
	t.Helper()
	rec := struct {
		URL     string          `json:"url"`
		Verdict bool            `json:"verdict"`
		TS      string          `json:"ts"`
		Content crawler.Content `json:"content"`
	}{URL: url, Verdict: verdict, TS: ts, Content: crawler.Content{Title: "page", MainContent: mainContent, JSONLD: jsonLD}}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal capture line: %v", err)
	}
	return string(line)
}

// writeCapture writes lines as a capture file in a temp dir and returns its path.
func writeCapture(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "capture.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write capture: %v", err)
	}
	return path
}

const lonePostingLD = `{"@context":"https://schema.org","@type":"JobPosting","title":"Senior Go Engineer"}`

// TestStratifyCandidates pins the structural stratum a page is sampled into. The
// stratum decides which rows the mechanism's guard is built from, so every shape
// the ADR-0042 lone-posting predicate accepts or refuses is asserted here.
func TestStratifyCandidates(t *testing.T) {
	tests := []struct {
		name   string
		blocks []string
		want   goldStratum
	}{
		{"no structured data at all", nil, stratumNoPosting},
		{"structured data with no posting", []string{`{"@type":"Organization","name":"Acme"}`}, stratumNoPosting},
		{"lone posting with a title", []string{lonePostingLD}, stratumLonePosting},
		{"lone posting nested in @graph", []string{`{"@graph":[{"@type":"WebPage"},{"@type":"JobPosting","title":"Cook"}]}`}, stratumLonePosting},
		{"type as an array", []string{`{"@type":["JobPosting"],"title":"Cook"}`}, stratumLonePosting},
		{"type as a schema.org URL", []string{`{"@type":"https://schema.org/JobPosting","title":"Cook"}`}, stratumLonePosting},
		{"two postings are an openings index", []string{`[{"@type":"JobPosting","title":"A"},{"@type":"JobPosting","title":"B"}]`}, stratumAmbiguousPosting},
		{"two postings across separate blocks", []string{lonePostingLD, `{"@type":"JobPosting","title":"B"}`}, stratumAmbiguousPosting},
		{"ItemList wrapping one posting", []string{`{"@type":"ItemList","itemListElement":[{"@type":"ListItem","item":{"@type":"JobPosting","title":"A"}}]}`}, stratumAmbiguousPosting},
		{"lone posting with no title", []string{`{"@type":"JobPosting","description":"we are hiring"}`}, stratumAmbiguousPosting},
		{"lone posting with a blank title", []string{`{"@type":"JobPosting","title":"   "}`}, stratumAmbiguousPosting},
		{"lone posting with a non-string title", []string{`{"@type":"JobPosting","title":42}`}, stratumAmbiguousPosting},
		{"unparseable block contributes nothing", []string{`{"@type":"JobPosting",`}, stratumNoPosting},
		{"unparseable block beside a real posting", []string{`not json`, lonePostingLD}, stratumLonePosting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stratumOf(tt.blocks); got != tt.want {
				t.Errorf("stratumOf = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSampleAppliesThePlan proves the realized sample is the plan applied to the
// real populations: a cell smaller than its quota is taken whole, a larger cell is
// truncated to its quota, and the total is the sum of what each cell could give.
func TestSampleAppliesThePlan(t *testing.T) {
	lines := []string{}
	for i := range 10 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/jobs/%d", i), true, "2026-08-01T10:00:00Z", []string{lonePostingLD}, "apply now"))
	}
	for i := range 2 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/about/%d", i), false, "2026-08-01T10:00:01Z", nil, "our culture"))
	}
	scan, err := scanCapture(writeCapture(t, lines...))
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}

	plan := []cellPlan{
		{stratumLonePosting, true, 3},      // population 10, truncated
		{stratumLonePosting, false, 0},     // population 0
		{stratumAmbiguousPosting, true, 0}, // population 0
		{stratumAmbiguousPosting, false, 0},
		{stratumNoPosting, true, 0},
		{stratumNoPosting, false, 5}, // population 2, taken whole
	}
	sel, err := applyPlan(scan, plan, defaultSeed, 0.5)
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	if len(sel.Chosen) != 5 {
		t.Fatalf("chose %d rows, want 5 (3 truncated + 2 whole)", len(sel.Chosen))
	}
	got := map[cellKey]cellResult{}
	for _, c := range sel.Cells {
		got[c.Key] = c
	}
	if c := got[cellKey{stratumLonePosting, true}]; c.Population != 10 || c.Sampled != 3 {
		t.Errorf("lone-posting/accept: population %d sampled %d, want 10 and 3", c.Population, c.Sampled)
	}
	if c := got[cellKey{stratumNoPosting, false}]; c.Population != 2 || c.Sampled != 2 {
		t.Errorf("no-posting/abstain: population %d sampled %d, want 2 and 2", c.Population, c.Sampled)
	}
}

// TestSampleRejectsAnUncoveredCell proves an incomplete plan fails loudly. The
// weights normalize over the plan, so a candidate in an uncovered cell would
// silently drop stream mass and quietly bias every weighted number.
func TestSampleRejectsAnUncoveredCell(t *testing.T) {
	scan, err := scanCapture(writeCapture(t,
		capturedPage(t, "https://acme.test/jobs/1", true, "2026-08-01T10:00:00Z", []string{lonePostingLD}, "apply"),
	))
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}
	if _, err := applyPlan(scan, []cellPlan{{stratumNoPosting, true, 1}}, defaultSeed, 0.5); err == nil {
		t.Fatal("applyPlan accepted a plan that does not cover the lone-posting/accept cell")
	}
}

// TestSampleIsDeterministic proves the committed file is reproducible: the same
// capture and seed always select the same rows, so a reviewer can regenerate it
// and diff, while a new seed is a genuine resample.
func TestSampleIsDeterministic(t *testing.T) {
	lines := []string{}
	for i := range 40 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/jobs/%d", i), true, "2026-08-01T10:00:00Z", []string{lonePostingLD}, "apply now"))
	}
	path := writeCapture(t, lines...)
	plan := []cellPlan{
		{stratumLonePosting, true, 8},
		{stratumLonePosting, false, 0},
		{stratumAmbiguousPosting, true, 0},
		{stratumAmbiguousPosting, false, 0},
		{stratumNoPosting, true, 0},
		{stratumNoPosting, false, 0},
	}

	urlsFor := func(seed string) []string {
		scan, err := scanCapture(path)
		if err != nil {
			t.Fatalf("scanCapture: %v", err)
		}
		sel, err := applyPlan(scan, plan, seed, 0.5)
		if err != nil {
			t.Fatalf("applyPlan: %v", err)
		}
		urls := []string{}
		for _, c := range sel.Chosen {
			urls = append(urls, c.URL)
		}
		return urls
	}

	first, second := urlsFor("seed-a"), urlsFor("seed-a")
	if strings.Join(first, "\n") != strings.Join(second, "\n") {
		t.Errorf("same seed selected different rows:\n%v\n%v", first, second)
	}
	if other := urlsFor("seed-b"); strings.Join(first, "\n") == strings.Join(other, "\n") {
		t.Error("a different seed selected the identical rows, so the seed does not resample")
	}
}

// TestSampleDedupesByURL proves a URL the crawler re-extracted across cycles
// contributes one row, and that the surviving row is the LATEST capture -- the
// parse closest to today's parser.
func TestSampleDedupesByURL(t *testing.T) {
	const url = "https://acme.test/jobs/repeat"
	path := writeCapture(t,
		capturedPage(t, url, false, "2026-08-01T10:00:00Z", nil, "first parse"),
		capturedPage(t, url, true, "2026-08-03T10:00:00Z", nil, "latest parse"),
		capturedPage(t, url, false, "2026-08-02T10:00:00Z", nil, "middle parse"),
	)
	scan, err := scanCapture(path)
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}
	if len(scan.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 after dedupe", len(scan.Candidates))
	}
	if scan.Duplicates != 2 {
		t.Errorf("duplicates = %d, want 2", scan.Duplicates)
	}
	if got := scan.Candidates[0]; got.TS != "2026-08-03T10:00:00Z" || !got.Verdict {
		t.Errorf("surviving candidate ts=%q verdict=%v, want the 2026-08-03 accept", got.TS, got.Verdict)
	}

	plan := []cellPlan{
		{stratumLonePosting, true, 0}, {stratumLonePosting, false, 0},
		{stratumAmbiguousPosting, true, 0}, {stratumAmbiguousPosting, false, 0},
		{stratumNoPosting, true, 0}, {stratumNoPosting, false, 0},
	}
	sel, err := applyPlan(scan, plan, defaultSeed, 0.5)
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	rows, err := readSelected(path, sel)
	if err != nil {
		t.Fatalf("readSelected: %v", err)
	}
	if len(rows) != 1 || rows[0].Content.MainContent != "latest parse" {
		t.Fatalf("materialized %d rows with content %q, want the latest parse", len(rows), rows[0].Content.MainContent)
	}
}

// TestSampleDropsOversizedAndUnparseableRows proves a harvest artifact is dropped
// and COUNTED rather than either bloating the committed file or aborting the
// sample. A silent drop would bias the file invisibly, which is why the counts are
// printed.
func TestSampleDropsOversizedAndUnparseableRows(t *testing.T) {
	scan, err := scanCapture(writeCapture(t,
		capturedPage(t, "https://acme.test/jobs/ok", true, "2026-08-01T10:00:00Z", []string{lonePostingLD}, "apply"),
		capturedPage(t, "https://acme.test/jobs/huge", true, "2026-08-01T10:00:01Z", []string{lonePostingLD}, strings.Repeat("x", 600*1024)),
		capturedPage(t, "://not a url", false, "2026-08-01T10:00:02Z", nil, "junk"),
	))
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}
	if scan.Oversized != 1 {
		t.Errorf("oversized = %d, want 1", scan.Oversized)
	}
	if scan.BadURL != 1 {
		t.Errorf("bad url = %d, want 1", scan.BadURL)
	}
	if len(scan.Candidates) != 1 || scan.Candidates[0].URL != "https://acme.test/jobs/ok" {
		t.Errorf("candidates = %v, want only the well-formed page", scan.Candidates)
	}
}

// TestLiveAcceptShareIgnoresTheCappedTail is the guard on ADR-0043's weighting
// rule: weights derive from the capture's per-verdict CAPS, never from the file's
// own accept/abstain mix. Once one verdict has hit its cap the file is all the
// other verdict, so the raw mix is the sampling design showing through -- the
// estimator must cut the timeline there.
func TestLiveAcceptShareIgnoresTheCappedTail(t *testing.T) {
	base := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

	t.Run("the tail after a cap is dropped", func(t *testing.T) {
		seq := []tsVerdict{}
		for i := range 100 {
			seq = append(seq, tsVerdict{TS: at(i), Verdict: true}, tsVerdict{TS: at(i), Verdict: false})
		}
		// The abstain cap has bound: every later record is an accept.
		for i := range 400 {
			seq = append(seq, tsVerdict{TS: at(100 + i/10), Verdict: true})
		}
		share, ok := liveAcceptShare(seq)
		if !ok {
			t.Fatal("liveAcceptShare could not estimate a share")
		}
		if math.Abs(share-0.5) > 1e-9 {
			t.Errorf("share = %.4f, want 0.5 (the raw file mix is %.4f, which the caps produced)", share, 500.0/700.0)
		}
	})

	t.Run("a gap starts a new session", func(t *testing.T) {
		// The tap appends across process restarts and its caps reset with each, so
		// a cap must be found per session. Two identical sessions, each an
		// alternating A/F run whose abstains then cap: cutting at the last abstain
		// keeps 4 rows with 2 accepts, so each session -- and the pooled estimate --
		// is 0.5.
		session := func(offset int) []tsVerdict {
			return []tsVerdict{
				{TS: at(offset + 0), Verdict: true}, {TS: at(offset + 1), Verdict: false},
				{TS: at(offset + 2), Verdict: true}, {TS: at(offset + 3), Verdict: false},
				{TS: at(offset + 4), Verdict: true}, {TS: at(offset + 5), Verdict: true},
			}
		}
		seq := append(session(0), session(500)...)

		share, ok := liveAcceptShare(seq)
		if !ok {
			t.Fatal("liveAcceptShare could not estimate a share")
		}
		if math.Abs(share-0.5) > 1e-9 {
			t.Errorf("share = %.4f, want 0.5", share)
		}
		if one, _ := liveAcceptShare(session(0)); math.Abs(one-0.5) > 1e-9 {
			t.Errorf("session 1 alone = %.4f, want 0.5", one)
		}
		// Collapsing the gap merges the two sessions into one, which moves the cap
		// cutoff past the first session's capped tail and inflates the estimate.
		merged := append(session(0), session(6)...)
		if got, _ := liveAcceptShare(merged); math.Abs(got-0.5) <= 1e-9 {
			t.Errorf("merging the sessions gave the same %.4f; the session split is doing nothing", got)
		}
	})

	t.Run("a single-verdict capture cannot be estimated", func(t *testing.T) {
		seq := []tsVerdict{{TS: at(0), Verdict: true}, {TS: at(1), Verdict: true}}
		if _, ok := liveAcceptShare(seq); ok {
			t.Error("estimated a share from a capture with no abstain; want ok=false so the caller must supply one")
		}
	})
}

// TestWeightsSumToRowCountAndInvertOversampling proves the weights are inverse
// inclusion probabilities: they total the row count (so a weighted mean is a mean),
// and the exhaustively-sampled rare cell counts for LESS per row than the thinly
// sampled common one, which is the whole point of carrying them.
func TestWeightsSumToRowCountAndInvertOversampling(t *testing.T) {
	// The populations and quotas realized on the committed capture.
	pops := map[cellKey]int{
		{stratumLonePosting, true}:       777,
		{stratumLonePosting, false}:      24,
		{stratumAmbiguousPosting, true}:  0,
		{stratumAmbiguousPosting, false}: 1,
		{stratumNoPosting, true}:         1037,
		{stratumNoPosting, false}:        2432,
	}
	counts := map[cellKey]int{
		{stratumLonePosting, true}:       46,
		{stratumLonePosting, false}:      24,
		{stratumAmbiguousPosting, true}:  0,
		{stratumAmbiguousPosting, false}: 1,
		{stratumNoPosting, true}:         33,
		{stratumNoPosting, false}:        45,
	}
	weights, err := weightsFor(samplePlan, pops, counts, 0.3432)
	if err != nil {
		t.Fatalf("weightsFor: %v", err)
	}

	total, rows := 0.0, 0
	for key, n := range counts {
		total += weights[key] * float64(n)
		rows += n
	}
	if math.Abs(total-float64(rows)) > 1e-6 {
		t.Errorf("weights sum to %.6f over %d rows, want equal", total, rows)
	}

	rare := weights[cellKey{stratumLonePosting, false}] // 24 of 24 taken
	common := weights[cellKey{stratumNoPosting, false}] // 45 of 2432 taken
	if !(rare > 0 && common > 0 && rare < common) {
		t.Errorf("exhaustive cell weight %.4f is not below the thinly-sampled cell weight %.4f", rare, common)
	}

	t.Run("an out-of-range accept share is rejected", func(t *testing.T) {
		for _, share := range []float64{0, 1, -0.2, 1.5} {
			if _, err := weightsFor(samplePlan, pops, counts, share); err == nil {
				t.Errorf("weightsFor accepted accept-share %g", share)
			}
		}
	})
}

// TestFrameSinceAndExclusionNarrowTheCandidates covers the two narrowings the #262
// drawing rests on. The frame cutoff is what keeps content parsed by a superseded
// parser out of a file whose whole premise is that a captured page is the exact
// bytes the gate will later see; the exclusion is what stops one page carrying two
// drawings' incompatible weights.
func TestFrameSinceAndExclusionNarrowTheCandidates(t *testing.T) {
	const cutoffText = "2026-08-07T21:13:00Z"
	cutoff, err := time.Parse(time.RFC3339, cutoffText)
	if err != nil {
		t.Fatalf("parse cutoff: %v", err)
	}

	scan, err := scanCapture(writeCapture(t,
		capturedPage(t, "https://acme.test/jobs/stale", true, "2026-08-06T10:00:00Z", nil, "parsed by the old parser"),
		capturedPage(t, "https://acme.test/jobs/fresh", true, "2026-08-07T22:00:00Z", nil, "fresh parse"),
		// Captured on both sides: scanCapture keeps the LATEST record, so it survives
		// the cutoff carrying today's parse.
		capturedPage(t, "https://acme.test/jobs/both", false, "2026-08-06T09:00:00Z", nil, "old parse"),
		capturedPage(t, "https://acme.test/jobs/both", true, "2026-08-08T09:00:00Z", nil, "new parse"),
		capturedPage(t, "https://acme.test/jobs/undated", false, "not-a-timestamp", nil, "unplaceable in time"),
		capturedPage(t, "https://acme.test/jobs/committed", true, "2026-08-08T10:00:00Z", nil, "already in the substrate"),
	))
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}

	framed, outOfFrame := frameSince(scan, cutoff)
	if outOfFrame != 2 {
		t.Errorf("dropped %d candidates out of frame, want 2 (the stale page and the undated one)", outOfFrame)
	}
	inFrame := map[string]string{}
	for _, c := range framed.Candidates {
		inFrame[c.URL] = c.TS
	}
	if _, ok := inFrame["https://acme.test/jobs/stale"]; ok {
		t.Error("a page captured only before the cutoff survived the frame")
	}
	if _, ok := inFrame["https://acme.test/jobs/undated"]; ok {
		t.Error("a page whose ts does not parse survived the frame")
	}
	if got := inFrame["https://acme.test/jobs/both"]; got != "2026-08-08T09:00:00Z" {
		t.Errorf("the both-sides page kept ts %q, want its post-cutoff parse", got)
	}

	// readSelected re-reads the capture by line number, so a narrowing that lost the
	// line would materialize the wrong page (or none).
	for _, c := range framed.Candidates {
		if c.Line == 0 {
			t.Errorf("%s: capture line number lost by the narrowing", c.URL)
		}
	}

	kept, excluded := excludingURLs(framed, map[string]struct{}{"https://acme.test/jobs/committed": {}})
	if excluded != 1 {
		t.Errorf("excluded %d already-committed candidates, want 1", excluded)
	}
	for _, c := range kept.Candidates {
		if c.URL == "https://acme.test/jobs/committed" {
			t.Error("an already-committed url survived the exclusion and would carry two weights")
		}
	}
	if len(kept.Candidates) != 2 {
		t.Errorf("candidate frame is %d rows, want 2", len(kept.Candidates))
	}
}

// TestRandomPlanWeightsReconstructTheStreamRate is the money property of the random
// drawing: the weights come from the MEASURED stream accept rate and the quotas, so
// applying them to the sample reconstructs that rate rather than the capture file's
// own cap-distorted mix.
func TestRandomPlanWeightsReconstructTheStreamRate(t *testing.T) {
	lines := []string{}
	for i := range 100 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/jobs/%d", i), true, "2026-08-08T10:00:00Z", []string{lonePostingLD}, "apply now"))
	}
	for i := range 200 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/about/%d", i), false, "2026-08-08T10:00:01Z", nil, "our culture"))
	}
	scan, err := scanCapture(writeCapture(t, lines...))
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}

	// The same 1:2 accept:abstain quota shape randomSamplePlan uses, one tenth the
	// size, over a capture whose own mix (1:2) is nothing like the stream's.
	plan := []cellPlan{{stratumRandom, true, 10}, {stratumRandom, false, 20}}
	const p = randomStreamAcceptRate
	sel, err := applyPlan(asStratum(scan, stratumRandom), plan, defaultRandomSeed, p)
	if err != nil {
		t.Fatalf("applyPlan: %v", err)
	}
	if sel.AcceptShareEstimated {
		t.Error("applyPlan reconstructed the accept share despite being handed one")
	}

	got := map[cellKey]cellResult{}
	for _, c := range sel.Cells {
		got[c.Key] = c
	}
	// With a 1:2 draw the weights collapse to 3p and 1.5(1-p).
	accept, abstain := got[cellKey{stratumRandom, true}], got[cellKey{stratumRandom, false}]
	if math.Abs(accept.Weight-3*p) > 1e-9 {
		t.Errorf("accept weight %.6f, want %.6f (3p)", accept.Weight, 3*p)
	}
	if math.Abs(abstain.Weight-1.5*(1-p)) > 1e-9 {
		t.Errorf("abstain weight %.6f, want %.6f (1.5(1-p))", abstain.Weight, 1.5*(1-p))
	}
	total := accept.Weight*float64(accept.Sampled) + abstain.Weight*float64(abstain.Sampled)
	if math.Abs(total-30) > 1e-9 {
		t.Errorf("weights sum to %.6f over 30 rows, want equal", total)
	}
	// The reconstruction, which is the whole reason the weights exist: the file's own
	// accept share here is 1/3, the stream's is p, and the weighted share is p.
	weighted := accept.Weight * float64(accept.Sampled) / total
	if math.Abs(weighted-p) > 1e-9 {
		t.Errorf("weighted accept share %.6f, want the measured stream rate %.6f (the file's own is %.4f)", weighted, p, 1.0/3.0)
	}
}

// TestRandomSampleRefusesAnUnsuppliedAcceptShare guards the one decision the random
// verb must never make for its operator. liveAcceptShare splits capture sessions on
// a one-hour ts gap, and three of the #261 capture's six process frames are
// contiguous within that, so the estimator would merge them and produce a WRONG
// number with no sign that anything went wrong. A refusal is the only safe default.
func TestRandomSampleRefusesAnUnsuppliedAcceptShare(t *testing.T) {
	dir := t.TempDir()
	capture := writeCapture(t,
		capturedPage(t, "https://acme.test/jobs/1", true, "2026-08-08T10:00:00Z", []string{lonePostingLD}, "apply"),
		capturedPage(t, "https://acme.test/about", false, "2026-08-08T10:00:01Z", nil, "culture"),
	)
	for name, args := range map[string][]string{
		"no accept share at all": {"-capture", capture, "-dir", dir, "-since", "2026-08-07T21:13:00Z"},
		"zero":                   {"-capture", capture, "-dir", dir, "-since", "2026-08-07T21:13:00Z", "-accept-share", "0"},
		"one":                    {"-capture", capture, "-dir", dir, "-since", "2026-08-07T21:13:00Z", "-accept-share", "1"},
		"negative":               {"-capture", capture, "-dir", dir, "-since", "2026-08-07T21:13:00Z", "-accept-share", "-0.5"},
		"no cutoff":              {"-capture", capture, "-dir", dir, "-accept-share", "0.0753"},
	} {
		if code := runGoldSetSampleRandom(args); code != 2 {
			t.Errorf("%s: exit code %d, want 2", name, code)
		}
		if _, err := os.Stat(filepath.Join(dir, goldSetFile)); !os.IsNotExist(err) {
			t.Errorf("%s: the verb wrote a substrate despite refusing the draw", name)
		}
	}
}

// TestRandomSampleAppendsToTheExistingSubstrate proves the verb is append-only with
// respect to labels. goldset-sample is destructive by design -- it rebuilds the file
// and drops every label -- and the random drawing extends a file that cost a human
// review pass, so an accidental rebuild there would be expensive and silent.
func TestRandomSampleAppendsToTheExistingSubstrate(t *testing.T) {
	dir := t.TempDir()
	existing := []goldRow{{
		URL: "https://acme.test/jobs/already", Verdict: true, TS: "2026-08-08T09:00:00Z",
		Stratum: stratumNoPosting, Weight: 1, Label: bench.ExtractDetail,
		LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-08T09:00:00Z", ConfirmedBy: "A Human", ConfirmedAt: "2026-08-08T09:00:00Z"},
		Content:         crawler.Content{Title: "already labelled"},
	}}
	if err := writeGoldSet(filepath.Join(dir, goldSetFile), existing); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}

	lines := []string{
		// The same URL the substrate already carries: it must be excluded, not redrawn.
		capturedPage(t, "https://acme.test/jobs/already", true, "2026-08-08T10:00:00Z", nil, "re-visited"),
		// Out of frame: parsed by a superseded parser.
		capturedPage(t, "https://acme.test/jobs/stale", true, "2026-08-01T10:00:00Z", nil, "old parse"),
	}
	for i := range 40 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/jobs/%d", i), true, "2026-08-08T10:00:00Z", []string{lonePostingLD}, "apply now"))
	}
	for i := range 80 {
		lines = append(lines, capturedPage(t, fmt.Sprintf("https://acme.test/about/%d", i), false, "2026-08-08T10:00:01Z", nil, "our culture"))
	}
	capture := writeCapture(t, lines...)

	code := runGoldSetSampleRandom([]string{
		"-capture", capture, "-dir", dir, "-since", "2026-08-07T21:13:00Z", "-accept-share", "0.0753",
	})
	if code != 0 {
		t.Fatalf("goldset-sample-random exit code %d, want 0", code)
	}

	merged, err := readGoldSet(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	if len(merged) != 1+randomStratumRows {
		t.Fatalf("substrate has %d rows, want %d (1 existing + %d drawn)", len(merged), 1+randomStratumRows, randomStratumRows)
	}
	drawn := []goldRow{}
	for _, row := range merged {
		if row.URL == existing[0].URL {
			if row.Label != bench.ExtractDetail || row.LabelProvenance.ConfirmedBy != "A Human" || row.Stratum != stratumNoPosting {
				t.Errorf("the existing row was rewritten by the draw: %+v", row)
			}
			continue
		}
		drawn = append(drawn, row)
		if row.Stratum != stratumRandom {
			t.Errorf("%s: drawn in stratum %q, want %q", row.URL, row.Stratum, stratumRandom)
		}
		if row.Label != "" {
			t.Errorf("%s: a fresh draw arrived labelled %q", row.URL, row.Label)
		}
	}
	if !weightsBalanced(drawn, 1e-6) {
		t.Errorf("the drawn rows' weights sum to %.6f over %d rows, want equal", weightSum(drawn), len(drawn))
	}
}

// TestSheetRoundTrip proves the human review surface is lossless for the fields it
// owns, and that a page title carrying a tab or a newline still yields exactly one
// parseable line -- a captured title is arbitrary page text.
func TestSheetRoundTrip(t *testing.T) {
	rows := []goldRow{
		{
			URL: "https://b.test/jobs/2", Verdict: false, Stratum: stratumNoPosting, Label: bench.ExtractResidue,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ConfirmedBy: "A Human", Note: "culture page"},
			Content:         crawler.Content{Title: "Careers\tat\nB Corp"},
		},
		{
			URL: "https://a.test/jobs/1", Verdict: true, Stratum: stratumLonePosting, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", Note: "single role"},
			Content:         crawler.Content{Title: "Senior Go Engineer"},
		},
	}

	data := renderSheet(rows)
	if lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1; lines != 3 {
		t.Fatalf("sheet has %d lines, want 1 header + 2 rows", lines)
	}
	parsed, err := parseSheet(data)
	if err != nil {
		t.Fatalf("parseSheet: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(parsed))
	}
	if parsed[0].URL != "https://a.test/jobs/1" {
		t.Errorf("sheet is not sorted by url: first row is %q", parsed[0].URL)
	}
	want := sheetRow{
		ID: rowID("https://a.test/jobs/1"), URL: "https://a.test/jobs/1", Stratum: stratumLonePosting,
		Verdict: true, Label: bench.ExtractDetail, ProposedBy: "llm:test", Note: "single role",
	}
	if parsed[0] != want {
		t.Errorf("round-tripped row = %+v, want %+v", parsed[0], want)
	}
	if parsed[1].ConfirmedBy != "A Human" {
		t.Errorf("confirmer = %q, want %q", parsed[1].ConfirmedBy, "A Human")
	}

	t.Run("a malformed sheet is rejected", func(t *testing.T) {
		for name, bad := range map[string]string{
			"too few columns": sheetHeader + "\nid\turl\tno-posting\ttrue\n",
			"bad verdict":     sheetHeader + "\nid\turl\tno-posting\tmaybe\tdetail\t\t\t\t\n",
			"unknown label":   sheetHeader + "\nid\turl\tno-posting\ttrue\tposting\t\t\t\t\n",
		} {
			if _, err := parseSheet([]byte(bad)); err == nil {
				t.Errorf("parseSheet accepted a sheet with %s", name)
			}
		}
	})
}

// TestApplyMergesLabelsAndStampsProvenance covers the merge an operator drives:
// labels land, provenance is stamped truthfully and never rewritten, a confirmation
// can be scoped to the stratum a human actually reviewed, and every disagreement
// with the substrate aborts the whole merge rather than half-applying it.
func TestApplyMergesLabelsAndStampsProvenance(t *testing.T) {
	const stamp = "2026-08-07T12:00:00Z"
	base := func() []goldRow {
		return []goldRow{
			{URL: "https://a.test/jobs/1", Verdict: true, Stratum: stratumLonePosting},
			{URL: "https://b.test/careers", Verdict: false, Stratum: stratumNoPosting},
		}
	}
	sheetFor := func(rows []goldRow) []sheetRow {
		parsed, err := parseSheet(renderSheet(rows))
		if err != nil {
			t.Fatalf("parseSheet: %v", err)
		}
		return parsed
	}

	t.Run("a proposal lands with its proposer and note", func(t *testing.T) {
		rows := base()
		proposals := []sheetRow{{ID: rowID("https://a.test/jobs/1"), Label: bench.ExtractDetail, Note: "one role, apply button"}}
		got, err := applyLabels(rows, sheetFor(rows), proposals, "llm:test-agent", "", "", stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		if got[0].Label != bench.ExtractDetail {
			t.Errorf("label = %q, want detail", got[0].Label)
		}
		if got[0].LabelProvenance.ProposedBy != "llm:test-agent" || got[0].LabelProvenance.ProposedAt != stamp {
			t.Errorf("provenance = %+v, want the agent stamped at %s", got[0].LabelProvenance, stamp)
		}
		if got[0].LabelProvenance.ConfirmedBy != "" {
			t.Error("a proposal must never fill confirmed_by")
		}
		if got[1].LabelProvenance.ProposedBy != "" {
			t.Error("an unlabelled row must not be stamped with a proposer")
		}
	})

	t.Run("an existing proposer is never overwritten or re-stamped", func(t *testing.T) {
		rows := base()
		rows[0].Label = bench.ExtractDetail
		rows[0].LabelProvenance = goldProvenance{ProposedBy: "llm:first", ProposedAt: "2026-01-01T00:00:00Z"}
		got, err := applyLabels(rows, sheetFor(rows), nil, "llm:second", "", "", stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		if got[0].LabelProvenance.ProposedBy != "llm:first" || got[0].LabelProvenance.ProposedAt != "2026-01-01T00:00:00Z" {
			t.Errorf("provenance = %+v, want the original proposer untouched", got[0].LabelProvenance)
		}
	})

	t.Run("confirmation is scoped to one stratum", func(t *testing.T) {
		rows := base()
		rows[0].Label, rows[1].Label = bench.ExtractDetail, bench.ExtractResidue
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "A Human", stratumLonePosting, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		if got[0].LabelProvenance.ConfirmedBy != "A Human" || got[0].LabelProvenance.ConfirmedAt != stamp {
			t.Errorf("lone-posting row not confirmed: %+v", got[0].LabelProvenance)
		}
		if got[1].LabelProvenance.ConfirmedBy != "" {
			t.Errorf("no-posting row was confirmed outside the requested stratum: %+v", got[1].LabelProvenance)
		}
	})

	t.Run("an unlabelled row is never confirmed", func(t *testing.T) {
		rows := base()
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "A Human", "", stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		for _, row := range got {
			if row.LabelProvenance.ConfirmedBy != "" {
				t.Errorf("%s confirmed with no label", row.URL)
			}
		}
	})

	t.Run("a disagreeing or unknown sheet aborts the merge", func(t *testing.T) {
		rows := base()
		good := sheetFor(rows)

		staleStratum := append([]sheetRow(nil), good...)
		staleStratum[0].Stratum = stratumNoPosting

		staleVerdict := append([]sheetRow(nil), good...)
		staleVerdict[0].Verdict = !staleVerdict[0].Verdict

		unknown := append([]sheetRow(nil), good...)
		unknown[0].URL, unknown[0].ID = "https://gone.test/x", rowID("https://gone.test/x")

		duplicate := append(append([]sheetRow(nil), good...), good[0])

		for name, sheet := range map[string][]sheetRow{
			"stratum disagrees": staleStratum,
			"verdict disagrees": staleVerdict,
			"unknown url":       unknown,
			"duplicate row":     duplicate,
		} {
			before := base()
			if _, err := applyLabels(before, sheet, nil, "llm:test", "", "", stamp); err == nil {
				t.Errorf("applyLabels accepted a sheet where %s", name)
			}
			if before[0].Label != "" || before[0].LabelProvenance.ProposedBy != "" {
				t.Errorf("%s: the caller's rows were mutated by a failed merge", name)
			}
		}

		if _, err := applyLabels(rows, good, []sheetRow{{ID: "deadbeefdead", Label: bench.ExtractDetail}}, "", "", "", stamp); err == nil {
			t.Error("applyLabels accepted a proposal for an unknown row id")
		}
	})

	t.Run("an unknown label in a proposals file is rejected at parse", func(t *testing.T) {
		if _, err := parseProposals([]byte("abc123\tposting\tnote\n")); err == nil {
			t.Error("parseProposals accepted an unknown label")
		}
		got, err := parseProposals([]byte("# comment\n\nabc123\tdetail\ta note\ndef456\tresidue\n"))
		if err != nil {
			t.Fatalf("parseProposals: %v", err)
		}
		if len(got) != 2 || got[0].Note != "a note" || got[1].Label != bench.ExtractResidue {
			t.Errorf("parsed %+v, want two proposals", got)
		}
	})
}

// TestWorksheetWithholdsTheStructuredData is the guard on the labeling protocol:
// the labeler's view must expose the page's own words and link structure and NOT
// the structured data, the stratum derived from it, or the live extractor's
// verdict. A labeler who can see that a page publishes a lone JobPosting would agree
// with the structured data by construction -- the exact circularity ADR-0043 exists
// to end -- and one who can see the verdict would agree with the verdict, which is
// the very thing the random stratum measures the precision of (#262).
func TestWorksheetWithholdsTheStructuredData(t *testing.T) {
	row := goldRow{
		URL: "https://acme.test/careers/senior-go-engineer", Verdict: true, Stratum: stratumLonePosting,
		Label: bench.ExtractDetail,
		Content: crawler.Content{
			Title:       "Senior Go Engineer",
			MainContent: strings.Repeat("responsibilities and requirements ", 200),
			JSONLD:      []string{`{"@type":"JobPosting","title":"SECRET-STRUCTURED-TITLE"}`},
			URLs: []string{
				"https://acme.test/careers/backend-engineer",
				"https://acme.test/about",
				"https://other.test/jobs/x",
			},
		},
	}

	encoded, err := json.Marshal(worksheetFor(row))
	if err != nil {
		t.Fatalf("marshal worksheet row: %v", err)
	}
	for _, leak := range []string{"SECRET-STRUCTURED-TITLE", "JobPosting", string(stratumLonePosting), string(bench.ExtractDetail)} {
		if strings.Contains(string(encoded), leak) {
			t.Errorf("worksheet leaks %q to the labeler: %s", leak, encoded)
		}
	}
	// The verdict is withheld by ABSENCE, not by a false value, so assert on the key.
	if strings.Contains(string(encoded), `"verdict"`) {
		t.Errorf("worksheet carries the live extractor's verdict, which the labels must be independent of: %s", encoded)
	}

	ws := worksheetFor(row)
	if ws.URLsTotal != 3 || ws.URLsSameHost != 2 || ws.URLsJoblike != 1 {
		t.Errorf("link counts = total %d same-host %d job-like %d, want 3/2/1", ws.URLsTotal, ws.URLsSameHost, ws.URLsJoblike)
	}
	if len([]rune(ws.Head)) != worksheetHeadRunes {
		t.Errorf("head is %d runes, want %d", len([]rune(ws.Head)), worksheetHeadRunes)
	}
	if ws.Mid == "" {
		t.Error("mid window is empty for a long page, so a nav-only first screen cannot be told from a real body")
	}
	if ws.ID != rowID(row.URL) {
		t.Errorf("worksheet id = %q, want the row id %q", ws.ID, rowID(row.URL))
	}
}

// TestCommittedGoldSetIsWellFormed is the structural guard on the committed file:
// it is the evidence base every later extract decision is argued from, so a row
// that cannot be scored, weighted, or attributed must fail the build rather than
// quietly weaken a scorecard.
func TestCommittedGoldSetIsWellFormed(t *testing.T) {
	rows := loadCommittedGoldSet(t)

	byDrawing := map[goldDrawing][]goldRow{}
	for _, row := range rows {
		byDrawing[row.Stratum.Drawing()] = append(byDrawing[row.Stratum.Drawing()], row)
	}
	if got := len(byDrawing[drawingStructural]); got != structuralStratumRows {
		t.Errorf("the structural drawing has %d rows, want %d", got, structuralStratumRows)
	}
	if got := len(byDrawing[drawingRandom]); got != randomStratumRows {
		t.Errorf("the random drawing has %d rows, want %d", got, randomStratumRows)
	}
	if want := structuralStratumRows + randomStratumRows; len(rows) != want {
		t.Errorf("gold set has %d rows, want %d (the two drawings and nothing else)", len(rows), want)
	}
	// Per drawing AND file-wide. A weight normalizes within its drawing, so the
	// per-drawing balance is the real invariant; the file-wide one holds only because
	// each drawing's weights sum to its own row count, and asserting both catches a
	// drawing that borrowed mass from the other.
	for _, d := range []goldDrawing{drawingStructural, drawingRandom} {
		if !weightsBalanced(byDrawing[d], 1e-6) {
			t.Errorf("the %s drawing's weights sum to %.6f over %d rows, want equal", d, weightSum(byDrawing[d]), len(byDrawing[d]))
		}
	}
	if !weightsBalanced(rows, 1e-6) {
		t.Errorf("weights sum to %.6f over %d rows, want equal", weightSum(rows), len(rows))
	}

	seen := map[string]struct{}{}
	byLabel := map[bench.ExtractLabel]int{}
	byStratum := map[goldStratum]int{}
	for _, row := range rows {
		if _, dup := seen[row.URL]; dup {
			t.Errorf("duplicate url %q", row.URL)
		}
		seen[row.URL] = struct{}{}
		if _, err := crawler.NewURL(row.URL); err != nil {
			t.Errorf("url %q does not parse: %v", row.URL, err)
		}
		if !row.Label.Valid() {
			t.Errorf("%s: label %q is not one of detail / hub-index / residue", row.URL, row.Label)
		}
		if !row.Stratum.Valid() {
			t.Errorf("%s: stratum %q is unknown", row.URL, row.Stratum)
		}
		if row.Weight <= 0 {
			t.Errorf("%s: weight %g, want > 0", row.URL, row.Weight)
		}
		if row.TS == "" {
			t.Errorf("%s: no capture timestamp", row.URL)
		}
		if row.LabelProvenance.ProposedBy == "" || row.LabelProvenance.ProposedAt == "" {
			t.Errorf("%s: label has no proposer (%+v)", row.URL, row.LabelProvenance)
		}
		// A page that parsed to nothing is real evidence about the stream (the live
		// extractor still ruled on it), so it stays in the sample -- but it can
		// never be a posting: there is nothing on it to be one.
		if row.Content.Title == "" && row.Content.MainContent == "" && len(row.Content.JSONLD) == 0 && row.Label == bench.ExtractDetail {
			t.Errorf("%s: labelled detail but the captured content is entirely empty", row.URL)
		}
		// The expected extraction is #256's field-fidelity ground truth. It exists
		// on exactly the rows the Free Extraction fires on -- an expectation
		// anywhere else could never be scored -- and it carries its own provenance,
		// because it is a label like any other.
		if row.Expected != nil {
			if row.Expected.Title == "" {
				t.Errorf("%s: expected extraction has no title", row.URL)
			}
			if row.Stratum != stratumLonePosting {
				t.Errorf("%s: carries an expected extraction in stratum %q, where a Free Extraction never fires", row.URL, row.Stratum)
			}
			if row.Expected.ProposedBy == "" || row.Expected.ProposedAt == "" {
				t.Errorf("%s: expected extraction has no proposer (%+v)", row.URL, row.Expected)
			}
			if row.Expected.FreeOK && (row.Label != bench.ExtractResidue || row.Expected.FreeOKNote == "") {
				t.Errorf("%s: free_ok on a %q row with note %q; only an argued residue fire is excusable", row.URL, row.Label, row.Expected.FreeOKNote)
			}
		}
		// The stratum is a STRUCTURAL classification -- the page carries one titled
		// posting node -- while firing additionally requires the page to still be
		// offering the posting it declares (ADR-0042). The two parted company when the predicate was
		// narrowed to refuse withdrawn postings, so this asks the mechanism itself rather
		// than reading firing off the stratum.
		//
		// Scoped to the STRUCTURAL drawing. #256's ground truth was proposed over that
		// drawing alone; extending it to the random stratum would raise
		// pendingExpectedConfirmations, a ratchet that may only fall, and is #256's
		// business rather than the random draw's. score-free skips the same rows for
		// the same reason.
		if row.Stratum.Drawing() == drawingStructural {
			posting, lone := crawler.LonePosting(&row.Content)
			fires := lone && posting.Title != "" && !crawler.WithdrawalNotice(&row.Content, posting)
			if fires && row.Expected == nil {
				t.Errorf("%s: the Free Extraction fires on it but it carries no expected extraction to score against", row.URL)
			}
			if !fires && row.Expected != nil {
				t.Errorf("%s: carries an expected extraction the Free Extraction no longer fires on; re-run scripts/propose-expected.sh and goldset-apply", row.URL)
			}
		}
		byLabel[row.Label]++
		byStratum[row.Stratum]++
	}

	for _, l := range bench.AllExtractLabels {
		if byLabel[l] == 0 {
			t.Errorf("no row is labelled %q; the set must exercise all three classes", l)
		}
	}
	for _, s := range allStrata {
		if byStratum[s] == 0 {
			t.Errorf("no row is in stratum %q", s)
		}
	}
	t.Logf("labels: %v", byLabel)
	t.Logf("strata: %v", byStratum)
}

// TestCommittedGoldSetHumanConfirmation is the honest ratchet on ADR-0043's
// confirmation rule: every lone-posting row -- the stratum a later false-drop
// guard is decided on -- must carry a HUMAN confirmation. Labels land
// LLM-proposed, so this starts at the full stratum count and is driven to zero by
// the human's review commit. It also refuses a machine confirmation outright, so
// the gap can never be closed by the tooling that opened it.
func TestCommittedGoldSetHumanConfirmation(t *testing.T) {
	rows := loadCommittedGoldSet(t)

	pending := []string{}
	for _, row := range rows {
		prov := row.LabelProvenance
		if strings.HasPrefix(prov.ConfirmedBy, "llm:") {
			t.Errorf("%s: confirmed_by %q is a machine; a confirmation must come from a human", row.URL, prov.ConfirmedBy)
		}
		if prov.ConfirmedBy != "" && prov.ConfirmedAt == "" {
			t.Errorf("%s: confirmed by %q with no timestamp", row.URL, prov.ConfirmedBy)
		}
		if row.Stratum == stratumLonePosting && prov.ConfirmedBy == "" {
			pending = append(pending, row.URL)
		}
	}

	if len(pending) > pendingHumanConfirmations {
		t.Errorf("%d lone-posting rows await human confirmation, above the ratchet of %d", len(pending), pendingHumanConfirmations)
	}
	if len(pending) < pendingHumanConfirmations {
		t.Errorf("only %d lone-posting rows await confirmation but the ratchet is %d; lower pendingHumanConfirmations to %d", len(pending), pendingHumanConfirmations, len(pending))
	}
	if len(pending) > 0 {
		t.Logf("awaiting human confirmation on %d lone-posting rows (see extract-goldset/README.md)", len(pending))
	}
}

// TestCommittedRandomStratumIsWeightedToTheStream is the guard on what the #262
// drawing claims to be. Its weights must come from the capture's per-verdict quotas
// and #261's MEASURED stream accept rate -- never from the file's own accept/abstain
// mix, which the caps produced -- so weighting the sample back must reconstruct that
// measured rate. Everything else here is what makes such a weight readable: exactly
// two of them (one per verdict cell), summing to the drawing's own row count.
func TestCommittedRandomStratumIsWeightedToTheStream(t *testing.T) {
	random := []goldRow{}
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum == stratumRandom {
			random = append(random, row)
		}
	}
	if len(random) != randomStratumRows {
		t.Fatalf("the random stratum has %d rows, want %d", len(random), randomStratumRows)
	}

	weights := map[float64]int{}
	accepted, total := 0.0, 0.0
	for _, row := range random {
		weights[row.Weight]++
		total += row.Weight
		if row.Verdict {
			accepted += row.Weight
		}
		if !row.Label.Valid() {
			t.Errorf("%s: unlabelled", row.URL)
		}
		if row.LabelProvenance.ProposedBy == "" {
			t.Errorf("%s: no label proposer", row.URL)
		}
		// #256's ground truth was proposed over the structural drawing alone, and a
		// fired row with no expectation is fatal, so a random row must carry none.
		if row.Expected != nil {
			t.Errorf("%s: a random row carries an expected extraction, which #256 never proposed", row.URL)
		}
	}

	// One weight per verdict cell: the draw's only sampling cell is the verdict, so a
	// third distinct weight would mean it was stratified on something else.
	if len(weights) != 2 {
		t.Errorf("the random stratum carries %d distinct weights, want 2 (one per verdict cell): %v", len(weights), weights)
	}
	if math.Abs(total-float64(len(random))) > 1e-6 {
		t.Errorf("weights sum to %.6f over %d rows, want equal", total, len(random))
	}
	if got := accepted / total; math.Abs(got-randomStreamAcceptRate) > 5e-4 {
		t.Errorf("the weighted accept share is %.6f, want the measured stream rate %.4f; the weights are resting on the file's own mix rather than on the census", got, randomStreamAcceptRate)
	}
}

// TestCommittedGoldSetSpotCheck is the random stratum's honest confirmation record.
// ADR-0043 asks this stratum to be SPOT-CHECKED rather than fully confirmed, so
// unlike the lone-posting ratchet this one rises: it asserts in BOTH directions, so
// neither a confirmation that quietly vanished nor one that landed without the
// constant moving can pass unseen.
func TestCommittedGoldSetSpotCheck(t *testing.T) {
	checked := []string{}
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum != stratumRandom || row.LabelProvenance.ConfirmedBy == "" {
			continue
		}
		checked = append(checked, row.URL)
	}

	if len(checked) != randomSpotChecks {
		t.Errorf("%d random rows carry a human confirmation but randomSpotChecks is %d; set it to %d", len(checked), randomSpotChecks, len(checked))
	}
	for _, url := range checked {
		t.Logf("spot-checked: %s", url)
	}
	if len(checked) == 0 {
		t.Logf("no random row has been spot-checked yet (see extract-goldset/README.md for the 20 rows to read)")
	}
}

// TestCommittedGoldSetScoresThroughScoreCapture is the acceptance guard that the
// existing gate-scoring verb runs against the committed file with no network and
// no model: every row replays through pagegate.ShouldExtract and folds into a
// scorecard.
//
// It deliberately does NOT assert zero false-drops. The capture tap sits
// DOWNSTREAM of the Extract Gate, so nearly every row is a page the gate already
// admitted; the file measures the gate's leak side honestly and its drop side
// barely at all. The counts are logged rather than asserted so the numbers are
// visible under `go test -v` without pretending to be a guard they are not
// (ADR-0044's random stratum and Shadow Extraction are what close that gap).
func TestCommittedGoldSetScoresThroughScoreCapture(t *testing.T) {
	path := filepath.Join("extract-goldset", goldSetFile)
	rows, skipped, err := replayCaptured(path, crawler.DefaultLLMGateConfig())
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}
	if skipped != 0 {
		t.Errorf("skipped %d unlabeled rows, want 0 (every committed row carries a label)", skipped)
	}
	if want := len(loadCommittedGoldSet(t)); len(rows) != want {
		t.Fatalf("scored %d rows, want %d", len(rows), want)
	}

	report := bench.ScoreExtract(verdictRows(rows))
	if report.Extract.Total != len(rows) {
		t.Errorf("scorecard total = %d, want %d", report.Extract.Total, len(rows))
	}
	for _, l := range bench.AllExtractLabels {
		if _, ok := report.Extract.ByClass[l]; !ok {
			t.Errorf("scorecard has no per-class slice for %q", l)
		}
	}
	t.Logf("extract-call rate %.4f, false-drops %d, leaks %d, residue %d/%d extracted",
		report.Extract.ExtractCallRate, len(report.Extract.FalseDrops), len(report.Extract.Leaks),
		report.Extract.ResidueExtracted, report.Extract.ResidueCount)
	for _, url := range report.Extract.FalseDrops {
		t.Logf("false-drop: %s", url)
	}

	// The stream figures are logged, never asserted, for the same reason the
	// false-drops are: they estimate the population the tap SAMPLED -- pages the gate
	// already admitted -- so under today's config the call rate is near 1.0 by
	// construction. They are the honest cost numbers only when a candidate config is
	// scored against them.
	stream := bench.ScoreExtractStream(verdictRowsIn(rows, stratumRandom))
	t.Logf("stream-weighted (n=%d, effective n=%.1f): composition %v, extract-call rate %.4f, precision %.4f, recall %.4f",
		stream.Rows, stream.EffectiveN, stream.Composition, stream.ExtractCallRate, stream.Precision, stream.Recall)
}

// TestCommittedGoldSetExpectedConfirmation is the honest ratchet on the #256
// ground truth: the expected title, location and working mode -- and every accepted
// residue fire resting on them -- are proposed by a script and must be confirmed by
// a human. It refuses a machine confirmer outright, so the gap can never be closed
// by the tooling that opened it.
func TestCommittedGoldSetExpectedConfirmation(t *testing.T) {
	rows := loadCommittedGoldSet(t)

	pending := []string{}
	for _, row := range rows {
		if row.Expected == nil {
			continue
		}
		if strings.HasPrefix(row.Expected.ConfirmedBy, "llm:") || strings.HasPrefix(row.Expected.ConfirmedBy, "script:") {
			t.Errorf("%s: expected confirmed_by %q is a machine; a confirmation must come from a human", row.URL, row.Expected.ConfirmedBy)
		}
		if row.Expected.ConfirmedBy != "" && row.Expected.ConfirmedAt == "" {
			t.Errorf("%s: expected extraction confirmed by %q with no timestamp", row.URL, row.Expected.ConfirmedBy)
		}
		if row.Expected.ConfirmedBy == "" {
			pending = append(pending, row.URL)
		}
	}

	if len(pending) > pendingExpectedConfirmations {
		t.Errorf("%d expected extractions await human confirmation, above the ratchet of %d", len(pending), pendingExpectedConfirmations)
	}
	if len(pending) < pendingExpectedConfirmations {
		t.Errorf("only %d expected extractions await confirmation but the ratchet is %d; lower pendingExpectedConfirmations to %d", len(pending), pendingExpectedConfirmations, len(pending))
	}
	if len(pending) > 0 {
		t.Logf("awaiting human confirmation on %d expected extractions (see extract-goldset/README.md)", len(pending))
	}
}

// TestExpectedSheetRoundTrip proves the #256 review surface is lossless for the
// fields it owns, and that a captured page title -- arbitrary page text -- carrying
// a tab, a newline or a leading '#' still yields exactly one parseable line.
func TestExpectedSheetRoundTrip(t *testing.T) {
	rows := []goldRow{
		{
			URL: "https://b.test/jobs/2", Stratum: stratumLonePosting, Label: bench.ExtractResidue,
			Expected: &goldExpected{
				Title: "#Cook\tat\nB Corp", Location: "", WorkArrangement: "remote",
				FreeOK: true, FreeOKNote: "expired posting still serving its structured data",
				ProposedBy: "script:test", ConfirmedBy: "A Human",
			},
		},
		{
			URL: "https://a.test/jobs/1", Stratum: stratumLonePosting, Label: bench.ExtractDetail,
			Expected: &goldExpected{Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified", ProposedBy: "script:test"},
		},
		{URL: "https://c.test/about", Stratum: stratumNoPosting, Label: bench.ExtractResidue},
	}

	data := renderExpectedSheet(rows)
	if lines := strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1; lines != 3 {
		t.Fatalf("sheet has %d lines, want 1 header + the 2 rows carrying an expectation", lines)
	}
	parsed, err := parseExpectedSheet(data)
	if err != nil {
		t.Fatalf("parseExpectedSheet: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("parsed %d rows, want 2", len(parsed))
	}
	want := expectedSheetRow{
		ID: rowID("https://a.test/jobs/1"), URL: "https://a.test/jobs/1", Label: bench.ExtractDetail,
		Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified", ProposedBy: "script:test",
	}
	if parsed[0] != want {
		t.Errorf("round-tripped row = %+v, want %+v", parsed[0], want)
	}
	if parsed[1].Title != "#Cook at B Corp" || parsed[1].Location != "" || !parsed[1].FreeOK || parsed[1].ConfirmedBy != "A Human" {
		t.Errorf("round-tripped row = %+v, want the flattened title, an empty location and the acceptance", parsed[1])
	}

	t.Run("a malformed sheet is rejected", func(t *testing.T) {
		const ok = "id\turl\tdetail\tTitle\tBerlin, DE\tunspecified\tfalse\t\t\t"
		for name, bad := range map[string]string{
			"too few columns":        expectedSheetHeader + "\nid\turl\tdetail\tTitle\n",
			"unknown label":          expectedSheetHeader + "\nid\turl\tposting\tTitle\tBerlin, DE\tunspecified\tfalse\t\t\t\n",
			"bad free_ok":            expectedSheetHeader + "\nid\turl\tdetail\tTitle\tBerlin, DE\tunspecified\tmaybe\t\t\t\n",
			"unknown work mode":      expectedSheetHeader + "\nid\turl\tdetail\tTitle\tBerlin, DE\tanywhere\tfalse\t\t\t\n",
			"empty work mode":        expectedSheetHeader + "\nid\turl\tdetail\tTitle\tBerlin, DE\t\tfalse\t\t\t\n",
			"free_ok with no reason": expectedSheetHeader + "\nid\turl\tresidue\tTitle\tBerlin, DE\tunspecified\ttrue\t\t\t\n",
			"a well-formed sheet is not rejected (control)": expectedSheetHeader + "\n" + ok + "\n",
		} {
			_, err := parseExpectedSheet([]byte(bad))
			if strings.HasPrefix(name, "a well-formed") {
				if err != nil {
					t.Errorf("parseExpectedSheet rejected a well-formed sheet: %v", err)
				}
				continue
			}
			if err == nil {
				t.Errorf("parseExpectedSheet accepted a sheet with %s", name)
			}
		}
	})
}

// TestApplyMergesExpectedAndStampsProvenance covers the #256 half of the merge: the
// expected extraction lands on the row, its provenance is stamped truthfully and
// never rewritten, an acceptance is only allowed where it means something, and
// every disagreement with the substrate aborts the whole merge rather than
// half-applying it.
func TestApplyMergesExpectedAndStampsProvenance(t *testing.T) {
	const stamp = "2026-08-07T12:00:00Z"
	base := func() []goldRow {
		return []goldRow{
			{URL: "https://a.test/jobs/1", Stratum: stratumLonePosting, Label: bench.ExtractDetail},
			{URL: "https://b.test/jobs/2", Stratum: stratumLonePosting, Label: bench.ExtractResidue},
			{URL: "https://c.test/careers", Stratum: stratumNoPosting, Label: bench.ExtractHubIndex},
		}
	}
	sheetFor := func(i int, mutate func(*expectedSheetRow)) []expectedSheetRow {
		rows := base()
		row := expectedSheetRow{
			ID: rowID(rows[i].URL), URL: rows[i].URL, Label: rows[i].Label,
			Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified",
		}
		if mutate != nil {
			mutate(&row)
		}
		return []expectedSheetRow{row}
	}

	t.Run("an expectation lands and is stamped with its proposer", func(t *testing.T) {
		got, err := applyExpected(base(), sheetFor(0, nil), "script:test", "", stamp)
		if err != nil {
			t.Fatalf("applyExpected: %v", err)
		}
		if got[0].Expected == nil {
			t.Fatal("no expected block was created")
		}
		if got[0].Expected.Title != "Senior Go Engineer" || got[0].Expected.Location != "Berlin, DE" {
			t.Errorf("expected = %+v, want the sheet's values", got[0].Expected)
		}
		if got[0].Expected.ProposedBy != "script:test" || got[0].Expected.ProposedAt != stamp {
			t.Errorf("provenance = %+v, want the script stamped at %s", got[0].Expected, stamp)
		}
		if got[0].Expected.ConfirmedBy != "" {
			t.Error("a proposal must never fill confirmed_by")
		}
		if got[1].Expected != nil || got[2].Expected != nil {
			t.Error("a row with no sheet line gained an expected block")
		}
	})

	// A confirmation names specific values. If a re-proposal may change them while the
	// human's name stays on the row, the whole confirmation record is worthless: the
	// guard would read "a human accepted this" about something no human ever saw.
	t.Run("editing a confirmed value retracts the confirmation", func(t *testing.T) {
		edits := []struct {
			name   string
			mutate func(*expectedSheetRow)
		}{
			{"the title changes", func(r *expectedSheetRow) { r.Title = "Staff Go Engineer" }},
			{"the location changes", func(r *expectedSheetRow) { r.Location = "Munich, DE" }},
			{"the work arrangement changes", func(r *expectedSheetRow) { r.WorkArrangement = "remote" }},
		}
		for _, e := range edits {
			t.Run(e.name, func(t *testing.T) {
				rows := base()
				rows[0].Expected = &goldExpected{
					Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified",
					ProposedBy: "script:first", ConfirmedBy: "A Human", ConfirmedAt: "2026-01-01T00:00:00Z",
				}
				got, err := applyExpected(rows, sheetFor(0, e.mutate), "", "", stamp)
				if err != nil {
					t.Fatalf("applyExpected: %v", err)
				}
				if got[0].Expected.ConfirmedBy != "" || got[0].Expected.ConfirmedAt != "" {
					t.Errorf("confirmation survived an edit to what it confirmed: %+v", got[0].Expected)
				}
			})
		}

		t.Run("re-arming a withdrawn acceptance retracts the confirmation", func(t *testing.T) {
			rows := base()
			rows[1].Expected = &goldExpected{
				Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified",
				FreeOK: false, ProposedBy: "script:first", ConfirmedBy: "A Human", ConfirmedAt: "2026-01-01T00:00:00Z",
			}
			sheet := sheetFor(1, func(r *expectedSheetRow) {
				r.FreeOK, r.FreeOKNote = true, "re-armed by a regenerated sheet"
			})
			got, err := applyExpected(rows, sheet, "", "", stamp)
			if err != nil {
				t.Fatalf("applyExpected: %v", err)
			}
			if got[1].Expected.ConfirmedBy != "" {
				t.Errorf("a re-armed free_ok stayed stamped as human-confirmed: %+v", got[1].Expected)
			}
		})

		t.Run("an unchanged re-proposal keeps the confirmation", func(t *testing.T) {
			rows := base()
			rows[0].Expected = &goldExpected{
				Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified",
				ProposedBy: "script:first", ConfirmedBy: "A Human", ConfirmedAt: "2026-01-01T00:00:00Z",
			}
			got, err := applyExpected(rows, sheetFor(0, nil), "", "", stamp)
			if err != nil {
				t.Fatalf("applyExpected: %v", err)
			}
			if got[0].Expected.ConfirmedBy != "A Human" {
				t.Errorf("an identical re-proposal dropped the confirmation: %+v", got[0].Expected)
			}
		})
	})

	t.Run("an existing proposer is never overwritten or re-stamped", func(t *testing.T) {
		rows := base()
		rows[0].Expected = &goldExpected{Title: "old", ProposedBy: "script:first", ProposedAt: "2026-01-01T00:00:00Z"}
		got, err := applyExpected(rows, sheetFor(0, nil), "script:second", "", stamp)
		if err != nil {
			t.Fatalf("applyExpected: %v", err)
		}
		if got[0].Expected.ProposedBy != "script:first" || got[0].Expected.ProposedAt != "2026-01-01T00:00:00Z" {
			t.Errorf("provenance = %+v, want the original proposer untouched", got[0].Expected)
		}
		if got[0].Expected.Title != "Senior Go Engineer" {
			t.Errorf("title = %q, want the sheet's corrected value", got[0].Expected.Title)
		}
		if rows[0].Expected.Title != "old" {
			t.Error("the caller's row was mutated by the merge")
		}
	})

	t.Run("a human confirmation lands only where none is recorded", func(t *testing.T) {
		rows := base()
		// The stored values match the sheet, so this isolates "a confirmer is never
		// overwritten" from the separate rule that an EDIT retracts the confirmation.
		rows[0].Expected = &goldExpected{
			Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified",
			ProposedBy: "script:first", ConfirmedBy: "First Human", ConfirmedAt: "2026-01-01T00:00:00Z",
		}
		got, err := applyExpected(rows, sheetFor(0, nil), "", "Second Human", stamp)
		if err != nil {
			t.Fatalf("applyExpected: %v", err)
		}
		if got[0].Expected.ConfirmedBy != "First Human" {
			t.Errorf("confirmer = %q, want the original untouched", got[0].Expected.ConfirmedBy)
		}
	})

	t.Run("an acceptance is refused where it means nothing", func(t *testing.T) {
		accept := func(row *expectedSheetRow) { row.FreeOK, row.FreeOKNote = true, "expired posting" }
		for name, sheet := range map[string][]expectedSheetRow{
			"free_ok on a detail row":    sheetFor(0, accept),
			"free_ok on a hub-index row": sheetFor(2, accept),
		} {
			if _, err := applyExpected(base(), sheet, "", "", stamp); err == nil {
				t.Errorf("applyExpected accepted %s", name)
			}
		}
		got, err := applyExpected(base(), sheetFor(1, accept), "", "", stamp)
		if err != nil {
			t.Fatalf("applyExpected refused an acceptance on a residue row: %v", err)
		}
		if !got[1].Expected.FreeOK || got[1].Expected.FreeOKNote != "expired posting" {
			t.Errorf("expected = %+v, want the acceptance and its reason", got[1].Expected)
		}
	})

	t.Run("a disagreeing or unscorable sheet aborts the merge", func(t *testing.T) {
		staleLabel := sheetFor(0, func(row *expectedSheetRow) { row.Label = bench.ExtractResidue })
		unknown := sheetFor(0, func(row *expectedSheetRow) { row.URL, row.ID = "https://gone.test/x", rowID("https://gone.test/x") })
		duplicate := append(sheetFor(0, nil), sheetFor(0, nil)...)

		for name, sheet := range map[string][]expectedSheetRow{
			"the label disagrees":                    staleLabel,
			"the url is unknown":                     unknown,
			"the row appears twice":                  duplicate,
			"the row is not sampled as lone-posting": sheetFor(2, nil),
		} {
			before := base()
			if _, err := applyExpected(before, sheet, "script:test", "", stamp); err == nil {
				t.Errorf("applyExpected accepted a sheet where %s", name)
			}
			for _, row := range before {
				if row.Expected != nil {
					t.Errorf("%s: the caller's rows were mutated by a failed merge", name)
				}
			}
		}
	})
}

// TestCommittedExpectedSheetMatchesTheGoldSet keeps the #256 review surface from
// drifting from the substrate: expected.tsv is the 70 lines a reviewer confirms,
// so a sheet that no longer renders from goldset.jsonl would have them confirming
// values that are not the ones being scored.
func TestCommittedExpectedSheetMatchesTheGoldSet(t *testing.T) {
	rows := loadCommittedGoldSet(t)
	committed, err := os.ReadFile(filepath.Join("extract-goldset", expectedFile))
	if err != nil {
		t.Fatalf("read committed expected sheet: %v", err)
	}
	if got := renderExpectedSheet(rows); string(got) != string(committed) {
		t.Error("expected.tsv is not the sheet rendered from goldset.jsonl; re-run `llmbench goldset-apply`")
	}
}

// TestCommittedLabelsSheetMatchesTheGoldSet keeps the human review surface from
// drifting from the substrate: labels.tsv is what a reviewer reads in a diff and
// edits to correct a label, so a sheet that no longer renders from goldset.jsonl
// would have a reviewer confirming rows that are not the ones being scored.
func TestCommittedLabelsSheetMatchesTheGoldSet(t *testing.T) {
	rows := loadCommittedGoldSet(t)
	committed, err := os.ReadFile(filepath.Join("extract-goldset", labelsFile))
	if err != nil {
		t.Fatalf("read committed sheet: %v", err)
	}
	if got := renderSheet(rows); string(got) != string(committed) {
		t.Error("labels.tsv is not the sheet rendered from goldset.jsonl; re-run `llmbench goldset-apply`")
	}
}
