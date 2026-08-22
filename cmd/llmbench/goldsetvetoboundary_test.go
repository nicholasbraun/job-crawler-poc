package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// This file is the Learned Veto boundary drawing's table (ADR-0049, #304). Every
// fixture in it is threshold-sensitive: the cases below only mean what they say while
// the committed weights rank them where they do, so each is built through a
// precondition guard that names the repair when a refit moves one. This is the THIRD
// site holding fixtures of that kind, beside internal/pagegate/learned_veto_test.go
// and internal/processor/url_processor/url_processor_test.go; the repository README's
// rollout section lists all three.

// vetoWindowStart is the -since cutoff every capture in this file is framed at. The
// drawing requires one, for the sibling's reason: content from a superseded parser is
// a different page.
const vetoWindowStart = "2026-08-08T00:00:00Z"

const (
	// richPostingTitle and richPostingBody are the shape the Posting Score was fitted
	// to rank highest -- five posting sections, an apply affordance and a role
	// designation -- taken from the rung's own table in
	// internal/pagegate/learned_veto_test.go so the two files agree about what a strong
	// page looks like.
	richPostingTitle = "Senior Engineer (m/w/d) gesucht"
	richPostingBody  = "Ihre Aufgaben. Ihr Profil. Wir bieten. Vollzeit. Ansprechpartner. " +
		"Jetzt bewerben. Wir freuen uns auf Ihre Bewerbung. Vergütung nach Tarif. Arbeiten bei uns."
)

// requireVetoDrops builds one capture line for a page the Learned Veto must WITHHOLD
// the extractor call from, after asserting both halves of that precondition: today's
// shipping gate EXTRACTS the page -- so it is inside the depth's denominator, and a
// page a reject rung already sheds can never pass a veto assertion for the wrong
// reason -- and its Posting Score is below the threshold.
//
// A retrain that moves the weights is expected to break these preconditions before it
// breaks any assertion, which is the point: the message names the repair.
func requireVetoDrops(t *testing.T, url string, verdict bool, ts, title, mainContent string) string {
	t.Helper()
	u, content := requireGateExtracts(t, url, title, mainContent)
	if got := pagegate.Score(u, content); got >= pagegate.VetoThreshold {
		t.Fatalf("%s scores %.6f, at or above VetoThreshold %.6f: this case needs a page the veto drops. "+
			"A retrain moved the weights -- pick a weaker fixture; never move the threshold to fit a test.",
			url, got, pagegate.VetoThreshold)
	}
	return capturedPageTitled(t, url, verdict, ts, title, nil, mainContent)
}

// requireVetoKeeps is requireVetoDrops's mirror, for the pages the veto must let
// through -- the survivors that make a depth a share rather than a total.
func requireVetoKeeps(t *testing.T, url string, verdict bool, ts, title, mainContent string) string {
	t.Helper()
	u, content := requireGateExtracts(t, url, title, mainContent)
	if got := pagegate.Score(u, content); got < pagegate.VetoThreshold {
		t.Fatalf("%s scores %.6f, below VetoThreshold %.6f: this case needs a page the veto keeps. "+
			"A retrain moved the weights -- pick a stronger fixture; never move the threshold to fit a test.",
			url, got, pagegate.VetoThreshold)
	}
	return capturedPageTitled(t, url, verdict, ts, title, nil, mainContent)
}

// requireGateExtracts fails unless the shipping gate extracts the page, and returns
// the parsed URL and content both guards then score.
func requireGateExtracts(t *testing.T, url, title, mainContent string) (crawler.URL, *crawler.Content) {
	t.Helper()
	u, err := crawler.NewURL(url)
	if err != nil {
		t.Fatalf("url %q: %v", url, err)
	}
	content := &crawler.Content{Title: title, MainContent: mainContent}
	if !pagegate.ShouldExtract(u, content, vetoBaselineConfig()) {
		t.Fatalf("%s: today's gate does not extract it, so it is outside the veto's population; "+
			"a reject rung moved -- pick a fixture the gate still admits", url)
	}
	return u, content
}

