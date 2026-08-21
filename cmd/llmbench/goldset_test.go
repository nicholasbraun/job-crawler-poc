package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/extractcapture"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// The Extract Gold Set (cmd/llmbench/extract-goldset/goldset.jsonl) is a
// committed sample of pages the LIVE extract stage actually decided on, stored as
// the parsed Content the pipeline itself produced (ADR-0043) -- never re-fetched
// HTML, because a page re-fetched months later is a different page and the drift
// corrupts the label.
//
// It holds THREE DRAWINGS. They share a row format and a file; they share nothing
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
// Drawing 3 -- BOUNDARY (#263), 188 rows. From the SAME capture and the SAME
// faithful frame as drawing 2, minus the 162 URLs the first two drawings had
// committed: 5042 candidates. Both gate configs -- today's blanket accept
// (boundaryBaselineConfig) and the tiered Positive Evidence rule
// (boundaryCandidateConfig) -- were replayed over every candidate's captured
// Content, and the 3059 pages they DISAGREE on were partitioned by the live
// extractor's verdict: 188 accept, 2871 abstain, 0 reversed. The accept half is
// taken WHOLE, so this drawing is a census: inclusion probability 1 and every weight
// exactly 1. The abstain half is recorded and not drawn (extract-goldset/README.md
// argues why). Drawn by `llmbench goldset-sample-boundary -since
// 2026-08-07T21:13:00Z`, which takes no seed and no gate-config precisely so the
// boundary cannot be silently redefined by a flag.
//
// ESTIMAND. None. A census of a disagreement set describes no population, which is
// why its rows are excluded from every weighted stream number and why the boundary
// scorecard is unweighted.
//
// Each row records its labelling in label_provenance. The structural drawing's
// lone-posting stratum is fully human-confirmed (pendingHumanConfirmations); the
// random stratum is SPOT-CHECKED instead (randomSpotChecks), which is what ADR-0043
// asks of it; the boundary stratum must be FULLY confirmed
// (pendingBoundaryConfirmations), because it is the stratum a hard-zero false-drop
// guard is decided on. Every one of those counts is a one-directional ratchet rather
// than a pin (ADR-0048); which rows carry a confirmer is pinned instead, across both
// files, by TestCommittedRecordAgreesOnWhoConfirmedWhat.

// pendingHumanConfirmations is how many lone-posting rows still carry an
// LLM-proposed label no human has confirmed. #254 requires 0.
//
// It is 1. The confirmation pass found three pages carrying a closure banner above an
// otherwise complete posting; they were relabelled residue and the mechanism now
// refuses them by that banner, so their labels are confirmed and they no longer fire.
// What remains is one open-application page labelled detail where the same shape is
// labelled residue elsewhere in the set -- a labelling inconsistency the set should
// answer the same way twice, written up in the Extract Gold Set README.
//
// RATCHET, asserted in ONE direction only (confirmationFloor, ADR-0048): a pending
// count that RISES fails the build, because that means a signature vanished; one that
// falls is logged with the number to set this to, never fatal. Lower it in the same
// commit as the confirmations -- `goldset-apply` prints the new figure.
const pendingHumanConfirmations = 1

// pendingExpectedConfirmations is how many rows carry an agent-proposed expected
// extraction that no human has confirmed. #256 requires 0: the values were read off
// each page's own JSON-LD by scripts/propose-expected.sh and a human confirms them
// at the review gate. It is 1, the same row pendingHumanConfirmations holds back: an
// expected extraction cannot be confirmed while the label it is scored under is in
// question. Like pendingHumanConfirmations it is a one-directional RATCHET
// (confirmationFloor, ADR-0048): a rise fails the build, a fall is logged with the
// number to lower it to.
const pendingExpectedConfirmations = 1

const (
	// structuralStratumRows, randomStratumRows and boundaryStratumRows are the three
	// drawings' exact row counts. They are pinned rather than bounded because a
	// drawing is a fixed act of sampling: a row appearing or vanishing changes what
	// every weighted number estimates, and must be seen in a diff. The boundary count
	// is not even a sample size -- it is the size of the disagreement itself.
	//
	// Unlike the confirmation counts below they are NOT floors: ADR-0048 relaxed those
	// because confirmations landing is progress, and a row appearing or vanishing never
	// is.
	structuralStratumRows = 149
	randomStratumRows     = 120
	boundaryStratumRows   = 188
	// randomStreamAcceptRate is the accept share the random drawing's weights were
	// built on -- #261's census measurement of the live extract stream, not the
	// capture file's own mix. TestCommittedRandomStratumIsWeightedToTheStream
	// reconstructs it from the committed weights, which is what proves they map the
	// sample back to the recorded stream rather than to the file.
	randomStreamAcceptRate = 0.0753
)

// randomSpotChecks is how many random-stratum rows a human has actually read and
// confirmed. ADR-0043 asks this stratum to be SPOT-CHECKED rather than fully
// confirmed, so unlike the pending counts this figure RISES as work lands: it is the
// machine-visible record of what has been checked, and what is therefore still owed.
//
// It is a FLOOR (confirmationFloor, ADR-0048), not a pin: falling below it fails the
// build, because a spot-check that vanished means a signature was lost; rising above
// it only logs the figure to raise it to. It was asserted in both directions until
// nine confirmations that LANDED (52a8c15) turned `main` red, and it stayed red until
// the constant was hand-edited (ead832e).
//
// It is 4 of 120 -- the rest ship LLM-proposed, and extract-goldset/README.md tells
// the reviewer exactly which rows to read and how to raise it.
const randomSpotChecks = 4

const (
	// pendingBoundaryConfirmations is how many Boundary Stratum rows still carry an
	// LLM-proposed label no human has confirmed. ADR-0043 requires 0 here -- these are
	// the rows a hard-zero false-drop guard is decided on, and they are exactly where
	// an LLM labeller is least reliable, which is what "boundary" means.
	//
	// It ships at the full stratum count: the labels were proposed in twelve 16-row
	// batches and nobody has read them yet. Until it reaches 0, the false-drop count
	// this stratum produces is one model's opinion of another model's opinion and no
	// guard may act on it. RATCHET in ONE direction (confirmationFloor, ADR-0048): a
	// rise fails the build, a fall is logged with the number to lower it to.
	// extract-goldset/README.md says exactly how.
	pendingBoundaryConfirmations = 188
	// ambiguousRows is how many rows carry the ambiguous label -- pages a reading
	// could not settle, recorded rather than forced into a class. Pinned in BOTH
	// directions: an ambiguity that appears, or one that quietly resolves, changes
	// what the confusion counts are computed over and must be seen in a diff. It is
	// deliberately NOT one of ADR-0048's floors: marking a page unresolvable is a rare,
	// deliberate keystroke, and it should stop the build until it is acknowledged.
	ambiguousRows = 10
	// boundaryDetailRows is how many Boundary Stratum rows are labelled detail. Until
	// #257 that was also how many Job Listings the Positive Evidence rule dropped
	// here, because the stratum was DRAWN as the pages that rule skipped; #257 widened
	// the rule, so the two numbers have parted company and the drop count is
	// boundaryFalseDropsRemaining. This constant is now purely a census of the labels,
	// and it is a TRIPWIRE on drift, not the guard -- these labels are still
	// LLM-proposed. The hard zero is #264's, and it may only be argued once
	// pendingBoundaryConfirmations is 0.
	//
	// It fell from 47 to 37 when the 57 consequential rows (every detail and every
	// ambiguous row) were re-read blind, with the first labeller's answer stripped, and
	// the 11 disagreements were settled by a third blind read from a different model.
	// Ten rows lost `detail`: five are a posting header followed by a rail of unrelated
	// postings or a title plus nothing but a PDF link -- real postings in the world
	// whose parsed body holds no role -- and five are standing calls (a "fortlaufend"
	// student-assistant page, an offer of a doctoral project OR a master's thesis,
	// apprenticeships across two professions). Neither shape is one posting, so
	// charging the rule with dropping them overstated the guard's target.
	boundaryDetailRows = 37
)

const (
	// boundaryRowsRecovered is how many Boundary Stratum rows the Extract Gate's
	// CURRENT Positive Evidence rung extracts. It was 0 at #263: the stratum was drawn
	// as exactly the pages the #258 rule skipped. #257 widened that rule, so the
	// stratum is now a HISTORICAL disagreement set, and these constants are the ledger
	// of what the widening recovered from it.
	//
	// The SPLIT matters more than the total. A rule that recovers non-postings faster
	// than postings is a regression however good the total looks, and pinning the four
	// numbers separately is what makes that visible in a diff.
	// It fell 29 -> 28 when "your application" left the vocabulary group it shared, by
	// substring, with applyPhrases' "submit/start/send us your application": the
	// overlap let one apply button score a section as well, so a page could reach the
	// strong four-group threshold a section early. Exactly one row lost by that,
	// vostel.de's volunteering project page, and it is AMBIGUOUS -- the detail and
	// non-posting halves below did not move at all.
	boundaryRowsRecovered = 28
	// boundaryDetailRecovered is the Job Listings the widening got back: the whole
	// point of the slice.
	boundaryDetailRecovered = 26
	// boundaryNonPostingsRecovered is the cost side of the same ledger -- hub-index
	// and residue rows the widening now extracts (2 hub-index, 0 residue).
	boundaryNonPostingsRecovered = 2
	// boundaryAmbiguousRecovered is the undecidable rows it extracts. They are neither
	// a win nor a loss, and they are counted apart so they can never be quietly
	// credited to either side. Zero since the applyPhrases overlap was removed (see
	// boundaryRowsRecovered); the one row it held is now counted in
	// boundaryAmbiguousSkipped.
	boundaryAmbiguousRecovered = 0
	// boundaryFalseDropsRemaining is what the current rung still drops here: the
	// number this stratum exists to produce, and the guard #264 must argue with. It is
	// NOT zero, and the shapes behind it are written up in ADR-0044 rather than closed
	// by a threshold nothing measured.
	boundaryFalseDropsRemaining = 11
	// boundaryAmbiguousSkipped is how many ambiguous rows are DROPPED here. It is
	// ambiguousRows minus boundaryAmbiguousRecovered: the two numbers coincided while
	// the rule skipped every boundary row, #257 parted them, and removing the
	// applyPhrases overlap put them back together.
	boundaryAmbiguousSkipped = 10
)

