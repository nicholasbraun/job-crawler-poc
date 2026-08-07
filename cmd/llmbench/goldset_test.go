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
// committed, stratified sample of pages the LIVE extract stage actually decided
// on, stored as the parsed Content the pipeline itself produced (ADR-0043) --
// never re-fetched HTML, because a page re-fetched months later is a different
// page and the drift corrupts the label.
//
// Provenance: the extract-decision tap's local capture
// (EXTRACT_CAPTURE_PATH=<repo>/capture/extract-capture.jsonl, gitignored), 5669
// records captured 2026-07-24T22:51Z .. 2026-08-06T21:27Z across three collection
// sessions; deduped by URL to 4271 pages (latest capture wins, 3 oversized lines
// dropped); sampled 2026-08-07 by `llmbench goldset-sample -seed
// extract-goldset-v1` under the plan in goldset.go. Labels were PROPOSED BY AN
// LLM (the #254 delivery agent) from each page's own title, text and link
// structure with the structured data deliberately withheld, and are confirmed by
// a human on the lone-posting stratum -- the rows a later guard is decided on.
// Each row records both in label_provenance; pendingHumanConfirmations below is
// the machine-visible count of what is still unconfirmed.

// pendingHumanConfirmations is how many lone-posting rows still carry an
// LLM-proposed label no human has confirmed. #254 requires 0: the labels were
// proposed by an agent and a human confirms them at the review gate, so this
// starts at the full stratum count and MUST be driven to 0 by that commit. It is a
// RATCHET -- lower it as confirmations land, never raise it.
const pendingHumanConfirmations = 70

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
// the structured data or the stratum derived from it. A labeler who can see that a
// page publishes a lone JobPosting would agree with the structured data by
// construction -- the exact circularity ADR-0043 exists to end.
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

	if len(rows) < 135 || len(rows) > 165 {
		t.Errorf("gold set has %d rows, want approximately 150", len(rows))
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
		if row.Expected != nil && row.Expected.Title == "" {
			t.Errorf("%s: expected extraction has no title", row.URL)
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

	report := bench.ScoreExtract(rows)
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