// vetoCapture is a four-page capture spanning every cell of the veto's boundary: a
// strong page both configs extract, a jobs-index terminal today's gate rejects before
// the veto is consulted, and two pages the gate extracts and the veto withholds -- one
// per live verdict, so both halves of the disagreement are exercised.
//
// The two low pages are the shape internal/pagegate's own table calls the least
// retrain-sensitive low fixture available: a posting-shaped URL admitted by Positive
// Evidence on the URL alone, whose empty content emits no Score Signals at all, so it
// scores the bare fitted intercept.
func vetoCapture(t *testing.T) string {
	t.Helper()
	return writeCapture(t,
		requireVetoKeeps(t, "https://acme.test/jobs/senior-go-engineer", true, "2026-08-08T10:00:00Z", richPostingTitle, richPostingBody),
		capturedPage(t, "https://acme.test/careers", true, "2026-08-08T10:00:01Z", nil, "our open roles"),
		requireVetoDrops(t, "https://acme.test/jobs/quiet-role", true, "2026-08-08T10:00:02Z", "", ""),
		requireVetoDrops(t, "https://acme.test/jobs/silent-role", false, "2026-08-08T10:00:03Z", "", ""),
	)
}

// replayVetoCapture scans a capture, frames it at vetoWindowStart and replays the
// veto's pair over it -- the same three calls runBoundaryDrawing makes.
func replayVetoCapture(t *testing.T, capture string, d boundaryDesign) boundaryOutcome {
	t.Helper()
	scan, err := scanCapture(capture)
	if err != nil {
		t.Fatalf("scanCapture: %v", err)
	}
	framed, _ := frameSince(scan, mustParseVetoTime(t, vetoWindowStart))
	outcome, err := replayBoundary(capture, framed, d)
	if err != nil {
		t.Fatalf("replayBoundary: %v", err)
	}
	return outcome
}

// mustParseVetoTime parses an RFC3339 cutoff or fails the test.
func mustParseVetoTime(t *testing.T, ts string) time.Time {
	t.Helper()
	cutoff, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		t.Fatalf("parse %q: %v", ts, err)
	}
	return cutoff
}

// vetoDrawArgs is the drawing's arguments for one test capture and substrate.
func vetoDrawArgs(t *testing.T, capture, dir string, draw bool) boundaryDrawArgs {
	t.Helper()
	return boundaryDrawArgs{
		Design:  learnedVetoBoundary,
		Capture: capture,
		Dir:     dir,
		Since:   mustParseVetoTime(t, vetoWindowStart),
		Draw:    draw,
	}
}

// TestVetoBoundaryIsTheDropSet pins what the reading IS: of the pages today's gate
// extracts, the ones rung 9 would withhold the call from, split by the live
// extractor's verdict but never filtered by it, with the pages the pair agrees on --
// in either direction -- left out of the numerator.
func TestVetoBoundaryIsTheDropSet(t *testing.T) {
	outcome := replayVetoCapture(t, vetoCapture(t), learnedVetoBoundary)

	t.Run("the denominator is what today's gate extracts", func(t *testing.T) {
		if outcome.Frame != 4 {
			t.Errorf("frame = %d, want the 4 captured pages", outcome.Frame)
		}
		// The jobs-index terminal is rejected before the veto is consulted, so it is
		// outside the population the depth is a share of.
		if outcome.BaselineAccepts != 3 {
			t.Errorf("gate extracts %d of the frame, want 3 (the index terminal is rejected before rung 9)", outcome.BaselineAccepts)
		}
	})

	t.Run("the drop set is both verdict halves", func(t *testing.T) {
		if got := urlsOf(outcome.DroppedAccepted); len(got) != 1 || got[0] != "https://acme.test/jobs/quiet-role" {
			t.Errorf("live-accept half = %v, want the accepted page the veto withholds", got)
		}
		if got := urlsOf(outcome.DroppedAbstained); len(got) != 1 || got[0] != "https://acme.test/jobs/silent-role" {
			t.Errorf("live-abstain half = %v, want the abstained page the veto withholds", got)
		}
		if got := len(outcome.Census(learnedVetoBoundary)); got != 2 {
			t.Errorf("census holds %d pages, want both halves (2); ADR-0049 forbids filtering the drop set by the extractor's own verdict", got)
		}
	})

	t.Run("the veto only ever subtracts", func(t *testing.T) {
		if len(outcome.Reversed) != 0 {
			t.Errorf("the veto ADDED %d pages (%v); it can only withhold a call, never add one", len(outcome.Reversed), outcome.Reversed)
		}
	})

	t.Run("the depth is the drop set over the denominator", func(t *testing.T) {
		if got := outcome.Depth(); got < 0.6666 || got > 0.6668 {
			t.Errorf("depth = %.4f, want 2/3", got)
		}
	})
}