// confirmationFloor is the shape every confirmation guard takes since ADR-0048: a
// recorded standard that fails the build only in the direction that means a human
// signature VANISHED, and logs the current figure in the direction that means work
// landed.
//
// THE SECOND DIRECTION WAS DROPPED DELIBERATELY. Do not restore it as a fix.
// Asserted as an equality, a confirmation that LANDED failed the build until somebody
// hand-edited a constant -- nine of them did exactly that (52a8c15, green again only
// in ead832e) -- and a tool that makes confirmation cheap makes that worse in
// proportion to how well it works. It was never the safety property it looked like: a
// confirmation cannot land unseen, because it arrives inside a commit that diffs both
// goldset.jsonl and labels.tsv, and `goldset-apply` prints the new counts at the end
// of every run. What the equality was standing in for is asserted directly and more
// sharply by TestCommittedRecordAgreesOnWhoConfirmedWhat, which names the rows the two
// files disagree on instead of reporting that a total moved.
//
// Row counts and ambiguousRows are NOT floors and stay pinned in both directions
// (TestCommittedGoldSetIsWellFormed, TestCommittedGoldSetAmbiguityIsRecorded): a
// drawing is a fixed act of sampling, and an ambiguity that appears or resolves
// changes what every confusion number is computed over.
type confirmationFloor struct {
	// constant names the identifier to edit, so a log line says what to change rather
	// than merely that something changed.
	constant string
	// counts describes what the figure is, in the words the failure should read in.
	counts string
	// current is the figure read off the committed record; recorded is the constant.
	current, recorded int
	// rises reports which direction confirmations move the figure in: true for a
	// CONFIRMED count, false for a PENDING count. A pending count is the complement of
	// a confirmation count over a row count pinned in both directions, so a ceiling on
	// pending is exactly a floor on confirmations -- which is why both live here.
	rises bool
}

// assert fails when the figure moved in the direction that means a signature was lost,
// and ALWAYS logs the figure: the number a ratchet is set to must never have to be
// computed by hand (ADR-0048).
func (f confirmationFloor) assert(t *testing.T) {
	t.Helper()
	switch {
	case f.rises && f.current < f.recorded:
		t.Errorf("%d %s, BELOW the recorded floor of %d (%s): a human confirmation has vanished from the record. "+
			"Find it in the diff -- a confirmation is a signature, and the floor may only be lowered by the person who withdraws one.",
			f.current, f.counts, f.recorded, f.constant)
	case !f.rises && f.current > f.recorded:
		t.Errorf("%d %s, ABOVE the recorded ratchet of %d (%s): a human confirmation has vanished from the record. "+
			"Find it in the diff -- a confirmation is a signature, and the ratchet may only be raised by the person who withdraws one.",
			f.current, f.counts, f.recorded, f.constant)
	case f.current != f.recorded:
		t.Logf("%d %s; set %s to %d in the same commit as the confirmations (it is %d)",
			f.current, f.counts, f.constant, f.current, f.recorded)
	default:
		t.Logf("%d %s (%s)", f.current, f.counts, f.constant)
	}
}

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
		got, err := applyLabels(rows, sheetFor(rows), proposals, "llm:test-agent", "", "", nil, stamp)
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
		got, err := applyLabels(rows, sheetFor(rows), nil, "llm:second", "", "", nil, stamp)
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
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "A Human", stratumLonePosting, nil, stamp)
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
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "A Human", "", nil, stamp)
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
			if _, err := applyLabels(before, sheet, nil, "llm:test", "", "", nil, stamp); err == nil {
				t.Errorf("applyLabels accepted a sheet where %s", name)
			}
			if before[0].Label != "" || before[0].LabelProvenance.ProposedBy != "" {
				t.Errorf("%s: the caller's rows were mutated by a failed merge", name)
			}
		}

		if _, err := applyLabels(rows, good, []sheetRow{{ID: "deadbeefdead", Label: bench.ExtractDetail}}, "", "", "", nil, stamp); err == nil {
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

	// The Proposed Label (ADR-0048): what the proposer actually proposed survives a
	// human overriding it, so the record says what happened and a confirmation pass
	// produces a measurement rather than an echo.
	t.Run("an override preserves the label the proposer proposed", func(t *testing.T) {
		rows := base()
		rows[0].Label = bench.ExtractDetail
		rows[0].LabelProvenance = goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-01-01T00:00:00Z", Note: "one role, apply button"}
		id := rowID("https://a.test/jobs/1")
		proposals := []sheetRow{{ID: id, Label: bench.ExtractResidue, Note: "an index of roles"}}
		got, err := applyLabels(rows, sheetFor(rows), proposals, "", "A Human", "", map[string]struct{}{id: {}}, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		prov := got[0].LabelProvenance
		if got[0].Label != bench.ExtractResidue {
			t.Errorf("label = %q, want residue", got[0].Label)
		}
		if prov.ProposedLabel != bench.ExtractDetail {
			t.Errorf("proposed_label = %q, want the label the proposer had put on the row", prov.ProposedLabel)
		}
		if prov.ProposedBy != "llm:test" {
			t.Errorf("proposer = %q, want the original untouched", prov.ProposedBy)
		}
		if prov.ConfirmedBy != "A Human" || prov.ConfirmedAt != stamp {
			t.Errorf("provenance = %+v, want the human's signature on the label they gave", prov)
		}
		if prov.Note != "an index of roles" {
			t.Errorf("note = %q, want the overriding note", prov.Note)
		}
	})

	t.Run("agreement records the same Proposed Label and keeps the proposer's note", func(t *testing.T) {
		rows := base()
		rows[0].Label = bench.ExtractDetail
		rows[0].LabelProvenance = goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-01-01T00:00:00Z", Note: "one role, apply button"}
		id := rowID("https://a.test/jobs/1")
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "A Human", "", map[string]struct{}{id: {}}, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		prov := got[0].LabelProvenance
		if got[0].Label != bench.ExtractDetail || prov.ProposedLabel != bench.ExtractDetail {
			t.Errorf("label %q / proposed_label %q, want both detail", got[0].Label, prov.ProposedLabel)
		}
		if prov.Note != "one role, apply button" {
			t.Errorf("note = %q, want the proposer's own untouched", prov.Note)
		}
		if prov.ConfirmedBy != "A Human" {
			t.Errorf("confirmer = %q, want the human", prov.ConfirmedBy)
		}
	})

	t.Run("backfill only touches rows with no confirmer", func(t *testing.T) {
		rows := base()
		rows[0].Label = bench.ExtractDetail
		rows[0].LabelProvenance = goldProvenance{
			ProposedBy: "llm:test", ProposedAt: "2026-01-01T00:00:00Z",
			ConfirmedBy: "A Human", ConfirmedAt: "2026-01-02T00:00:00Z",
		}
		rows[1].Label = bench.ExtractResidue
		rows[1].LabelProvenance = goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-01-01T00:00:00Z"}
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "", "", nil, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		if got[0].LabelProvenance.ProposedLabel != "" {
			t.Errorf("proposed_label = %q on an already-confirmed row; the set does not record what was proposed there", got[0].LabelProvenance.ProposedLabel)
		}
		if got[1].LabelProvenance.ProposedLabel != bench.ExtractResidue {
			t.Errorf("proposed_label = %q, want residue on the unconfirmed row", got[1].LabelProvenance.ProposedLabel)
		}
		if got[0].Label != bench.ExtractDetail || got[1].Label != bench.ExtractResidue {
			t.Errorf("labels = %q / %q, want them untouched by a backfill", got[0].Label, got[1].Label)
		}
		if got[0].LabelProvenance.ConfirmedBy != "A Human" || got[1].LabelProvenance.ConfirmedBy != "" {
			t.Errorf("confirmers = %q / %q, want them untouched by a backfill", got[0].LabelProvenance.ConfirmedBy, got[1].LabelProvenance.ConfirmedBy)
		}
	})

	t.Run("an unlabelled row gains no Proposed Label", func(t *testing.T) {
		rows := base()
		got, err := applyLabels(rows, sheetFor(rows), nil, "", "", "", nil, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		for _, row := range got {
			if row.LabelProvenance.ProposedLabel != "" || row.LabelProvenance.ProposedBy != "" {
				t.Errorf("%s: unlabelled row carries provenance %+v", row.URL, row.LabelProvenance)
			}
		}
	})

	t.Run("a label that first lands in this run is its own Proposed Label", func(t *testing.T) {
		rows := base()
		proposals := []sheetRow{{ID: rowID("https://a.test/jobs/1"), Label: bench.ExtractDetail}}
		got, err := applyLabels(rows, sheetFor(rows), proposals, "llm:test-agent", "", "", nil, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		prov := got[0].LabelProvenance
		if prov.ProposedLabel != bench.ExtractDetail || prov.ProposedBy != "llm:test-agent" || prov.ProposedAt != stamp {
			t.Errorf("provenance = %+v, want the agent's own label stamped at %s", prov, stamp)
		}
	})

	t.Run("a first label with no proposer carries no Proposed Label", func(t *testing.T) {
		rows := base()
		proposals := []sheetRow{{ID: rowID("https://a.test/jobs/1"), Label: bench.ExtractDetail}}
		got, err := applyLabels(rows, sheetFor(rows), proposals, "", "", "", nil, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		prov := got[0].LabelProvenance
		if got[0].Label != bench.ExtractDetail {
			t.Errorf("label = %q, want detail", got[0].Label)
		}
		// The well-formedness guard rejects a Proposed Label with no proposer, so the
		// merge must never be able to produce one.
		if prov.ProposedBy != "" || prov.ProposedLabel != "" {
			t.Errorf("provenance = %+v, want no proposer and no proposed label", prov)
		}
	})

	t.Run("an existing Proposed Label is never rewritten", func(t *testing.T) {
		rows := base()
		rows[0].Label = bench.ExtractHubIndex
		rows[0].LabelProvenance = goldProvenance{
			ProposedBy: "llm:first", ProposedAt: "2026-01-01T00:00:00Z", ProposedLabel: bench.ExtractDetail,
		}
		proposals := []sheetRow{{ID: rowID("https://a.test/jobs/1"), Label: bench.ExtractResidue}}
		got, err := applyLabels(rows, sheetFor(rows), proposals, "", "", "", nil, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		if got[0].Label != bench.ExtractResidue {
			t.Errorf("label = %q, want residue", got[0].Label)
		}
		if got[0].LabelProvenance.ProposedLabel != bench.ExtractDetail {
			t.Errorf("proposed_label = %q, want the first proposal kept (first-writer-wins, like proposed_by)", got[0].LabelProvenance.ProposedLabel)
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
	if got := len(byDrawing[drawingBoundary]); got != boundaryStratumRows {
		t.Errorf("the boundary drawing has %d rows, want %d", got, boundaryStratumRows)
	}
	if want := structuralStratumRows + randomStratumRows + boundaryStratumRows; len(rows) != want {
		t.Errorf("gold set has %d rows, want %d (the three drawings and nothing else)", len(rows), want)
	}
	// Per drawing AND file-wide. A weight normalizes within its drawing, so the
	// per-drawing balance is the real invariant; the file-wide one holds only because
	// each drawing's weights sum to its own row count, and asserting both catches a
	// drawing that borrowed mass from the other.
	for _, d := range []goldDrawing{drawingStructural, drawingRandom, drawingBoundary} {
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
			t.Errorf("%s: label %q is not one of detail / hub-index / residue / ambiguous", row.URL, row.Label)
		}
		// The Boundary Stratum is a census, so its weight is not a sampling artifact
		// to be recomputed: it is 1 by definition, and any other value means the row
		// arrived from a drawing that thought it was sampling.
		if row.Stratum == stratumBoundary && row.Weight != boundaryCensusWeight {
			t.Errorf("%s: boundary row carries weight %g, want the census weight %g", row.URL, row.Weight, boundaryCensusWeight)
		}
		// This drawing takes the accept half of the disagreement only.
		if row.Stratum == stratumBoundary && !row.Verdict {
			t.Errorf("%s: boundary row whose live verdict was abstain", row.URL)
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
		// The Proposed Label is a claim about what a NAMED proposer said (ADR-0048), so
		// it cannot stand on a row that names none, and it must be a label the set
		// actually scores. A row confirmed before the field existed carries none at all --
		// that absence is honest, and is not what this rejects.
		if prov := row.LabelProvenance; prov.ProposedLabel != "" {
			if prov.ProposedBy == "" {
				t.Errorf("%s: proposed_label %q with no proposer (%+v)", row.URL, prov.ProposedLabel, prov)
			}
			if !prov.ProposedLabel.Valid() {
				t.Errorf("%s: proposed_label %q is not one of detail / hub-index / residue / ambiguous", row.URL, prov.ProposedLabel)
			}
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
// the human's review commit; it is asserted as a one-directional ratchet
// (confirmationFloor, ADR-0048) so that driving it down never turns the build red. It
// also refuses a machine confirmation outright, so the gap can never be closed by the
// tooling that opened it.
func TestCommittedGoldSetHumanConfirmation(t *testing.T) {
	rows := loadCommittedGoldSet(t)

	pending := []string{}
	for _, row := range rows {
		prov := row.LabelProvenance
		if machineName(prov.ConfirmedBy) {
			t.Errorf("%s: confirmed_by %q is a machine; a confirmation must come from a human", row.URL, prov.ConfirmedBy)
		}
		if prov.ConfirmedBy != "" && prov.ConfirmedAt == "" {
			t.Errorf("%s: confirmed by %q with no timestamp", row.URL, prov.ConfirmedBy)
		}
		if row.Stratum == stratumLonePosting && prov.ConfirmedBy == "" {
			pending = append(pending, row.URL)
		}
	}

	confirmationFloor{
		constant: "pendingHumanConfirmations",
		counts:   "lone-posting rows await human confirmation (see extract-goldset/README.md)",
		current:  len(pending), recorded: pendingHumanConfirmations,
	}.assert(t)
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
// unlike the lone-posting ratchet this one rises as work lands. It is a FLOOR
// (ADR-0048): a spot-check that vanished fails the build, one that landed logs the
// number to raise the constant to. The direction it lost is asserted more sharply by
// TestCommittedRecordAgreesOnWhoConfirmedWhat.
func TestCommittedGoldSetSpotCheck(t *testing.T) {
	checked := []string{}
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum != stratumRandom || row.LabelProvenance.ConfirmedBy == "" {
			continue
		}
		checked = append(checked, row.URL)
	}

	confirmationFloor{
		constant: "randomSpotChecks",
		counts:   "random rows carry a human confirmation",
		current:  len(checked), recorded: randomSpotChecks, rises: true,
	}.assert(t)
	for _, url := range checked {
		t.Logf("spot-checked: %s", url)
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
//
// The Boundary Stratum contributes nothing to the drop side HERE, by construction:
// every one of its rows is a page today's blanket accept extracts, so under
// DefaultLLMGateConfig they all extract and none can be a false-drop. Its
// information appears only when the candidate config is scored --
// TestCommittedBoundaryFalseDropsUnderTheCandidateRule is where that number lives.
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

	confirmationFloor{
		constant: "pendingExpectedConfirmations",
		counts:   "expected extractions await human confirmation (see extract-goldset/README.md)",
		current:  len(pending), recorded: pendingExpectedConfirmations,
	}.assert(t)
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

// confirmerLine is one row reduced to what the cross-file confirmation guard
// compares: the id that keys it in both files, the url that names it in a failure,
// and the confirmer that file claims. Both review sheets render one, so the guard is
// written once rather than twice over two column layouts.
type confirmerLine struct{ ID, URL, ConfirmedBy string }

// confirmerMismatch is one row the substrate and a review sheet do not agree a
// confirmer on. Both sides are carried verbatim: "the two files differ" is a finding
// nobody can act on, and which file claims what is the whole diagnosis.
type confirmerMismatch struct {
	ID  string
	URL string
	// InSubstrate and InSheet report whether the row exists in that file at all. A row
	// one file carries and the other does not is a disagreement in its own right: a
	// confirmation that vanished with its line is exactly the fault this guard is for.
	InSubstrate, InSheet bool
	SubstrateConfirmer   string
	SheetConfirmer       string
}

func (m confirmerMismatch) String() string {
	switch {
	case !m.InSheet:
		return fmt.Sprintf("%s %s: the substrate carries it (confirmer %q), the sheet has no such row", m.ID, m.URL, m.SubstrateConfirmer)
	case !m.InSubstrate:
		return fmt.Sprintf("%s %s: the sheet carries it (confirmer %q), the substrate has no such row", m.ID, m.URL, m.SheetConfirmer)
	default:
		return fmt.Sprintf("%s %s: the substrate says confirmer %q, the sheet says %q", m.ID, m.URL, m.SubstrateConfirmer, m.SheetConfirmer)
	}
}

// confirmerDisagreements pairs a substrate against a rendered review sheet by row id
// and returns every row the two do not agree a confirmer on -- a name in one file and
// not the other, two different names, or a row only one of them carries -- in id
// order, so a failure reads the same way twice.
//
// Both sides are compared through flattenField because renderSheet collapses tabs and
// newlines on write: a confirmer the sheet can only hold flattened is a faithful
// render, not a disagreement. The raw values are what the mismatch reports.
func confirmerDisagreements(substrate, sheet []confirmerLine) []confirmerMismatch {
	bySubstrate := map[string]confirmerLine{}
	for _, l := range substrate {
		bySubstrate[l.ID] = l
	}
	bySheet := map[string]confirmerLine{}
	for _, l := range sheet {
		bySheet[l.ID] = l
	}

	ids := make([]string, 0, len(bySubstrate)+len(bySheet))
	for id := range bySubstrate {
		ids = append(ids, id)
	}
	for id := range bySheet {
		if _, dup := bySubstrate[id]; !dup {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	out := []confirmerMismatch{}
	for _, id := range ids {
		sub, inSubstrate := bySubstrate[id]
		sh, inSheet := bySheet[id]
		if inSubstrate && inSheet && flattenField(sub.ConfirmedBy) == flattenField(sh.ConfirmedBy) {
			continue
		}
		url := sub.URL
		if !inSubstrate {
			url = sh.URL
		}
		out = append(out, confirmerMismatch{
			ID: id, URL: url,
			InSubstrate: inSubstrate, InSheet: inSheet,
			SubstrateConfirmer: sub.ConfirmedBy, SheetConfirmer: sh.ConfirmedBy,
		})
	}
	return out
}

// TestCommittedRecordAgreesOnWhoConfirmedWhat is the tripwire that replaces the
// direction ADR-0048 took off the confirmation ratchets. A total that moved was only
// ever a proxy for the fault that matters: a signature present in one file and absent
// from the other. This asserts that directly, row by row, and NAMES the rows.
//
// `goldset-apply` writes the substrate and both sheets from one merged slice, so they
// can only disagree if something outside it touched one of them: a half-applied write,
// a bad rebase, or a hand edit. TestCommittedLabelsSheetMatchesTheGoldSet already
// catches that as a byte difference; this says WHICH rows and WHICH confirmers, which
// is what a person needs in order to fix it.
func TestCommittedRecordAgreesOnWhoConfirmedWhat(t *testing.T) {
	rows := loadCommittedGoldSet(t)

	t.Run("labels.tsv", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("extract-goldset", labelsFile))
		if err != nil {
			t.Fatalf("read committed sheet: %v", err)
		}
		parsed, err := parseSheet(data)
		if err != nil {
			t.Fatalf("parse committed sheet: %v", err)
		}

		substrate := make([]confirmerLine, 0, len(rows))
		confirmed := 0
		for _, row := range rows {
			substrate = append(substrate, confirmerLine{ID: rowID(row.URL), URL: row.URL, ConfirmedBy: row.LabelProvenance.ConfirmedBy})
			if row.LabelProvenance.ConfirmedBy != "" {
				confirmed++
			}
		}
		sheet := make([]confirmerLine, 0, len(parsed))
		for _, line := range parsed {
			sheet = append(sheet, confirmerLine{ID: line.ID, URL: line.URL, ConfirmedBy: line.ConfirmedBy})
		}

		for _, m := range confirmerDisagreements(substrate, sheet) {
			t.Errorf("CONFIRMER DISAGREEMENT %s -- goldset.jsonl and labels.tsv are written from one merged slice, "+
				"so a difference means something outside `goldset-apply` touched one of them. Re-run the verb and read the diff.", m)
		}
		t.Logf("%d of %d rows carry a label confirmer, agreed across both files", confirmed, len(substrate))
	})

	t.Run("expected.tsv", func(t *testing.T) {
		data, err := os.ReadFile(filepath.Join("extract-goldset", expectedFile))
		if err != nil {
			t.Fatalf("read committed expected sheet: %v", err)
		}
		parsed, err := parseExpectedSheet(data)
		if err != nil {
			t.Fatalf("parse committed expected sheet: %v", err)
		}

		substrate := []confirmerLine{}
		confirmed := 0
		for _, row := range rows {
			if row.Expected == nil {
				continue
			}
			substrate = append(substrate, confirmerLine{ID: rowID(row.URL), URL: row.URL, ConfirmedBy: row.Expected.ConfirmedBy})
			if row.Expected.ConfirmedBy != "" {
				confirmed++
			}
		}
		sheet := make([]confirmerLine, 0, len(parsed))
		for _, line := range parsed {
			sheet = append(sheet, confirmerLine{ID: line.ID, URL: line.URL, ConfirmedBy: line.ConfirmedBy})
		}

		for _, m := range confirmerDisagreements(substrate, sheet) {
			t.Errorf("CONFIRMER DISAGREEMENT %s -- goldset.jsonl and expected.tsv are written from one merged slice, "+
				"so a difference means something outside `goldset-apply` touched one of them. Re-run the verb and read the diff.", m)
		}
		t.Logf("%d of %d rows carrying an expected extraction carry a confirmer, agreed across both files", confirmed, len(substrate))
	})
}

// TestConfirmerDisagreementsNamesEveryDivergence is the demonstration that the
// committed guard above actually fires: a confirmation removed from EITHER file alone
// is a named disagreement, and so is a renamed confirmer or a row only one file
// carries. It also pins the one shape that is NOT a disagreement -- a confirmer the
// sheet can only render flattened.
func TestConfirmerDisagreementsNamesEveryDivergence(t *testing.T) {
	line := func(id, confirmer string) confirmerLine {
		return confirmerLine{ID: id, URL: "https://" + id + ".test/row", ConfirmedBy: confirmer}
	}

	for _, tc := range []struct {
		name              string
		substrate, sheet  []confirmerLine
		want              []string
		wantInSubstrate   bool
		wantInSheet       bool
		assertMembership  bool
		wantSubstrateName string
		wantSheetName     string
	}{
		{
			name:      "both files agree on a confirmer",
			substrate: []confirmerLine{line("aaa", "Nicholas Braun")},
			sheet:     []confirmerLine{line("aaa", "Nicholas Braun")},
			want:      []string{},
		},
		{
			name:      "both files agree the row is unconfirmed",
			substrate: []confirmerLine{line("aaa", "")},
			sheet:     []confirmerLine{line("aaa", "")},
			want:      []string{},
		},
		{
			name:              "a confirmation removed from the sheet alone",
			substrate:         []confirmerLine{line("aaa", "Nicholas Braun")},
			sheet:             []confirmerLine{line("aaa", "")},
			want:              []string{"aaa"},
			assertMembership:  true,
			wantInSubstrate:   true,
			wantInSheet:       true,
			wantSubstrateName: "Nicholas Braun",
			wantSheetName:     "",
		},
		{
			name:              "a confirmation removed from the substrate alone",
			substrate:         []confirmerLine{line("aaa", "")},
			sheet:             []confirmerLine{line("aaa", "Nicholas Braun")},
			want:              []string{"aaa"},
			assertMembership:  true,
			wantInSubstrate:   true,
			wantInSheet:       true,
			wantSubstrateName: "",
			wantSheetName:     "Nicholas Braun",
		},
		{
			name:              "the two files name different confirmers",
			substrate:         []confirmerLine{line("aaa", "Nicholas Braun")},
			sheet:             []confirmerLine{line("aaa", "Someone Else")},
			want:              []string{"aaa"},
			assertMembership:  true,
			wantInSubstrate:   true,
			wantInSheet:       true,
			wantSubstrateName: "Nicholas Braun",
			wantSheetName:     "Someone Else",
		},
		{
			name:              "a row the sheet does not carry",
			substrate:         []confirmerLine{line("aaa", "Nicholas Braun")},
			sheet:             []confirmerLine{},
			want:              []string{"aaa"},
			assertMembership:  true,
			wantInSubstrate:   true,
			wantInSheet:       false,
			wantSubstrateName: "Nicholas Braun",
		},
		{
			name:             "a row the substrate does not carry",
			substrate:        []confirmerLine{},
			sheet:            []confirmerLine{line("aaa", "Nicholas Braun")},
			want:             []string{"aaa"},
			assertMembership: true,
			wantInSubstrate:  false,
			wantInSheet:      true,
			wantSheetName:    "Nicholas Braun",
		},
		{
			name:      "a confirmer the sheet can only render flattened",
			substrate: []confirmerLine{line("aaa", "Nicholas\tBraun")},
			sheet:     []confirmerLine{line("aaa", "Nicholas Braun")},
			want:      []string{},
		},
		{
			name: "every divergence is named, in id order",
			substrate: []confirmerLine{
				line("ccc", "Nicholas Braun"),
				line("aaa", ""),
				line("bbb", "Nicholas Braun"),
			},
			sheet: []confirmerLine{
				line("ccc", ""),
				line("aaa", "Nicholas Braun"),
				line("bbb", "Someone Else"),
			},
			want: []string{"aaa", "bbb", "ccc"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := confirmerDisagreements(tc.substrate, tc.sheet)
			ids := []string{}
			for _, m := range got {
				ids = append(ids, m.ID)
			}
			if !reflect.DeepEqual(ids, tc.want) {
				t.Fatalf("disagreements %v, want %v (%v)", ids, tc.want, got)
			}
			if !tc.assertMembership {
				return
			}
			m := got[0]
			if m.InSubstrate != tc.wantInSubstrate || m.InSheet != tc.wantInSheet {
				t.Errorf("row presence: in substrate %v, in sheet %v; want %v/%v", m.InSubstrate, m.InSheet, tc.wantInSubstrate, tc.wantInSheet)
			}
			if m.SubstrateConfirmer != tc.wantSubstrateName || m.SheetConfirmer != tc.wantSheetName {
				t.Errorf("confirmers: substrate %q, sheet %q; want %q/%q", m.SubstrateConfirmer, m.SheetConfirmer, tc.wantSubstrateName, tc.wantSheetName)
			}
			if !strings.Contains(m.String(), m.URL) {
				t.Errorf("the mismatch %q does not name the row's url %q", m, m.URL)
			}
		})
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

// boundaryCapture builds a synthetic capture whose four shapes span the whole
// decision space the boundary draw partitions: a posting-shaped URL both configs
// extract, a jobs-index terminal both configs skip, and two pages that clear every
// reject rung while carrying no Positive Evidence -- which today's blanket accept
// extracts and the tiered rule skips -- one per live verdict, so the accept and
// abstain halves of the disagreement are both exercised.
func boundaryCapture(t *testing.T) string {
	t.Helper()
	return writeCapture(t,
		// Agree, both extract: a job word in a non-terminal path segment is strong
		// Positive Evidence.
		capturedPage(t, "https://acme.test/jobs/senior-go-engineer", true, "2026-08-08T10:00:00Z", nil, "your tasks and your profile, apply now"),
		// Agree, both skip: a jobs-index terminal is rejected before rung 8.
		capturedPage(t, "https://acme.test/careers", true, "2026-08-08T10:00:01Z", nil, "our open roles"),
		// Disagree, live verdict accept: nothing rejects it, nothing admits it.
		capturedPage(t, "https://acme.test/company/we-grew", true, "2026-08-08T10:00:02Z", nil, "a note about our year"),
		// Disagree, live verdict abstain: the same shape on the other side of the tap.
		capturedPage(t, "https://acme.test/company/we-moved", false, "2026-08-08T10:00:03Z", nil, "a note about our office"),
	)
}

// boundarySubstrate writes a one-row substrate the boundary draw can extend, and
// returns its directory. The row is fully labelled and human-confirmed, so a draw
// that rewrote rather than appended would be visible.
func boundarySubstrate(t *testing.T, url string) string {
	t.Helper()
	dir := t.TempDir()
	existing := []goldRow{{
		URL: url, Verdict: true, TS: "2026-08-08T09:00:00Z",
		Stratum: stratumNoPosting, Weight: 1, Label: bench.ExtractDetail,
		LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-08T09:00:00Z", ConfirmedBy: "A Human", ConfirmedAt: "2026-08-08T09:00:00Z"},
		Content:         crawler.Content{Title: "already labelled"},
	}}
	if err := writeGoldSet(filepath.Join(dir, goldSetFile), existing); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}
	return dir
}

// TestBoundaryIsTheDisagreementSet pins what the drawing IS: exactly the pages the
// two gate configs decide differently, split by the live verdict, with the pages
// they agree on -- in either direction -- left out. The rung is purely additive, so
// the reversed set must be empty; the verb refuses to write when it is not.
func TestBoundaryIsTheDisagreementSet(t *testing.T) {
	capture := boundaryCapture(t)
	scan, err := scanCapture(capture)
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}

	accept, abstain, reversed, err := boundaryDisagreements(capture, scan)
	if err != nil {
		t.Fatalf("boundaryDisagreements: %v", err)
	}
	if len(reversed) != 0 {
		t.Errorf("the Positive Evidence rung added %d pages (%v); it is supposed to be purely additive", len(reversed), reversed)
	}
	if len(accept) != 1 || accept[0].URL != "https://acme.test/company/we-grew" {
		t.Errorf("accept half = %v, want exactly the accepted page the two configs disagree on", urlsOf(accept))
	}
	if len(abstain) != 1 || abstain[0].URL != "https://acme.test/company/we-moved" {
		t.Errorf("abstain half = %v, want exactly the abstained page the two configs disagree on", urlsOf(abstain))
	}
}

// urlsOf projects candidates to their URLs for a readable failure message.
func urlsOf(cands []candidate) []string {
	out := []string{}
	for _, c := range cands {
		out = append(out, c.URL)
	}
	return out
}

// TestBoundarySampleAppendsAndCensusWeights proves the verb is append-only with
// respect to labels -- the file it extends cost a review pass -- and that a census
// weights every row at exactly 1, which is what makes the per-drawing weight balance
// hold with no accept-share correction to get wrong.
func TestBoundarySampleAppendsAndCensusWeights(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	capture := boundaryCapture(t)

	if code := runGoldSetSampleBoundary([]string{"-capture", capture, "-dir", dir, "-since", "2026-08-07T21:13:00Z"}); code != 0 {
		t.Fatalf("goldset-sample-boundary exit code %d, want 0", code)
	}

	merged, err := readGoldSet(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("substrate has %d rows, want 2 (1 existing + 1 drawn)", len(merged))
	}
	drawn := []goldRow{}
	for _, row := range merged {
		if row.URL == "https://acme.test/jobs/already" {
			if row.Label != bench.ExtractDetail || row.LabelProvenance.ConfirmedBy != "A Human" || row.Stratum != stratumNoPosting {
				t.Errorf("the existing row was rewritten by the draw: %+v", row)
			}
			continue
		}
		drawn = append(drawn, row)
		if row.Stratum != stratumBoundary {
			t.Errorf("%s: drawn in stratum %q, want %q", row.URL, row.Stratum, stratumBoundary)
		}
		if row.Weight != boundaryCensusWeight {
			t.Errorf("%s: weight %g, want the census weight %g", row.URL, row.Weight, boundaryCensusWeight)
		}
		if !row.Verdict {
			t.Errorf("%s: drawn from the abstain half, which this drawing does not take", row.URL)
		}
		if row.Label != "" {
			t.Errorf("%s: a fresh draw arrived labelled %q", row.URL, row.Label)
		}
	}
	if !weightsBalanced(drawn, 1e-9) {
		t.Errorf("the drawn rows' weights sum to %.9f over %d rows, want equal", weightSum(drawn), len(drawn))
	}
}

// TestBoundarySampleRefusesADuplicateURL proves the draw is all-or-nothing: a page
// the substrate already carries would give one URL two drawings' incompatible
// weights, and the verb must leave the committed file untouched rather than write a
// corrupted one. The exclusion normally prevents this, so the guard is exercised by
// handing validation a draw the exclusion never saw.
func TestBoundarySampleRefusesADuplicateURL(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/company/we-grew")
	before, err := os.ReadFile(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("read substrate: %v", err)
	}

	// The only disagreeing accept page is the one the substrate already holds, so the
	// exclusion empties the draw and the verb refuses it rather than writing nothing
	// silently.
	if code := runGoldSetSampleBoundary([]string{"-capture", boundaryCapture(t), "-dir", dir, "-since", "2026-08-07T21:13:00Z"}); code != 2 {
		t.Errorf("exit code %d, want 2 (there is no boundary left to draw)", code)
	}
	after, err := os.ReadFile(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("read substrate: %v", err)
	}
	if string(before) != string(after) {
		t.Error("the verb rewrote the substrate despite refusing the draw")
	}

	drawn := []goldRow{{URL: "https://acme.test/company/we-grew", Verdict: true, Stratum: stratumBoundary, Weight: boundaryCensusWeight}}
	committed := map[string]struct{}{"https://acme.test/company/we-grew": {}}
	if err := validateDrawnBoundaryRows(drawn, committed); err == nil {
		t.Error("validateDrawnBoundaryRows accepted a row the substrate already carries")
	}
}

// TestCommittedBoundaryStratumIsTheDisagreementSet is the half of this drawing's
// provenance that is still a live guard: every committed row must be a page today's
// BLANKET ACCEPT extracts. That is a statement about the REJECT RUNGS, which the
// stratum was computed under at #263 and which #257 did not touch. It goes red if
// one of them moves, and a moved reject rung really does invalidate the draw.
//
// The other half -- that the candidate rule skips every row -- was true by
// construction at #263 and is no longer: #257 widened the rule and recovered 29 of
// these pages. TestCommittedBoundaryRecoveryLedger is what replaced it.
func TestCommittedBoundaryStratumIsTheDisagreementSet(t *testing.T) {
	baseline := boundaryBaselineConfig()
	rows := 0
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum != stratumBoundary {
			continue
		}
		rows++
		u, err := crawler.NewURL(row.URL)
		if err != nil {
			t.Fatalf("%s: %v", row.URL, err)
		}
		if !pagegate.ShouldExtract(u, &row.Content, baseline) {
			t.Errorf("%s: today's blanket accept SKIPS it, so it is not on the boundary. The Boundary Stratum was computed under the reject rungs as they stood at #263; a rung has changed, so the stratum no longer marks the boundary and must be re-drawn from the capture.", row.URL)
		}
	}
	if rows != boundaryStratumRows {
		t.Errorf("the boundary stratum has %d rows, want %d", rows, boundaryStratumRows)
	}
}

// TestCommittedBoundaryRecoveryLedger pins what the CURRENT Positive Evidence rung
// recovers from the Boundary Stratum, split by label.
//
// It exists because the stratum stopped being a live disagreement set at #257. It
// was drawn as the pages the #258 rule skipped, so "the candidate rule skips every
// row" was true by construction; widening the rule made 29 of them extract. The
// stratum was deliberately NOT re-drawn -- re-drawing would delete the 26 recovered
// postings from the record, which is precisely the evidence the widening produced,
// and would owe 188 fresh human confirmations nobody has budgeted. What it measures
// now is that recovery, and this ledger is the measurement.
//
// Pinned in BOTH directions and split by label on purpose: a later widening that
// recovers non-postings faster than postings must go red here rather than show up as
// a bigger total.
func TestCommittedBoundaryRecoveryLedger(t *testing.T) {
	rows, _, err := replayCaptured(filepath.Join("extract-goldset", goldSetFile), boundaryCandidateConfig())
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}

	recovered := map[bench.ExtractLabel]int{}
	total := 0
	for _, row := range rows {
		if row.Stratum != stratumBoundary || !row.Extract {
			continue
		}
		total++
		recovered[row.Label]++
	}

	nonPostings := recovered[bench.ExtractHubIndex] + recovered[bench.ExtractResidue]
	for _, c := range []struct {
		what string
		got  int
		want int
	}{
		{"boundary rows recovered", total, boundaryRowsRecovered},
		{"detail rows recovered", recovered[bench.ExtractDetail], boundaryDetailRecovered},
		{"non-posting rows recovered", nonPostings, boundaryNonPostingsRecovered},
		{"ambiguous rows recovered", recovered[bench.ExtractAmbiguous], boundaryAmbiguousRecovered},
	} {
		if c.got != c.want {
			t.Errorf("the Positive Evidence rung recovers %d %s, the ledger says %d; if the change is intended, move the constant and say what bought it", c.got, c.what, c.want)
		}
	}
	t.Logf("recovered from the boundary: %d rows (%d detail, %d non-posting, %d ambiguous)",
		total, recovered[bench.ExtractDetail], nonPostings, recovered[bench.ExtractAmbiguous])
}

// TestCommittedBoundaryStratumConfirmation is the ratchet ADR-0043 requires on this
// stratum: EVERY row must carry a human confirmation, because these are the rows a
// hard-zero false-drop guard is decided on. The count is a one-directional ratchet
// (confirmationFloor, ADR-0048) -- a rise fails, a fall is logged -- so a confirmation
// pass never turns the build red; a confirmation that vanished is caught by that
// direction and, row by row, by TestCommittedRecordAgreesOnWhoConfirmedWhat. It
// refuses a machine confirmer outright so the gap can never be closed by the tooling
// that opened it.
func TestCommittedBoundaryStratumConfirmation(t *testing.T) {
	pending := []string{}
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum != stratumBoundary {
			continue
		}
		if machineName(row.LabelProvenance.ConfirmedBy) {
			t.Errorf("%s: confirmed_by %q is a machine; a confirmation must come from a human", row.URL, row.LabelProvenance.ConfirmedBy)
		}
		if row.LabelProvenance.ProposedBy == "" || row.LabelProvenance.ProposedAt == "" {
			t.Errorf("%s: boundary row has no proposer (%+v)", row.URL, row.LabelProvenance)
		}
		if row.LabelProvenance.ConfirmedBy == "" {
			pending = append(pending, row.URL)
		}
	}
	confirmationFloor{
		constant: "pendingBoundaryConfirmations",
		counts:   "boundary rows await human confirmation (see extract-goldset/README.md)",
		current:  len(pending), recorded: pendingBoundaryConfirmations,
	}.assert(t)
}

// TestCommittedGoldSetAmbiguityIsRecorded pins the pages a reading could not settle.
// They are recorded rather than forced into a class, and excluded from scoring
// outright, so an undecidable page becomes neither a false-drop nor a leak. The
// count is asserted in both directions because it changes what every confusion
// number is computed over, and each one must carry the reason it could not be
// settled -- an unexplained shrug is not a finding a human can act on.
func TestCommittedGoldSetAmbiguityIsRecorded(t *testing.T) {
	ambiguous := []string{}
	for _, row := range loadCommittedGoldSet(t) {
		if row.Label != bench.ExtractAmbiguous {
			continue
		}
		ambiguous = append(ambiguous, row.URL)
		if row.LabelProvenance.Note == "" {
			t.Errorf("%s: labelled ambiguous with no stated tension", row.URL)
		}
		if row.Label.Scored() {
			t.Errorf("%s: the ambiguous label reports itself as scored", row.URL)
		}
	}
	if len(ambiguous) != ambiguousRows {
		t.Errorf("%d rows are labelled ambiguous but ambiguousRows is %d; set it to %d", len(ambiguous), ambiguousRows, len(ambiguous))
	}
	for _, url := range ambiguous {
		t.Logf("ambiguous: %s", url)
	}
}

// TestBoundaryStratumNeverEntersTheStreamEstimate is the acceptance criterion "the
// scorer never mixes them into the weighted stream estimates", asserted as a
// property rather than trusted as a convention: a census of a disagreement set
// describes no population, so weighting it into the stream numbers would corrupt
// every one of them. Scoring the committed file with the boundary rows removed must
// leave the stream scorecard byte-identical.
func TestBoundaryStratumNeverEntersTheStreamEstimate(t *testing.T) {
	rows, _, err := replayCaptured(filepath.Join("extract-goldset", goldSetFile), crawler.DefaultLLMGateConfig())
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}
	withoutBoundary := []captureRow{}
	for _, row := range rows {
		if row.Stratum != stratumBoundary {
			withoutBoundary = append(withoutBoundary, row)
		}
	}
	if len(rows)-len(withoutBoundary) != boundaryStratumRows {
		t.Fatalf("removed %d boundary rows, want %d", len(rows)-len(withoutBoundary), boundaryStratumRows)
	}

	all := bench.ScoreExtractStream(verdictRowsIn(rows, stratumRandom))
	trimmed := bench.ScoreExtractStream(verdictRowsIn(withoutBoundary, stratumRandom))
	if !reflect.DeepEqual(all, trimmed) {
		t.Errorf("the boundary rows moved the stream estimate:\n with %+v\n without %+v", all, trimmed)
	}
}

// TestCommittedBoundaryFalseDropsUnderTheCandidateRule reports the number this
// stratum exists to produce: how many real Job Listings the tiered Positive Evidence
// rule drops on the boundary. Until #257 that was exactly the detail-labelled rows,
// because the rule skipped every row here by construction; now it is the
// detail-labelled rows the widening did NOT recover.
//
// It is pinned in both directions as a TRIPWIRE on drift, NOT as the guard. These
// labels are LLM-proposed; until pendingBoundaryConfirmations is 0 the count is one
// model's opinion of another's, and the hard zero #264 must argue with is not this
// assertion. The unconfirmed count is the one figure here that is a ratchet rather
// than a pin (ADR-0048); the label censuses stay pinned as drift tripwires.
func TestCommittedBoundaryFalseDropsUnderTheCandidateRule(t *testing.T) {
	rows, _, err := replayCaptured(filepath.Join("extract-goldset", goldSetFile), boundaryCandidateConfig())
	if err != nil {
		t.Fatalf("replayCaptured: %v", err)
	}
	sc := bench.ScoreExtractBoundary(boundaryRowsOf(rows))

	if sc.Rows != boundaryStratumRows {
		t.Fatalf("scored %d boundary rows, want %d", sc.Rows, boundaryStratumRows)
	}
	if sc.Extracted != boundaryRowsRecovered {
		t.Errorf("the candidate rule extracts %d boundary rows, the recovery ledger says %d", sc.Extracted, boundaryRowsRecovered)
	}
	if sc.Counts[bench.ExtractDetail] != boundaryDetailRows {
		t.Errorf("%d boundary rows are labelled detail but boundaryDetailRows is %d; set it to %d",
			sc.Counts[bench.ExtractDetail], boundaryDetailRows, sc.Counts[bench.ExtractDetail])
	}
	if len(sc.FalseDrops) != boundaryFalseDropsRemaining {
		t.Errorf("the candidate rule drops %d real Job Listings here but boundaryFalseDropsRemaining is %d; set it to %d",
			len(sc.FalseDrops), boundaryFalseDropsRemaining, len(sc.FalseDrops))
	}
	if sc.AmbiguousSkipped != boundaryAmbiguousSkipped {
		t.Errorf("%d skipped boundary rows are ambiguous, want %d; an undecidable page is neither a false-drop nor forgiven", sc.AmbiguousSkipped, boundaryAmbiguousSkipped)
	}
	// The scorecard's own view of the same ratchet: it reads label_provenance through
	// replayCaptured rather than readGoldSet, so a fold that lost a confirmation shows
	// up here too. A ceiling for the same reason as everywhere else (ADR-0048).
	confirmationFloor{
		constant: "pendingBoundaryConfirmations",
		counts:   "boundary rows the scorecard reports unconfirmed",
		current:  sc.Unconfirmed, recorded: pendingBoundaryConfirmations,
	}.assert(t)
	t.Logf("boundary under the Positive Evidence rule: %d rows, %d false-drops, %d ambiguous-skipped, %d confirmed",
		sc.Rows, len(sc.FalseDrops), sc.AmbiguousSkipped, sc.Confirmed)
}

// TestConfirmSheetWithholdsTheStructuredData is the confirmation sheet's own
// discipline. The confirmer answers one question -- is this page one Job Listing? --
// and the number this stratum exists to produce is what the gate concluded about
// these very pages, so showing them the gate's conclusion would anchor the answer the
// rule is being judged against. The page's own words and the proposed label are
// shown; the structured data and the live verdict are not.
func TestConfirmSheetWithholdsTheStructuredData(t *testing.T) {
	rows := []goldRow{{
		URL: "https://acme.test/company/we-grew", Verdict: true, Stratum: stratumBoundary, Weight: 1,
		Label:           bench.ExtractResidue,
		LabelProvenance: goldProvenance{ProposedBy: "llm:test", Note: "a note about the year"},
		Content: crawler.Content{
			Title:       "We grew",
			MainContent: "SENTINEL-BODY a note about our year",
			JSONLD:      []string{`{"@type":"JobPosting","title":"SENTINEL-STRUCTURED"}`},
			URLs:        []string{"https://acme.test/jobs/one"},
		},
	}}

	chunks := renderConfirmSheet(rows, 20)
	if len(chunks) != 1 {
		t.Fatalf("rendered %d chunks over 1 row, want 1", len(chunks))
	}
	body := string(chunks[0].Body)
	for _, want := range []string{rowID(rows[0].URL), rows[0].URL, "SENTINEL-BODY", "residue", "a note about the year", "We grew"} {
		if !strings.Contains(body, want) {
			t.Errorf("the sheet withholds %q, which the confirmer needs", want)
		}
	}
	for _, leak := range []string{"SENTINEL-STRUCTURED", "JobPosting", "verdict", "@type"} {
		if strings.Contains(body, leak) {
			t.Errorf("the sheet shows %q, which would anchor the confirmation on the very signal being judged", leak)
		}
	}
	if len(chunks[0].IDs) != 1 || chunks[0].IDs[0] != rowID(rows[0].URL) {
		t.Errorf("chunk ids = %v, want the one row it covers", chunks[0].IDs)
	}
}

// TestConfirmSheetChunksInOrder pins the working shape of the human pass: fixed-size
// chunks in row-id order, so a partial pass is legitimate, resumable and quotable,
// and the order can never group the rows by the answer being checked.
func TestConfirmSheetChunksInOrder(t *testing.T) {
	rows := []goldRow{}
	for i := range 5 {
		rows = append(rows, goldRow{
			URL: fmt.Sprintf("https://acme.test/company/%d", i), Verdict: true, Stratum: stratumBoundary, Weight: 1,
			Label: bench.ExtractResidue, Content: crawler.Content{Title: "t", MainContent: "body"},
		})
	}

	chunks := renderConfirmSheet(rows, 2)
	if len(chunks) != 3 {
		t.Fatalf("rendered %d chunks over 5 rows at 2 per file, want 3", len(chunks))
	}
	seen := []string{}
	for i, chunk := range chunks {
		if want := fmt.Sprintf("confirm-%02d.md", i+1); chunk.Name != want {
			t.Errorf("chunk %d is named %q, want %q", i, chunk.Name, want)
		}
		seen = append(seen, chunk.IDs...)
	}
	if len(seen) != len(rows) {
		t.Fatalf("the chunks cover %d rows, want %d", len(seen), len(rows))
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Errorf("chunk ids are not in ascending row-id order: %v", seen)
			break
		}
	}
}

// TestApplyConfirmsExactlyTheListedIDs proves a chunk-by-chunk human pass is exact
// and reviewable: only the rows a human actually read gain a confirmer, so the diff
// shows precisely what was signed off. Every rejection path is asserted too, because
// a confirmation applied to the wrong row is worse than one not applied at all.
func TestApplyConfirmsExactlyTheListedIDs(t *testing.T) {
	rows := []goldRow{
		{URL: "https://acme.test/a", Stratum: stratumBoundary, Weight: 1, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-08T09:00:00Z"}},
		{URL: "https://acme.test/b", Stratum: stratumBoundary, Weight: 1, Label: bench.ExtractResidue,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-08T09:00:00Z"}},
		{URL: "https://acme.test/c", Stratum: stratumBoundary, Weight: 1},
	}
	stamp := "2026-08-08T12:00:00Z"

	merged, err := applyLabels(rows, nil, nil, "", "A Human", "", map[string]struct{}{rowID("https://acme.test/a"): {}}, stamp)
	if err != nil {
		t.Fatalf("applyLabels: %v", err)
	}
	if got := merged[0].LabelProvenance.ConfirmedBy; got != "A Human" {
		t.Errorf("the listed row was confirmed by %q, want %q", got, "A Human")
	}
	if got := merged[0].LabelProvenance.ConfirmedAt; got != stamp {
		t.Errorf("the listed row was confirmed at %q, want %q", got, stamp)
	}
	if got := merged[1].LabelProvenance.ConfirmedBy; got != "" {
		t.Errorf("an unlisted row was confirmed by %q; -confirm-ids names exactly the rows a human read", got)
	}

	if _, err := applyLabels(rows, nil, nil, "", "A Human", "", map[string]struct{}{"deadbeefdead": {}}, stamp); err == nil {
		t.Error("applyLabels accepted a confirmation for a row id that does not exist")
	}
	if _, err := applyLabels(rows, nil, nil, "", "A Human", "", map[string]struct{}{rowID("https://acme.test/c"): {}}, stamp); err == nil {
		t.Error("applyLabels accepted a confirmation for a row carrying no label")
	}

	if _, err := parseConfirmIDs([]byte("# nothing but a comment\n\n")); err == nil {
		t.Error("parseConfirmIDs accepted a file naming no row")
	}
	ids, err := parseConfirmIDs([]byte("# read on 2026-08-08\nabc123abc123\n\nfff000fff000\n"))
	if err != nil {
		t.Fatalf("parseConfirmIDs: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("parsed %d ids, want 2", len(ids))
	}
}

// TestApplyRefusesAnUnreviewableConfirmation covers the flag-level guards on
// -confirm-ids: two selectors at once leaves it unclear which rows were signed off,
// and an id list with no confirmer names rows without naming who read them. Both are
// refused before anything is read or written.
func TestApplyRefusesAnUnreviewableConfirmation(t *testing.T) {
	dir := t.TempDir()
	idFile := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(idFile, []byte("abc123abc123\n"), 0o644); err != nil {
		t.Fatalf("write id file: %v", err)
	}

	for name, args := range map[string][]string{
		"two selectors": {"-dir", dir, "-confirmed-by", "A Human", "-confirm-ids", idFile, "-confirm-stratum", string(stratumBoundary)},
		"no confirmer":  {"-dir", dir, "-confirm-ids", idFile},
		// A machine confirmer is refused by the SAME predicate on both flags. The
		// "script:" case is the one that used to slip through: -confirmed-by tested
		// only the "llm:" prefix while its sibling used machineName, so
		// `-confirmed-by "script:auto-confirm"` stamped labels as human-confirmed.
		"llm confirmer":             {"-dir", dir, "-confirmed-by", "llm:claude"},
		"script confirmer":          {"-dir", dir, "-confirmed-by", "script:auto-confirm"},
		"llm expected confirmer":    {"-dir", dir, "-expected-confirmed-by", "llm:claude"},
		"script expected confirmer": {"-dir", dir, "-expected-confirmed-by", "script:propose-expected"},
	} {
		if code := runGoldSetApply(args); code != 2 {
			t.Errorf("%s: exit code %d, want 2", name, code)
		}
	}
}

// TestApplyRefusesAMachineConfirmerOnTheSheet closes the second half of the same
// hole: the flag guard above is worthless if the labels.tsv confirmer column is
// copied through unchecked, because a machine-driven pass writes the column instead
// of passing the flag. The false-drop guard arms itself on these confirmations, so
// this is the tooling refusing to sign off the evidence its own labels are judged by
// (ADR-0043).
func TestApplyRefusesAMachineConfirmerOnTheSheet(t *testing.T) {
	rows := []goldRow{
		{URL: "https://a.test/jobs/1", Verdict: true, Stratum: stratumLonePosting, Label: bench.ExtractDetail},
	}
	sheet, err := parseSheet(renderSheet(rows))
	if err != nil {
		t.Fatalf("parseSheet: %v", err)
	}

	for _, name := range []string{"llm:claude", "script:auto-confirm"} {
		sheet[0].ConfirmedBy = name
		if _, err := applyLabels(rows, sheet, nil, "", "", "", nil, "2026-08-08T12:00:00Z"); err == nil {
			t.Errorf("applyLabels accepted a sheet confirmed by %q", name)
		}
	}

	sheet[0].ConfirmedBy = "A Human"
	merged, err := applyLabels(rows, sheet, nil, "", "", "", nil, "2026-08-08T12:00:00Z")
	if err != nil {
		t.Fatalf("applyLabels rejected a human confirmer: %v", err)
	}
	if merged[0].LabelProvenance.ConfirmedBy != "A Human" {
		t.Errorf("confirmer = %q, want %q", merged[0].LabelProvenance.ConfirmedBy, "A Human")
	}
}

// TestApplyRefusesAMachineConfirmerOnTheExpectedSheet closes the same hole on the
// OTHER sheet. -expected-confirmed-by rejects a machine name, but that guard is
// worthless if expected.tsv's confirmer column is copied through unchecked, since a
// machine-driven pass writes the column instead of passing the flag. What it buys is
// smaller than on the labels sheet -- the false-drop guard arms on LABEL
// confirmations, not on these -- but pendingExpectedConfirmations is a ratchet over
// the #256 field-fidelity ground truth, and a machine must not lower that either.
func TestApplyRefusesAMachineConfirmerOnTheExpectedSheet(t *testing.T) {
	const stamp = "2026-08-08T12:00:00Z"
	rows := []goldRow{
		{URL: "https://a.test/jobs/1", Stratum: stratumLonePosting, Label: bench.ExtractDetail},
	}
	sheetWith := func(confirmer string) []expectedSheetRow {
		return []expectedSheetRow{{
			ID: rowID(rows[0].URL), URL: rows[0].URL, Label: rows[0].Label,
			Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified",
			ConfirmedBy: confirmer,
		}}
	}

	for _, name := range []string{"llm:claude", "script:auto-confirm"} {
		if _, err := applyExpected(rows, sheetWith(name), "", "", stamp); err == nil {
			t.Errorf("applyExpected accepted an expected sheet confirmed by %q", name)
		}
	}

	got, err := applyExpected(rows, sheetWith("A Human"), "", "", stamp)
	if err != nil {
		t.Fatalf("applyExpected rejected a human confirmer: %v", err)
	}
	if got[0].Expected == nil {
		t.Fatal("no expected block was created")
	}
	if got[0].Expected.ConfirmedBy != "A Human" {
		t.Errorf("confirmer = %q, want %q", got[0].Expected.ConfirmedBy, "A Human")
	}
}

// TestApplyRetractsAConfirmationWhenAProposalChangesTheLabel pins the proposals
// path to the sheet path's rule. A human confirmed the OLD label; a proposer that
// replaces it must not inherit that signature, or the ledger's arming condition --
// "this label was read by a person" -- becomes a statement about a label nobody
// read. The row's empty Proposed Label stays empty through it: proposed_by is
// first-writer-wins, so filling it from the new label would pair the original
// proposer with an answer they never gave. A proposal that re-states the SAME label
// changes nothing and keeps the confirmation.
func TestApplyRetractsAConfirmationWhenAProposalChangesTheLabel(t *testing.T) {
	const stamp = "2026-08-08T12:00:00Z"
	base := func() []goldRow {
		return []goldRow{{
			URL: "https://a.test/jobs/1", Verdict: true, Stratum: stratumBoundary, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{
				ProposedBy: "llm:test", ProposedAt: "2026-08-01T00:00:00Z",
				ConfirmedBy: "A Human", ConfirmedAt: "2026-08-02T00:00:00Z",
			},
		}}
	}
	id := rowID("https://a.test/jobs/1")

	changed, err := applyLabels(base(), nil, []sheetRow{{ID: id, Label: bench.ExtractResidue}}, "", "", "", nil, stamp)
	if err != nil {
		t.Fatalf("applyLabels: %v", err)
	}
	if changed[0].Label != bench.ExtractResidue {
		t.Errorf("label = %q, want residue", changed[0].Label)
	}
	if got := changed[0].LabelProvenance.ConfirmedBy; got != "" {
		t.Errorf("confirmed_by = %q after a proposal changed the label; the human confirmed the old one", got)
	}
	if got := changed[0].LabelProvenance.ConfirmedAt; got != "" {
		t.Errorf("confirmed_at = %q after a retraction, want empty", got)
	}
	// The Proposed Label is retracted with the confirmation. proposed_by still names
	// the ORIGINAL proposer -- it is first-writer-wins -- and that proposer never said
	// anything about the label the proposal has just put on the row, so any value here
	// would be a pairing that never happened (ADR-0048).
	if got := changed[0].LabelProvenance.ProposedLabel; got != "" {
		t.Errorf("proposed_label = %q after a retraction, want empty: %q proposed detail, not that",
			got, changed[0].LabelProvenance.ProposedBy)
	}
	if got := changed[0].LabelProvenance.ProposedBy; got != "llm:test" {
		t.Errorf("proposed_by = %q, want the original proposer untouched by a retraction", got)
	}

	same, err := applyLabels(base(), nil, []sheetRow{{ID: id, Label: bench.ExtractDetail}}, "", "", "", nil, stamp)
	if err != nil {
		t.Fatalf("applyLabels: %v", err)
	}
	if same[0].LabelProvenance.ConfirmedBy != "A Human" || same[0].LabelProvenance.ConfirmedAt != "2026-08-02T00:00:00Z" {
		t.Errorf("provenance = %+v; a proposal re-stating the same label retracts nothing", same[0].LabelProvenance)
	}
	// The confirmation stands, so the row stays one the set cannot say what was
	// proposed on: a confirmed row's empty Proposed Label is never filled in.
	if got := same[0].LabelProvenance.ProposedLabel; got != "" {
		t.Errorf("proposed_label = %q on a row whose confirmation still stands, want empty", got)
	}
}

// TestApplySummaryReportsAgreementWithTheProposer pins the by-product a blind
// confirmation pass exists to produce (ADR-0048): how often an independent human
// reached the label the proposer proposed. Only rows where BOTH are known are
// comparable -- a confirmation given before the Proposed Label existed says nothing
// about agreement, and counting it as agreement would inflate the very number the
// still-unconfirmed rows are trusted on.
func TestApplySummaryReportsAgreementWithTheProposer(t *testing.T) {
	row := func(url string, label, proposed bench.ExtractLabel, confirmer string) goldRow {
		return goldRow{
			URL: url, Verdict: true, Stratum: stratumLonePosting, Weight: 1, Label: label,
			LabelProvenance: goldProvenance{
				ProposedBy: "llm:test", ProposedAt: "2026-08-01T00:00:00Z",
				ProposedLabel: proposed, ConfirmedBy: confirmer,
			},
		}
	}

	rows := []goldRow{
		row("https://a.test/1", bench.ExtractDetail, bench.ExtractDetail, "A Human"),
		row("https://a.test/2", bench.ExtractResidue, bench.ExtractResidue, "A Human"),
		row("https://a.test/3", bench.ExtractDetail, bench.ExtractHubIndex, "A Human"),
		// Confirmed before the Proposed Label existed: not comparable either way.
		row("https://a.test/4", bench.ExtractDetail, "", "A Human"),
		// Proposed but never confirmed: there is no independent reading to compare to.
		row("https://a.test/5", bench.ExtractDetail, bench.ExtractDetail, ""),
	}
	if agreed, comparable := labelAgreement(rows); agreed != 2 || comparable != 3 {
		t.Errorf("labelAgreement = %d/%d, want 2/3 (the pre-field and unconfirmed rows count in neither)", agreed, comparable)
	}

	var buf bytes.Buffer
	printApplySummary(&buf, rows)
	if want := "agreement         2/3 (66.7%)"; !strings.Contains(buf.String(), want) {
		t.Errorf("summary does not report %q:\n%s", want, buf.String())
	}

	// A set whose confirmations all predate the field has an empty denominator, which
	// must read as "nothing to compare" rather than as a NaN percentage.
	none := []goldRow{
		row("https://b.test/1", bench.ExtractDetail, "", "A Human"),
		row("https://b.test/2", bench.ExtractDetail, bench.ExtractDetail, ""),
	}
	buf.Reset()
	printApplySummary(&buf, none)
	if !strings.Contains(buf.String(), "agreement         0/0") {
		t.Errorf("summary does not report an empty agreement:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "NaN") {
		t.Errorf("summary divided by zero:\n%s", buf.String())
	}
}

// TestCapturedRendererReachesTheGoldSetRow drives the ticket's round trip through
// the capture file itself (#281): the live tap writes a record, the gold-set
// decoder reads it, and the renderer survives into a written substrate row. The
// capture is produced by internal/extractcapture rather than a hand-built line, so
// this fails if the two formats ever drift apart.
func TestCapturedRendererReachesTheGoldSetRow(t *testing.T) {
	const (
		renderer    = "structural-v1"
		url         = "https://acme.test/jobs/go-dev"
		mainContent = "#\tOpen positions\n-\t[Go Developer](/jobs/go-dev)"
	)

	capturePath := filepath.Join(t.TempDir(), "capture.jsonl")
	hook, closer, err := extractcapture.New(capturePath, 0, renderer)
	if err != nil {
		t.Fatalf("extractcapture.New: %v", err)
	}
	hook(t.Context(), url, true, &crawler.Content{Title: "Go Developer", MainContent: mainContent, JSONLD: []string{lonePostingLD}})
	if err := closer.Close(); err != nil {
		t.Fatalf("close capture: %v", err)
	}

	line, err := os.ReadFile(capturePath)
	if err != nil {
		t.Fatalf("read capture: %v", err)
	}
	var row goldRow
	if err := json.Unmarshal(line, &row); err != nil {
		t.Fatalf("decode capture line %s as a goldRow: %v", line, err)
	}
	if row.Renderer != renderer {
		t.Errorf("renderer = %q, want %q -- the tap's stamp must reach the gold-set decoder", row.Renderer, renderer)
	}
	if row.URL != url || !row.Verdict || row.Content.MainContent != mainContent {
		t.Errorf("the stamp disturbed the record beside it: url=%q verdict=%v main content=%q", row.URL, row.Verdict, row.Content.MainContent)
	}

	out := filepath.Join(t.TempDir(), goldSetFile)
	if err := writeGoldSet(out, []goldRow{row}); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}
	written, err := readGoldSet(out)
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("wrote 1 row, read back %d", len(written))
	}
	if written[0].Renderer != renderer {
		t.Errorf("renderer = %q after a substrate round trip, want %q", written[0].Renderer, renderer)
	}
	if written[0].Content.MainContent != mainContent {
		t.Errorf("main content = %q after a substrate round trip, want %q", written[0].Content.MainContent, mainContent)
	}
}

// TestPreStampCaptureRowCarriesNoRenderer verifies a capture record written before
// the renderer stamp existed still decodes, and re-serializes with no renderer at
// all (#281). An empty-but-present renderer would be a claim the row cannot
// support: those rows carry an UNKNOWN renderer and are ADR-0047's population.
func TestPreStampCaptureRowCarriesNoRenderer(t *testing.T) {
	line := capturedPage(t, "https://acme.test/jobs/old", true, "2026-08-01T10:00:00Z", []string{lonePostingLD}, "apply now")

	var row goldRow
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		t.Fatalf("decode a pre-stamp capture line: %v", err)
	}
	if row.Renderer != "" {
		t.Errorf("renderer = %q on a row that never carried one, want empty", row.Renderer)
	}
	if row.URL == "" || row.Content.MainContent == "" {
		t.Fatalf("the pre-stamp line did not decode: %+v", row)
	}

	out := filepath.Join(t.TempDir(), goldSetFile)
	if err := writeGoldSet(out, []goldRow{row}); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}
	written, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read re-written row: %v", err)
	}
	if strings.Contains(string(written), `"renderer"`) {
		t.Errorf("re-serializing a pre-stamp row emitted a renderer key: %s", written)
	}
}

// TestCommittedGoldSetIsByteStableThroughTheDecoder reads the committed substrate
// and writes it straight back, asserting the bytes are identical. Every goldset-*
// verb rewrites the whole file, so a row-format change that decodes-and-re-encodes
// differently rewrites all 457 rows the next time anyone applies a label -- a 6.3 MB
// diff nobody reviews, hiding whatever real change came with it.
//
// It is also what fails if the "renderer" field ever loses its omitempty (#281): the
// rows drawn before the stamp existed carry an UNKNOWN renderer, not an empty one,
// and re-serializing must leave them exactly as they are.
func TestCommittedGoldSetIsByteStableThroughTheDecoder(t *testing.T) {
	path := filepath.Join("extract-goldset", goldSetFile)
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed gold set: %v", err)
	}

	rows, err := readGoldSet(path)
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	out := filepath.Join(t.TempDir(), goldSetFile)
	if err := writeGoldSet(out, rows); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read re-written gold set: %v", err)
	}

	if !bytes.Equal(got, committed) {
		offset := len(got)
		for i := range got {
			if i >= len(committed) || got[i] != committed[i] {
				offset = i
				break
			}
		}
		t.Fatalf("re-serializing the committed gold set changed it: %d rows, %d bytes written against %d committed, first difference at byte %d\n"+
			"a round trip through readGoldSet/writeGoldSet must be the identity, or every goldset-* verb rewrites the whole substrate",
			len(rows), len(got), len(committed), offset)
	}
}

// TestWriteGoldSetLeavesTheSubstrateIntactWhenARowFailsToEncode is the interrupted
// write driven through the real production function, with no test-only seam: the
// encoder writes the first rows into the staging file and then refuses the last one,
// which is exactly the state a killed process or a full disk leaves behind. The
// committed 6.4 MB of ground truth must still be there, complete and readable (#283).
//
// A NaN weight is the fault used because it is the only one a goldRow can carry that
// the JSON encoder rejects, and it is a fault the substrate can never legitimately
// hold -- every weight is finite by construction -- so the test cannot mask a real
// defect. The failing row sorts LAST so the rows before it are already written when
// it fails.
func TestWriteGoldSetLeavesTheSubstrateIntactWhenARowFailsToEncode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, goldSetFile)

	good := []goldRow{
		{
			URL: "https://a.test/1", Verdict: true, TS: "2026-08-20T10:00:00Z",
			Stratum: stratumLonePosting, Weight: 1, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-20T10:00:00Z", ConfirmedBy: "A Human", ConfirmedAt: "2026-08-20T10:00:00Z"},
			Content:         crawler.Content{Title: "Senior Go Engineer", MainContent: "your tasks"},
		},
		{
			URL: "https://b.test/2", Verdict: false, TS: "2026-08-20T10:00:01Z",
			Stratum: stratumNoPosting, Weight: 1, Label: bench.ExtractResidue,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-20T10:00:01Z"},
			Content:         crawler.Content{Title: "About us"},
		},
	}
	if err := writeGoldSet(path, good); err != nil {
		t.Fatalf("writeGoldSet: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the committed substrate: %v", err)
	}

	doomed := append(append([]goldRow{}, good...), goldRow{
		URL: "https://z.test/3", Stratum: stratumRandom, Weight: math.NaN(),
		Content: crawler.Content{Title: "the row the encoder refuses"},
	})
	err = writeGoldSet(path, doomed)
	if err == nil {
		t.Fatal("writeGoldSet returned nil for a row the encoder cannot serialize")
	}
	if !strings.Contains(err.Error(), "https://z.test/3") {
		t.Errorf("error %q does not name the row it failed on", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the substrate after the failed write: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatalf("the failed write changed the substrate: %d bytes now, %d before -- an interrupted write must leave the committed file exactly as it was", len(after), len(before))
	}
	rows, err := readGoldSet(path)
	if err != nil {
		t.Fatalf("readGoldSet after the failed write: %v", err)
	}
	if len(rows) != len(good) {
		t.Fatalf("read %d rows back, want the %d that were committed before the failed write", len(rows), len(good))
	}
	if rows[0].URL != good[0].URL || rows[1].URL != good[1].URL {
		t.Errorf("read back %q and %q, want %q and %q", rows[0].URL, rows[1].URL, good[0].URL, good[1].URL)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != goldSetFile {
		t.Errorf("directory holds %d entries after the failed write, want only %s -- nothing half-written may survive", len(entries), goldSetFile)
	}
}

// TestWriteGoldSetFilesReplacesEveryCommittedFile pins the seam the goldset-* verbs
// write through: the substrate and BOTH review sheets land together, each rendered
// from the same rows, each 0644 (#283). It is the drift the two committed-sheet
// tests assert on the real files, checked here at the writer instead.
func TestWriteGoldSetFilesReplacesEveryCommittedFile(t *testing.T) {
	dir := t.TempDir()
	rows := []goldRow{
		{
			URL: "https://a.test/jobs/1", Verdict: true, TS: "2026-08-20T10:00:00Z",
			Stratum: stratumLonePosting, Weight: 1, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-20T10:00:00Z", ConfirmedBy: "A Human", ConfirmedAt: "2026-08-20T10:00:00Z"},
			Expected:        &goldExpected{Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified", ProposedBy: "script:test"},
			Content:         crawler.Content{Title: "Senior Go Engineer", MainContent: "your tasks"},
		},
		{
			URL: "https://b.test/about", Verdict: false, TS: "2026-08-20T10:00:01Z",
			Stratum: stratumNoPosting, Weight: 1, Label: bench.ExtractResidue,
			LabelProvenance: goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-20T10:00:01Z"},
			Content:         crawler.Content{Title: "About us"},
		},
	}

	substrate, sheet := goldSetPaths(dir)
	expected := expectedSheetPath(dir)
	for _, path := range []string{substrate, sheet, expected} {
		if err := os.WriteFile(path, []byte("stale\n"), 0o644); err != nil {
			t.Fatalf("seed %q: %v", path, err)
		}
	}

	if err := writeGoldSetFiles(dir, rows); err != nil {
		t.Fatalf("writeGoldSetFiles: %v", err)
	}

	got, err := readGoldSet(substrate)
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	if len(got) != len(rows) {
		t.Fatalf("substrate holds %d rows, want %d", len(got), len(rows))
	}
	if !reflect.DeepEqual(got, rows) {
		t.Errorf("the substrate did not round-trip the rows it was written from")
	}
	if data, err := os.ReadFile(sheet); err != nil {
		t.Fatalf("read %q: %v", sheet, err)
	} else if !bytes.Equal(data, renderSheet(rows)) {
		t.Errorf("%s is not the sheet rendered from the rows it was written with", labelsFile)
	}
	if data, err := os.ReadFile(expected); err != nil {
		t.Fatalf("read %q: %v", expected, err)
	} else if !bytes.Equal(data, renderExpectedSheet(rows)) {
		t.Errorf("%s is not the sheet rendered from the rows it was written with", expectedFile)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("directory holds %d entries, want exactly the three committed files with no staging leftovers", len(entries))
	}
	for _, path := range []string{substrate, sheet, expected} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if perm := info.Mode().Perm(); perm != goldSetPerm {
			t.Errorf("%s mode = %v, want %v", filepath.Base(path), perm, goldSetPerm)
		}
	}
}

// TestConfirmSheetStatesTheSharedRubric pins the rubric extraction (#286). The
// confirmation sheet and goldset-ui must put ONE question and ONE set of definitions
// to the same human, so both render extractLabelRubric -- and the sheet's bytes are
// still the ones it has always printed. The literal below is that wording, kept here
// on purpose: a change to the shared value then has to be made deliberately rather
// than drifting out of the confirmation sheet unnoticed.
func TestConfirmSheetStatesTheSharedRubric(t *testing.T) {
	const want = "One question per row: **is this page ONE Job Listing?**\n\n" +
		"- `detail` -- the page IS one Job Listing: one role's responsibilities or requirements, and an apply action.\n" +
		"- `hub-index` -- the page LISTS openings (a board root, a search result, a location or department facet). A page listing exactly one opening is still `hub-index`.\n" +
		"- `residue` -- neither: culture, about, benefits, blog, press, contact, a cookie or login wall, a JS shell, a 404, a salary guide, a \"post a job\" form, or a withdrawn posting with no role body.\n" +
		"- `ambiguous` -- the page genuinely does not resolve. Say what the tension is in the note.\n\n"

	rows := []goldRow{{
		URL: "https://acme.test/jobs/1", Verdict: true, Stratum: stratumBoundary, Weight: 1,
		Label: bench.ExtractResidue, Content: crawler.Content{Title: "t", MainContent: "body"},
	}}
	chunks := renderConfirmSheet(rows, 20)
	if len(chunks) != 1 {
		t.Fatalf("rendered %d chunks over 1 row, want 1", len(chunks))
	}
	body := string(chunks[0].Body)
	if !strings.Contains(body, want) {
		t.Errorf("the confirmation sheet no longer prints the rubric byte for byte:\n%s", body)
	}
	if !strings.Contains(body, extractLabelQuestion) {
		t.Errorf("the sheet asks a different question than goldset-ui does")
	}
	for _, e := range extractLabelRubric {
		if !strings.Contains(body, e.Text) {
			t.Errorf("the sheet omits the definition of %q, which goldset-ui states", e.Label)
		}
	}
}

// TestApplyRetractionNeverInventsAProposedLabel walks the two ways goldset-apply can
// overturn a label a human already signed, on a row that records no Proposed Label --
// the shape of every row confirmed before the field existed. proposed_by is
// first-writer-wins, so it still names the proposer of the label that was just
// replaced; filling the empty field from the REPLACEMENT would assert that proposer
// gave an answer they were never asked for, and labelAgreement would then score the
// row as an independent human reaching the proposer's conclusion -- inflating the
// exact number a blind pass exists to produce (ADR-0048), on a row nobody
// independently agreed on.
func TestApplyRetractionNeverInventsAProposedLabel(t *testing.T) {
	const stamp = "2026-08-20T12:00:00Z"
	const url = "https://a.test/jobs/1"
	id := rowID(url)

	// The shape of the 78 rows a human confirmed before the Proposed Label existed:
	// a proposer, no proposed_label, a human's signature on the label they read.
	preField := func() []goldRow {
		return []goldRow{{
			URL: url, Verdict: true, Stratum: stratumLonePosting, Weight: 1, Label: bench.ExtractDetail,
			LabelProvenance: goldProvenance{
				ProposedBy: "llm:claude-opus-5", ProposedAt: "2026-08-01T00:00:00Z",
				ConfirmedBy: "Nicholas Braun", ConfirmedAt: "2026-08-02T00:00:00Z",
			},
		}}
	}

	t.Run("a proposal that overturns a confirmation, re-confirmed in the same run", func(t *testing.T) {
		proposals := []sheetRow{{ID: id, Label: bench.ExtractResidue, Note: "an index of roles"}}
		got, err := applyLabels(preField(), nil, proposals, "", "Nicholas Braun", "", map[string]struct{}{id: {}}, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		prov := got[0].LabelProvenance
		if got[0].Label != bench.ExtractResidue {
			t.Errorf("label = %q, want residue", got[0].Label)
		}
		if prov.ProposedLabel != "" {
			t.Errorf("proposed_label = %q, want empty: %q proposed detail and was never asked about residue",
				prov.ProposedLabel, prov.ProposedBy)
		}
		if prov.ConfirmedBy != "Nicholas Braun" || prov.ConfirmedAt != stamp {
			t.Errorf("provenance = %+v, want the fresh signature on the new label", prov)
		}
		if agreed, comparable := labelAgreement(got); agreed != 0 || comparable != 0 {
			t.Errorf("labelAgreement = %d/%d, want 0/0: nobody independently reached this label", agreed, comparable)
		}
	})

	t.Run("a sheet that overturns a confirmation, re-confirmed in the same run", func(t *testing.T) {
		sheet := []sheetRow{{
			ID: id, URL: url, Stratum: stratumLonePosting, Verdict: true,
			Label: bench.ExtractResidue, Note: "an index of roles",
		}}
		got, err := applyLabels(preField(), sheet, nil, "", "Nicholas Braun", "", map[string]struct{}{id: {}}, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		prov := got[0].LabelProvenance
		if got[0].Label != bench.ExtractResidue {
			t.Errorf("label = %q, want residue", got[0].Label)
		}
		if prov.ProposedLabel != "" {
			t.Errorf("proposed_label = %q, want empty: %q proposed detail and was never asked about residue",
				prov.ProposedLabel, prov.ProposedBy)
		}
		if agreed, comparable := labelAgreement(got); agreed != 0 || comparable != 0 {
			t.Errorf("labelAgreement = %d/%d, want 0/0: nobody independently reached this label", agreed, comparable)
		}
	})

	// The other half of the rule: a Proposed Label that is ALREADY on the record is a
	// true statement about the proposer, so a retraction leaves it exactly where it is.
	// The relabel is then a disagreement the set can count -- which is what the UI's
	// undo-and-relabel does on every row it has already written.
	t.Run("a retraction keeps a Proposed Label that was truthfully recorded", func(t *testing.T) {
		rows := preField()
		rows[0].LabelProvenance.ProposedLabel = bench.ExtractDetail
		proposals := []sheetRow{{ID: id, Label: bench.ExtractResidue, Note: "an index of roles"}}
		got, err := applyLabels(rows, nil, proposals, "", "Nicholas Braun", "", map[string]struct{}{id: {}}, stamp)
		if err != nil {
			t.Fatalf("applyLabels: %v", err)
		}
		if prov := got[0].LabelProvenance; prov.ProposedLabel != bench.ExtractDetail {
			t.Errorf("proposed_label = %q, want the detail the proposer really did propose", prov.ProposedLabel)
		}
		if agreed, comparable := labelAgreement(got); agreed != 0 || comparable != 1 {
			t.Errorf("labelAgreement = %d/%d, want 0/1: a human read the page and reached the other answer", agreed, comparable)
		}
	})
}