// TestVetoBoundaryDrawsBothHalvesOfTheDisagreement runs the verb end to end and holds
// the property the stratum exists for: every page below the cut is drawn, INCLUDING
// the one the live extractor abstained on. ADR-0049 rules out grading this rung
// against that verdict, so a drop set filtered by it would import the extractor's
// 0.454 precision into the sample before a human ever reads a row.
//
// It is also the regression test for the two-cell census selection: a one-cell census
// would write the abstain row at weight 0 and unbalance the drawing.
func TestVetoBoundaryDrawsBothHalvesOfTheDisagreement(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	capture := vetoCapture(t)

	code := runGoldSetSampleVetoBoundary([]string{"-capture", capture, "-dir", dir, "-since", vetoWindowStart, "-draw"})
	if code != 0 {
		t.Fatalf("goldset-sample-veto-boundary exit code %d, want 0", code)
	}

	merged, err := readGoldSet(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	drawn := []goldRow{}
	for _, row := range merged {
		if row.URL == "https://acme.test/jobs/already" {
			continue
		}
		drawn = append(drawn, row)
		if row.Stratum != stratumVetoBoundary {
			t.Errorf("%s: drawn in stratum %q, want %q", row.URL, row.Stratum, stratumVetoBoundary)
		}
		if row.Weight != boundaryCensusWeight {
			t.Errorf("%s: weight %g, want the census weight %g", row.URL, row.Weight, boundaryCensusWeight)
		}
		if row.Label != "" {
			t.Errorf("%s: a fresh draw arrived labelled %q", row.URL, row.Label)
		}
	}
	want := []string{"https://acme.test/jobs/quiet-role", "https://acme.test/jobs/silent-role"}
	if got := rowURLs(drawn); !equalStrings(got, want) {
		t.Errorf("drew %v, want %v -- the live-abstain page belongs in the drawing: ADR-0049 forbids "+
			"grading the Learned Veto against the extractor's own verdict, so the drop set may not be filtered by it", got, want)
	}
	if !weightsBalanced(drawn, 1e-9) {
		t.Errorf("the drawn rows' weights sum to %.9f over %d rows, want equal", weightSum(drawn), len(drawn))
	}
}

// TestVetoDepthIsMeasuredOverTheWholeFrameNotTheUndrawnRemainder holds the one
// ordering decision the whole reading rests on. The exclusion of already-committed
// URLs applies to the DRAW -- a page cannot carry two drawings' weights -- and must
// not apply to the depth, or the pre-registered go/no-go becomes a number about how
// much of this frame an earlier drawing happened to sample.
func TestVetoDepthIsMeasuredOverTheWholeFrameNotTheUndrawnRemainder(t *testing.T) {
	capture := vetoCapture(t)
	dir := boundarySubstrate(t, "https://acme.test/jobs/quiet-role")

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoDrawArgs(t, capture, dir, true), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
	}

	// The computation, read straight off the replay the drawing ran.
	outcome := replayVetoCapture(t, capture, learnedVetoBoundary)
	if outcome.BaselineAccepts != 3 || outcome.Drop() != 2 {
		t.Errorf("gate extracts %d, drop set %d; want 3 and 2 over the WHOLE frame, with the committed page still counted",
			outcome.BaselineAccepts, outcome.Drop())
	}

	// And the account of it, so a reader of the run sees the same numbers.
	report := buf.String()
	for _, want := range []string{
		"gate extracts        3 of 4 framed",
		"drop set             2 (live accept 1 / live abstain 1; this drawing takes both halves)",
		"depth                0.6667",
		"dropped committed    1",
		"drawn rows           1",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}

	merged, err := readGoldSet(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	if len(merged) != 2 {
		t.Fatalf("substrate has %d rows, want 2 (1 existing + 1 drawn); the committed page must be excluded from the DRAW", len(merged))
	}
}

// TestVetoBoundaryReportsWithoutWriting holds the default's whole point: the depth is
// ADR-0049's pre-registered go/no-go, so reading it must never commit the rows it
// implies -- those change the fitted weights and each owes a human confirmation, on a
// rollout the number may yet say no to.
func TestVetoBoundaryReportsWithoutWriting(t *testing.T) {
	capture := vetoCapture(t)

	t.Run("it needs no substrate at all", func(t *testing.T) {
		var buf bytes.Buffer
		absent := filepath.Join(t.TempDir(), "no-gold-set-here")
		if code := runBoundaryDrawing(vetoDrawArgs(t, capture, absent, false), &buf); code != 0 {
			t.Fatalf("exit code %d, want 0 with no -dir present\n%s", code, buf.String())
		}
		if _, err := os.Stat(absent); !os.IsNotExist(err) {
			t.Errorf("the report-only run touched %s", absent)
		}
		for _, want := range []string{"depth                0.6667", "wrote                nothing. Re-run with -draw to append the 2-row census."} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("the report does not say %q:\n%s", want, buf.String())
			}
		}
	})

	t.Run("it leaves an existing substrate byte-identical", func(t *testing.T) {
		dir := boundarySubstrate(t, "https://acme.test/jobs/already")
		before := readFileOrFail(t, filepath.Join(dir, goldSetFile))
		var buf bytes.Buffer
		if code := runBoundaryDrawing(vetoDrawArgs(t, capture, dir, false), &buf); code != 0 {
			t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
		}
		if after := readFileOrFail(t, filepath.Join(dir, goldSetFile)); before != after {
			t.Error("the report-only run rewrote the substrate")
		}
	})

	t.Run("an empty drop set is a measurement, not a wiring error", func(t *testing.T) {
		// A frame of nothing but strong pages: the veto would withhold no call at all.
		high := writeCapture(t, requireVetoKeeps(t, "https://acme.test/jobs/senior-go-engineer", true,
			"2026-08-08T10:00:00Z", richPostingTitle, richPostingBody))
		dir := boundarySubstrate(t, "https://acme.test/jobs/already")

		var buf bytes.Buffer
		if code := runBoundaryDrawing(vetoDrawArgs(t, high, dir, false), &buf); code != 0 {
			t.Errorf("exit code %d, want 0: \"the veto would drop nothing\" is a depth of 0, not a broken frame\n%s", code, buf.String())
		}
		if !strings.Contains(buf.String(), "depth                0.0000") {
			t.Errorf("the report does not state a zero depth:\n%s", buf.String())
		}
		// With -draw the same frame IS an error: there is nothing to draw.
		if code := runBoundaryDrawing(vetoDrawArgs(t, high, dir, true), &buf); code != 2 {
			t.Errorf("exit code %d with -draw, want 2 (there is no drop set to append)", code)
		}
	})
}

// TestVetoBoundaryRefusesAReversal proves the subtractive-only claim is CHECKED
// rather than assumed. The inverted pair below is a real reversal over real pages --
// the gate is never faked -- and a rule that adds a call is a rule this drawing's
// one-sided definition no longer describes, so nothing may be written.
func TestVetoBoundaryRefusesAReversal(t *testing.T) {
	inverted := learnedVetoBoundary
	inverted.Verb = "goldset-sample-veto-boundary (inverted, test only)"
	inverted.Baseline, inverted.Candidate = vetoCandidateConfig, vetoBaselineConfig

	capture := vetoCapture(t)
	if got := replayVetoCapture(t, capture, inverted).Reversed; len(got) == 0 {
		t.Fatal("the inverted pair reported no reversal, so this case is no longer a reversal at all")
	}

	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	before := readFileOrFail(t, filepath.Join(dir, goldSetFile))
	args := vetoDrawArgs(t, capture, dir, true)
	args.Design = inverted
	if code := runBoundaryDrawing(args, &bytes.Buffer{}); code != 2 {
		t.Errorf("exit code %d, want 2 on a reversal", code)
	}
	if after := readFileOrFail(t, filepath.Join(dir, goldSetFile)); before != after {
		t.Error("the verb rewrote the substrate despite refusing the draw")
	}
}

// TestVetoBoundaryRefusesAFrameTodaysGateExtractsNothingFrom keeps a wrong -capture or
// -since from reporting itself as a measurement. A depth with no denominator is not a
// depth of zero, so both modes refuse it.
func TestVetoBoundaryRefusesAFrameTodaysGateExtractsNothingFrom(t *testing.T) {
	// Jobs-index terminals: rejected several rungs before the veto is consulted.
	capture := writeCapture(t,
		capturedPage(t, "https://acme.test/careers", true, "2026-08-08T10:00:00Z", nil, "our open roles"),
		capturedPage(t, "https://acme.test/jobs", true, "2026-08-08T10:00:01Z", nil, "all our openings"),
	)
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	before := readFileOrFail(t, filepath.Join(dir, goldSetFile))

	for _, draw := range []bool{false, true} {
		if code := runBoundaryDrawing(vetoDrawArgs(t, capture, dir, draw), &bytes.Buffer{}); code != 2 {
			t.Errorf("exit code %d with -draw=%v, want 2 (the depth has no denominator)", code, draw)
		}
	}
	if after := readFileOrFail(t, filepath.Join(dir, goldSetFile)); before != after {
		t.Error("the verb rewrote the substrate despite refusing the frame")
	}
}

// TestEachBoundaryDesignStampsItsOwnStratum holds the provenance property as a
// property. A committed row says which pair drew it through its stratum and through
// nothing else, so two designs sharing one would make their rows indistinguishable in
// the committed file -- and would silently pool a veto row into ADR-0044's boundary
// scorecard, which reads its rows off the stratum.
func TestEachBoundaryDesignStampsItsOwnStratum(t *testing.T) {
	designs := []boundaryDesign{positiveEvidenceBoundary, learnedVetoBoundary}

	verbs, strata, drawings := map[string]bool{}, map[goldStratum]bool{}, map[goldDrawing]bool{}
	for _, d := range designs {
		t.Run(string(d.Stratum), func(t *testing.T) {
			if d.Verb == "" || verbs[d.Verb] {
				t.Errorf("verb %q is empty or shared with another design", d.Verb)
			}
			verbs[d.Verb] = true
			if !d.Stratum.Valid() {
				t.Errorf("stratum %q is not one goldStratum.Valid() knows, so every verb that takes a stratum by name would refuse it", d.Stratum)
			}
			if strata[d.Stratum] {
				t.Errorf("stratum %q is already another design's; two pairs' rows would be indistinguishable in the committed file", d.Stratum)
			}
			strata[d.Stratum] = true
			if drawing := d.Stratum.Drawing(); drawing == "" || drawings[drawing] {
				t.Errorf("drawing %q is empty or shared; each census normalizes over its own draw", drawing)
			} else {
				drawings[drawing] = true
			}
			if d.Baseline == nil || d.Candidate == nil {
				t.Error("the pair is incomplete; a boundary is a disagreement between two named configs")
			}
			if d.Rule == "" || d.Reversal == "" {
				t.Error("a design must state its rule and its reversal claim: the run prints both rather than leaving them to be remembered")
			}
		})
	}
}

// TestValidateDrawnBoundaryRowsHonoursTheDesignsHalf pins the one validation check
// that differs between the two drawings. ADR-0043's takes the accept half only, so an
// abstain row in it is a corrupt draw; ADR-0049's takes both halves and must, so the
// same row is legitimate there.
func TestValidateDrawnBoundaryRowsHonoursTheDesignsHalf(t *testing.T) {
	tests := []struct {
		name       string
		design     boundaryDesign
		abstainOK  bool
		wrongLabel goldStratum
	}{
		{"positive evidence boundary", positiveEvidenceBoundary, false, stratumVetoBoundary},
		{"learned veto boundary", learnedVetoBoundary, true, stratumBoundary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			abstained := []goldRow{{URL: "https://acme.test/jobs/silent-role", Verdict: false, Stratum: tt.design.Stratum, Weight: boundaryCensusWeight}}
			err := validateDrawnBoundaryRows(tt.design, abstained, map[string]struct{}{})
			if tt.abstainOK && err != nil {
				t.Errorf("refused a live-abstain row: %v -- ADR-0049 forbids grading this rung against the extractor's verdict, so the drop set may not be filtered by it", err)
			}
			if !tt.abstainOK && err == nil {
				t.Error("accepted a live-abstain row into a drawing that takes the accept half only")
			}

			foreign := []goldRow{{URL: "https://acme.test/jobs/quiet-role", Verdict: true, Stratum: tt.wrongLabel, Weight: boundaryCensusWeight}}
			if err := validateDrawnBoundaryRows(tt.design, foreign, map[string]struct{}{}); err == nil {
				t.Errorf("accepted a row stamped %q, which claims the other design's boundary", tt.wrongLabel)
			}
		})
	}
}

// TestVetoFloorIsReportedAndNeverFatal holds train-scorer's precedent for the
// pre-registered condition: it governs the FLIP, not the drawing. A frame below the
// floor still draws, because the confirmed rows outlive the decision the depth
// informs -- and the report says NOT MET so nobody has to remember the number.
func TestVetoFloorIsReportedAndNeverFatal(t *testing.T) {
	lines := []string{requireVetoDrops(t, "https://acme.test/jobs/quiet-role", true, "2026-08-08T10:00:00Z", "", "")}
	for i := range 11 {
		url := fmt.Sprintf("https://acme.test/jobs/senior-go-engineer-%d", i)
		lines = append(lines, requireVetoKeeps(t, url, true, "2026-08-08T10:00:00Z", richPostingTitle, richPostingBody))
	}
	capture := writeCapture(t, lines...)
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoDrawArgs(t, capture, dir, true), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0: the floor is reported and never moves an exit code\n%s", code, buf.String())
	}
	if !strings.Contains(buf.String(), "FLOOR NOT MET") {
		t.Errorf("a depth of 1/12 is below ADR-0049's %.2f floor and the report does not say so:\n%s", vetoFloor, buf.String())
	}
	merged, err := readGoldSet(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	if len(merged) != 2 {
		t.Errorf("substrate has %d rows, want 2 (1 existing + the 1 page below the cut)", len(merged))
	}
}

// TestCommittedVetoBoundaryStratumIsTheDropSet is this drawing's guard on the
// committed file. It is vacuous today -- the stratum ships defined and undrawn -- and
// load-bearing the day the rollout draws it: every row must be a page today's
// shipping gate EXTRACTS, which is a statement about the rungs ABOVE the veto, and a
// moved reject rung really does invalidate the draw.
//
// It deliberately does NOT assert that the veto still drops the row. A refit is
// allowed to move the cut -- #257 moved ADR-0044's, and TestCommittedBoundaryRecoveryLedger
// is the precedent for recording that rather than re-drawing.
func TestCommittedVetoBoundaryStratumIsTheDropSet(t *testing.T) {
	baseline := vetoBaselineConfig()
	rows := 0
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum != stratumVetoBoundary {
			continue
		}
		rows++
		u, err := crawler.NewURL(row.URL)
		if err != nil {
			t.Fatalf("%s: %v", row.URL, err)
		}
		if !pagegate.ShouldExtract(u, &row.Content, baseline) {
			t.Errorf("%s: today's gate SKIPS it, so it is not on the Learned Veto's boundary. The stratum was "+
				"computed under the reject rungs and the Positive Evidence rung as they stood when it was drawn; one has "+
				"changed, so the stratum no longer marks the boundary and must be re-drawn from a fresh capture window.", row.URL)
		}
	}
	if rows != vetoBoundaryStratumRows {
		t.Errorf("the veto-boundary stratum has %d rows, want %d", rows, vetoBoundaryStratumRows)
	}
}

// rowURLs projects rows to their URLs for a readable failure message.
func rowURLs(rows []goldRow) []string {
	out := []string{}
	for _, row := range rows {
		out = append(out, row.URL)
	}
	return out
}

// readFileOrFail reads a file whole, so a test can compare a substrate byte for byte
// before and after a run that must not have touched it.
func readFileOrFail(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
