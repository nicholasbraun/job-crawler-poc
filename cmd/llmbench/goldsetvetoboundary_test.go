package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// This file is the Learned Veto boundary drawing's table (ADR-0049, #304). Every
// fixture in it is threshold-sensitive: the cases below only mean what they say while
// the committed weights rank them where they do, so each is built through a
// precondition guard that names the repair when a refit moves one. This is the FOURTH
// site holding fixtures of that kind, beside internal/pagegate/learned_veto_test.go,
// internal/processor/url_processor/url_processor_test.go and
// internal/collection/refetch_test.go; the repository README's rollout section lists all
// four.

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
	// nearPostingBody carries two posting sections and an apply affordance -- enough,
	// under richPostingTitle, to lift a posting-shaped page well into the NEAR band
	// without clearing the cut. The empty-content pages the file already uses score the
	// bare fitted intercept and are its DEEP fixtures.
	nearPostingBody = "Ihre Aufgaben. Jetzt bewerben."
	// unevidencedPostingBody is three posting sections with no apply affordance and no
	// role designation, which the Positive Evidence rung sheds while the Posting Score
	// ranks the page ABOVE VetoThreshold. It is the only shape that can put a page the
	// veto KEEPS inside a drop set, which is what the banding's own refusal needs.
	unevidencedPostingBody = "Ihre Aufgaben. Ihr Profil. Wir bieten."
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

// requireVetoDropsNear and requireVetoDropsDeep are requireVetoDrops plus the band the
// case needs the page to land in. They exist because a band is decided on the Posting
// Score, so a retrain can move a fixture from one sampled band to the other while every
// veto assertion about it still holds -- and the case would then quietly stop testing
// what it says it tests.
func requireVetoDropsNear(t *testing.T, url string, verdict bool, ts, title, mainContent string, nearBand float64) string {
	t.Helper()
	line := requireVetoDrops(t, url, verdict, ts, title, mainContent)
	requireBand(t, url, title, mainContent, nearBand, bandNear)
	return line
}

func requireVetoDropsDeep(t *testing.T, url string, verdict bool, ts, title, mainContent string, nearBand float64) string {
	t.Helper()
	line := requireVetoDrops(t, url, verdict, ts, title, mainContent)
	requireBand(t, url, title, mainContent, nearBand, bandDeep)
	return line
}

// requireBand asserts which sampled band a nearBand-wide plan puts the page in. The
// message names the repair a refit owes, exactly as the two veto guards do.
func requireBand(t *testing.T, url, title, mainContent string, nearBand float64, want goldBand) {
	t.Helper()
	u, content := requireGateExtracts(t, url, title, mainContent)
	score := pagegate.Score(u, content)
	if got := bandOf(score, pagegate.VetoThreshold, nearBand); got != want {
		t.Fatalf("%s scores %.6f, which a %g-wide near band puts in the %q band, not %q. "+
			"A retrain moved the weights -- pick a different fixture; never move the threshold or the band to fit a test.",
			url, score, nearBand, got, want)
	}
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

// The URLs vetoBandsCapture puts in each band, named so a case can assert exactly which
// pages a quota took and which it left. The accepted band is the LIVE VERDICT's, not the
// score's, which is why one of its three pages scores in the near band and two at the
// bottom: banding by verdict rather than by score is the design, and a case has to be
// able to see it.
var (
	vetoAcceptedURLs = []string{
		"https://acme.test/jobs/accepted-near-role",
		"https://acme.test/jobs/accepted-quiet-role",
		"https://acme.test/jobs/accepted-silent-role",
	}
	vetoNearURLs = []string{
		"https://acme.test/jobs/near-role-1", "https://acme.test/jobs/near-role-2",
		"https://acme.test/jobs/near-role-3", "https://acme.test/jobs/near-role-4",
		"https://acme.test/jobs/near-role-5", "https://acme.test/jobs/near-role-6",
	}
	vetoDeepURLs = []string{
		"https://acme.test/jobs/deep-role-1", "https://acme.test/jobs/deep-role-2",
		"https://acme.test/jobs/deep-role-3", "https://acme.test/jobs/deep-role-4",
		"https://acme.test/jobs/deep-role-5", "https://acme.test/jobs/deep-role-6",
		"https://acme.test/jobs/deep-role-7", "https://acme.test/jobs/deep-role-8",
	}
)

// vetoBandsCapture is the frame the STRATIFIED drawing is read over: the same two
// out-of-drop-set pages vetoCapture carries -- one the veto keeps, one an index terminal
// today's gate rejects -- plus a drop set of 3 live-accept, 6 near-band live-abstain and
// 8 deep-band live-abstain pages. The two sampled bands are deliberately larger than the
// quotas the cases pass, because a quota that does not BIND proves nothing about a
// sample.
func vetoBandsCapture(t *testing.T) string {
	t.Helper()
	lines := []string{
		requireVetoKeeps(t, "https://acme.test/jobs/senior-go-engineer", true, "2026-08-08T10:00:00Z", richPostingTitle, richPostingBody),
		capturedPage(t, "https://acme.test/careers", true, "2026-08-08T10:00:01Z", nil, "our open roles"),
		// The census cell: the live extractor read each of these as one posting.
		requireVetoDropsNear(t, vetoAcceptedURLs[0], true, "2026-08-08T10:00:02Z", richPostingTitle, nearPostingBody, defaultVetoNearBand),
		requireVetoDropsDeep(t, vetoAcceptedURLs[1], true, "2026-08-08T10:00:02Z", "", "", defaultVetoNearBand),
		requireVetoDropsDeep(t, vetoAcceptedURLs[2], true, "2026-08-08T10:00:02Z", "", "", defaultVetoNearBand),
	}
	for _, u := range vetoNearURLs {
		lines = append(lines, requireVetoDropsNear(t, u, false, "2026-08-08T10:00:03Z", richPostingTitle, nearPostingBody, defaultVetoNearBand))
	}
	for _, u := range vetoDeepURLs {
		lines = append(lines, requireVetoDropsDeep(t, u, false, "2026-08-08T10:00:04Z", "", "", defaultVetoNearBand))
	}
	return writeCapture(t, lines...)
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

// defaultVetoPlan is the sampling plan runGoldSetSampleVetoBoundary's own flag defaults
// build, so a case that does not care about quotas still runs the SHIPPED design rather
// than a plan invented in a test.
func defaultVetoPlan() vetoBandPlan {
	return vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: defaultVetoNearRows, DeepRows: defaultVetoDeepRows}
}

// vetoDrawArgs is the drawing's arguments for one test capture and substrate, at the
// verb's own defaults.
func vetoDrawArgs(t *testing.T, capture, dir string, draw bool) boundaryDrawArgs {
	t.Helper()
	return vetoPlanDrawArgs(t, capture, dir, draw, defaultVetoPlan())
}

// vetoPlanDrawArgs is vetoDrawArgs with the sampling plan named, for the cases whose
// whole subject is a quota that binds.
func vetoPlanDrawArgs(t *testing.T, capture, dir string, draw bool, plan vetoBandPlan) boundaryDrawArgs {
	t.Helper()
	return boundaryDrawArgs{
		Design:  learnedVetoBoundary,
		Capture: capture,
		Dir:     dir,
		Since:   mustParseVetoTime(t, vetoWindowStart),
		Plan:    plan,
		Seed:    defaultVetoBoundarySeed,
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
		if got := len(outcome.DropSet(learnedVetoBoundary)); got != 2 {
			t.Errorf("drop set holds %d pages, want both halves (2); ADR-0049 forbids filtering it by the extractor's own verdict", got)
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
// It is also the regression test for the per-band cell keying: a selection missing a
// cell for a band it drew would write those rows at weight 0 and unbalance the drawing.
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
		// The weight is an inverse SELECTION probability, not a census weight: here the
		// quotas exceed both band populations, so every probability is 1 and the two
		// coincide -- what the drawing must never write is a zero.
		if row.Weight <= 0 {
			t.Errorf("%s: weight %g, want a positive inverse selection probability", row.URL, row.Weight)
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
		for _, want := range []string{"depth                0.6667", "wrote                nothing. Re-run with -draw to sample 2 rows from the 2-page drop set."} {
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
//
// It asserts the SELECTION design too, and that is the second half of the same property
// (ADR-0049): because a stratum is claimed by exactly one design, a row's stratum also
// states the inclusion probabilities it was drawn under. Pooling a census and a sample
// inside one stratum is what makes a weighted estimate over it unrecoverable, and a
// design that declared no selection design would let that happen unnoticed.
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
			if d.Selection != selectionCensus && d.Selection != selectionStratified {
				t.Errorf("selection design %q is not one of %q / %q; a committed row states its inclusion probabilities through its stratum, "+
					"and a design that declares none leaves them unstated", d.Selection, selectionCensus, selectionStratified)
			}
		})
	}
}

// TestValidateDrawnBoundaryRowsHonoursTheDesignsSelection pins the two checks that
// differ between the drawings. ADR-0043's takes the accept half only, so an abstain row
// in it is a corrupt draw; ADR-0049's takes both halves and must. And a census row's
// weight is exactly 1 where a stratified row's must be one of the SAMPLED cells'
// weights -- the direct guard against a cell-keying bug, which writes a legitimate-
// looking row at somebody else's probability or at zero.
func TestValidateDrawnBoundaryRowsHonoursTheDesignsSelection(t *testing.T) {
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
			// One cell per verdict half, fully taken -- the one shape in which a census
			// weight and an inverse selection probability coincide, so both designs read
			// these rows as legitimate and the subject stays the verdict and the stratum.
			// The weights the two designs do NOT share are the sub-cases below.
			sel := selection{Cells: []cellResult{
				{Key: cellKey{Stratum: tt.design.Stratum, Verdict: true}, Population: 3, Sampled: 3, Weight: boundaryCensusWeight},
				{Key: cellKey{Stratum: tt.design.Stratum, Verdict: false}, Population: 3, Sampled: 3, Weight: boundaryCensusWeight},
			}}
			abstained := []goldRow{{URL: "https://acme.test/jobs/silent-role", Verdict: false, Stratum: tt.design.Stratum, Weight: boundaryCensusWeight}}
			err := validateDrawnBoundaryRows(tt.design, sel, abstained, map[string]struct{}{})
			if tt.abstainOK && err != nil {
				t.Errorf("refused a live-abstain row: %v -- ADR-0049 forbids grading this rung against the extractor's verdict, so the drop set may not be filtered by it", err)
			}
			if !tt.abstainOK && err == nil {
				t.Error("accepted a live-abstain row into a drawing that takes the accept half only")
			}

			foreign := []goldRow{{URL: "https://acme.test/jobs/quiet-role", Verdict: true, Stratum: tt.wrongLabel, Weight: boundaryCensusWeight}}
			if err := validateDrawnBoundaryRows(tt.design, sel, foreign, map[string]struct{}{}); err == nil {
				t.Errorf("accepted a row stamped %q, which claims the other design's boundary", tt.wrongLabel)
			}
		})
	}

	t.Run("a census design refuses any weight but 1", func(t *testing.T) {
		sel := selection{Cells: []cellResult{{Key: cellKey{Stratum: stratumBoundary, Verdict: true}, Population: 3, Sampled: 3, Weight: boundaryCensusWeight}}}
		row := []goldRow{{URL: "https://acme.test/jobs/one", Verdict: true, Stratum: stratumBoundary, Weight: 1.5}}
		if err := validateDrawnBoundaryRows(positiveEvidenceBoundary, sel, row, map[string]struct{}{}); err == nil {
			t.Error("accepted a sampled weight into a census; a census row's inclusion probability is 1 by definition")
		}
	})

	t.Run("a stratified design refuses a weight no sampled cell has", func(t *testing.T) {
		sel := selection{Cells: []cellResult{
			{Key: cellKey{Stratum: stratumVetoBoundary, Verdict: true, Band: bandAccepted}, Population: 2, Sampled: 2, Weight: 0.5},
			{Key: cellKey{Stratum: stratumVetoBoundary, Verdict: false, Band: bandNear}, Population: 6, Sampled: 2, Weight: 1.5},
			// An EMPTY band still emits a cell, at weight 0. Its weight must never
			// license a row: a zero-weight row is exactly what a mis-keyed cell writes.
			{Key: cellKey{Stratum: stratumVetoBoundary, Verdict: false, Band: bandDeep}, Population: 0, Sampled: 0, Weight: 0},
		}}
		for _, tc := range []struct {
			name   string
			weight float64
		}{
			{"a weight from no cell at all", 0.9},
			{"the empty band's zero", 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				row := []goldRow{{URL: "https://acme.test/jobs/one", Verdict: false, Stratum: stratumVetoBoundary, Weight: tc.weight}}
				if err := validateDrawnBoundaryRows(learnedVetoBoundary, sel, row, map[string]struct{}{}); err == nil {
					t.Errorf("accepted weight %g; the row's cell was keyed wrong and the drawing would not normalize", tc.weight)
				}
			})
		}
	})

	t.Run("a stratified draw whose weights do not normalize is refused", func(t *testing.T) {
		sel := selection{Cells: []cellResult{
			{Key: cellKey{Stratum: stratumVetoBoundary, Verdict: true, Band: bandAccepted}, Population: 2, Sampled: 2, Weight: 0.5},
		}}
		// Two rows at weight 0.5 sum to 1, not 2: legitimate per row, wrong as a drawing.
		rows := []goldRow{
			{URL: "https://acme.test/jobs/one", Verdict: true, Stratum: stratumVetoBoundary, Weight: 0.5},
			{URL: "https://acme.test/jobs/two", Verdict: true, Stratum: stratumVetoBoundary, Weight: 0.5},
		}
		if err := validateDrawnBoundaryRows(learnedVetoBoundary, sel, rows, map[string]struct{}{}); err == nil {
			t.Error("accepted a draw whose weights sum to half its row count; a drawing's weights normalize to its own rows")
		}
	})
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
//
// It also holds the WEIGHT STRUCTURE this drawing's design implies, on the model of
// TestCommittedRandomStratumIsWeightedToTheStream: three sampling cells, so at most
// three distinct weights, one of them shared by every live-accept row (the censused
// band) and at most two across the live-abstain ones -- and the whole stratum summing
// to its own row count, because a drawing's weights normalize within the drawing.
func TestCommittedVetoBoundaryStratumIsTheDropSet(t *testing.T) {
	baseline := vetoBaselineConfig()
	drawn := []goldRow{}
	for _, row := range loadCommittedGoldSet(t) {
		if row.Stratum != stratumVetoBoundary {
			continue
		}
		drawn = append(drawn, row)
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
	if len(drawn) != vetoBoundaryStratumRows {
		t.Errorf("the veto-boundary stratum has %d rows, want %d", len(drawn), vetoBoundaryStratumRows)
	}
	if len(drawn) == 0 {
		return
	}

	all, accepted, abstained := map[float64]int{}, map[float64]int{}, map[float64]int{}
	for _, row := range drawn {
		all[row.Weight]++
		if row.Verdict {
			accepted[row.Weight]++
		} else {
			abstained[row.Weight]++
		}
	}
	if len(all) > len(boundaryBands) {
		t.Errorf("the veto-boundary stratum carries %d distinct weights, want at most %d (one per sampling band): %v",
			len(all), len(boundaryBands), all)
	}
	if len(accepted) > 1 {
		t.Errorf("the live-accept rows carry %d distinct weights, want 1: that band is a CENSUS, so every one of its rows "+
			"has inclusion probability 1 and therefore one weight: %v", len(accepted), accepted)
	}
	if len(abstained) > 2 {
		t.Errorf("the live-abstain rows carry %d distinct weights, want at most 2 (the near and deep bands): %v", len(abstained), abstained)
	}
	if !weightsBalanced(drawn, 1e-6) {
		t.Errorf("the veto-boundary drawing's weights sum to %.6f over %d rows, want equal", weightSum(drawn), len(drawn))
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

// TestVetoBoundaryDrawIsStratifiedNotACensus is the whole point of the change. With a
// quota below each sampled band's population, the draw takes every accepted-but-dropped
// page, exactly -near-rows of the near band and exactly -deep-rows of the deep one --
// where a census would have taken all seventeen. A census does not survive this rung:
// the drop set is most of the stream, and ADR-0043 requires a human confirmation on
// every row of it.
func TestVetoBoundaryDrawIsStratifiedNotACensus(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	capture := vetoBandsCapture(t)
	plan := vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 2, DeepRows: 3}

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoPlanDrawArgs(t, capture, dir, true, plan), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
	}

	drawn := drawnVetoRows(t, dir)
	if got := len(drawn); got != len(vetoAcceptedURLs)+plan.NearRows+plan.DeepRows {
		t.Fatalf("drew %d rows, want %d (%d accepted + %d near + %d deep); a census would have drawn %d",
			got, len(vetoAcceptedURLs)+plan.NearRows+plan.DeepRows, len(vetoAcceptedURLs), plan.NearRows, plan.DeepRows,
			len(vetoAcceptedURLs)+len(vetoNearURLs)+len(vetoDeepURLs))
	}
	for _, band := range []struct {
		name string
		urls []string
		want int
	}{
		{"accepted", vetoAcceptedURLs, len(vetoAcceptedURLs)},
		{"near", vetoNearURLs, plan.NearRows},
		{"deep", vetoDeepURLs, plan.DeepRows},
	} {
		if got := countDrawn(drawn, band.urls); got != band.want {
			t.Errorf("the %s band contributed %d of its %d pages, want %d", band.name, got, len(band.urls), band.want)
		}
	}
}

// TestVetoBoundaryDrawsEveryAcceptedButDroppedPage holds the one band that is NOT
// subject to a quota. The accepted band is the candidate false-drops, the recall claim
// rests on exactly those pages, and sampling them would put sampling error on the number
// the rollout turns on -- so the tightest quotas the two sampled bands accept must still
// leave it whole.
func TestVetoBoundaryDrawsEveryAcceptedButDroppedPage(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	capture := vetoBandsCapture(t)

	var buf bytes.Buffer
	plan := vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 1, DeepRows: 1}
	if code := runBoundaryDrawing(vetoPlanDrawArgs(t, capture, dir, true, plan), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
	}

	drawn := drawnVetoRows(t, dir)
	if got := countDrawn(drawn, vetoAcceptedURLs); got != len(vetoAcceptedURLs) {
		t.Errorf("drew %d of the %d accepted-but-dropped pages, want all of them: that band is a census, "+
			"because the recall claim rests on exactly these rows", got, len(vetoAcceptedURLs))
	}
	if got := len(drawn); got != len(vetoAcceptedURLs)+2 {
		t.Errorf("drew %d rows, want %d (the whole accepted band plus one row per sampled band)", got, len(vetoAcceptedURLs)+2)
	}
}

// TestVetoBoundaryWeightsInvertTheSelectionProbability is the crux of the design. A
// sampled row weighted 1 and pooled with a censused one makes any weighted read over the
// stratum describe the enriched sample while claiming to describe the drop set, so each
// band's rows carry N_c/n_c, normalized to the drawn row count.
//
// The RATIOS are asserted rather than the floats: a fixture count changing must move
// this test's arithmetic, not falsify its claim.
func TestVetoBoundaryWeightsInvertTheSelectionProbability(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	capture := vetoBandsCapture(t)
	plan := vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 2, DeepRows: 4}

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoPlanDrawArgs(t, capture, dir, true, plan), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
	}
	drawn := drawnVetoRows(t, dir)

	weights := map[string]float64{}
	for _, row := range drawn {
		band := "deep"
		switch {
		case contains(vetoAcceptedURLs, row.URL):
			band = "accepted"
		case contains(vetoNearURLs, row.URL):
			band = "near"
		}
		if seen, ok := weights[band]; ok && seen != row.Weight {
			t.Errorf("the %s band carries two weights, %g and %g; one cell means one inclusion probability", band, seen, row.Weight)
		}
		weights[band] = row.Weight
	}

	// N_c/n_c: 3 of 3 censused, 2 of 6 near, 4 of 8 deep -- so near rows stand for three
	// pages each and deep rows for two, against the census cell's one.
	for _, want := range []struct {
		band  string
		ratio float64
	}{{"near", 3}, {"deep", 2}} {
		got := weights[want.band] / weights["accepted"]
		if math.Abs(got-want.ratio) > 1e-9 {
			t.Errorf("the %s band's weight is %.6f times the censused band's, want %g -- the inverse of how thinly it was sampled",
				want.band, got, want.ratio)
		}
	}
	if !weightsBalanced(drawn, 1e-9) {
		t.Errorf("the drawn rows' weights sum to %.9f over %d rows, want equal", weightSum(drawn), len(drawn))
	}
	if !strings.Contains(buf.String(), "drawn weight sum") || !strings.Contains(buf.String(), "inverse selection probability") {
		t.Errorf("the report does not say what the weights ARE:\n%s", buf.String())
	}
}

// TestVetoBoundaryDrawIsDeterministic holds what a committed artifact rests on: the same
// capture and seed always produce the same file, byte for byte, while a new seed is a
// deliberate, genuinely different resample of the SAMPLED bands only -- the censused one
// has nothing to resample.
func TestVetoBoundaryDrawIsDeterministic(t *testing.T) {
	capture := vetoBandsCapture(t)
	plan := vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 2, DeepRows: 3}

	draw := func(t *testing.T, seed string) (string, []goldRow) {
		t.Helper()
		dir := boundarySubstrate(t, "https://acme.test/jobs/already")
		args := vetoPlanDrawArgs(t, capture, dir, true, plan)
		args.Seed = seed
		var buf bytes.Buffer
		if code := runBoundaryDrawing(args, &buf); code != 0 {
			t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
		}
		return readFileOrFail(t, filepath.Join(dir, goldSetFile)), drawnVetoRows(t, dir)
	}

	first, firstRows := draw(t, defaultVetoBoundarySeed)
	again, _ := draw(t, defaultVetoBoundarySeed)
	if first != again {
		t.Error("the same capture and seed produced two different substrates; a drawing's provenance rests on it being reproducible")
	}

	_, resampled := draw(t, defaultVetoBoundarySeed+"-resample")
	if countDrawn(resampled, vetoAcceptedURLs) != len(vetoAcceptedURLs) {
		t.Error("a new seed changed the CENSUSED band; there is nothing there to resample")
	}
	if equalStrings(rowURLs(firstRows), rowURLs(resampled)) {
		t.Error("a new seed selected exactly the same rows, so it is not keying the within-band selection at all")
	}
}

// TestVetoBoundaryRefusesASecondDrawIntoTheStratum holds the rule that keeps the stratum
// readable. A census tolerates a repeat -- a page taken under it had inclusion
// probability 1 whenever it was taken -- but a second window's SAMPLE would carry a
// second set of inclusion probabilities into one stratum, and pooling two selection
// designs there is what makes a weighted estimate over it unrecoverable.
func TestVetoBoundaryRefusesASecondDrawIntoTheStratum(t *testing.T) {
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	capture := vetoBandsCapture(t)

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoDrawArgs(t, capture, dir, true), &buf); code != 0 {
		t.Fatalf("the first draw exited %d, want 0\n%s", code, buf.String())
	}
	before := readFileOrFail(t, filepath.Join(dir, goldSetFile))

	// A SECOND window, holding pages the first one never saw, so the refusal can only be
	// the design's: there is plenty left to draw and the verb refuses anyway.
	second := writeCapture(t,
		requireVetoDropsNear(t, "https://acme.test/jobs/second-window-near", true, "2026-08-08T11:00:00Z", richPostingTitle, nearPostingBody, defaultVetoNearBand),
		requireVetoDropsDeep(t, "https://acme.test/jobs/second-window-deep", false, "2026-08-08T11:00:01Z", "", "", defaultVetoNearBand),
	)
	buf.Reset()
	if code := runBoundaryDrawing(vetoDrawArgs(t, second, dir, true), &buf); code != 2 {
		t.Errorf("exit code %d, want 2 on a second draw into a sampled stratum", code)
	}
	if after := readFileOrFail(t, filepath.Join(dir, goldSetFile)); before != after {
		t.Error("the refused second draw rewrote the substrate")
	}
}

// TestVetoBoundarySamplesThePopulationTheSubstrateHasNotAlreadyDrawn pins the ordering
// the weights rest on. A page an earlier drawing committed leaves the SAMPLING
// POPULATION before the quota is applied -- that is what makes each band's inclusion
// probability correct -- while still counting toward the depth, which is ADR-0049's
// pre-registered go/no-go over the whole frame.
func TestVetoBoundarySamplesThePopulationTheSubstrateHasNotAlreadyDrawn(t *testing.T) {
	dir := boundarySubstrate(t, vetoDeepURLs[0])
	capture := vetoBandsCapture(t)
	// Take both sampled bands whole, so the only thing that can shrink the deep band is
	// the exclusion.
	plan := vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 0, DeepRows: 0}

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoPlanDrawArgs(t, capture, dir, true, plan), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0\n%s", code, buf.String())
	}

	drawn := drawnVetoRows(t, dir)
	if countDrawn(drawn, vetoDeepURLs[:1]) != 0 {
		t.Errorf("%s was drawn although the substrate already carries it; one page cannot carry two drawings' weights", vetoDeepURLs[0])
	}
	if got := countDrawn(drawn, vetoDeepURLs); got != len(vetoDeepURLs)-1 {
		t.Errorf("the deep band contributed %d rows, want %d: the committed page leaves the POPULATION, the rest stay", got, len(vetoDeepURLs)-1)
	}

	report := buf.String()
	for _, want := range []string{
		// The drop set and the depth are over the WHOLE frame, committed page included.
		"drop set             17 (live accept 3 / live abstain 14; this drawing takes both halves)",
		"dropped committed    1",
		"cell deep             verdict=false population     7  sampled   7",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the report does not say %q:\n%s", want, report)
		}
	}
}

// TestVetoBoundaryReportsThePlanBeforeDrawing holds what the non-destructive default is
// FOR, now that the drawing has a plan: the operator reads each band's population and
// the rows the current quotas would take, and tunes the flags before a single row is
// committed. Nothing is written and the gold set is not even opened, so the populations
// it prints are pre-exclusion and the report says so.
func TestVetoBoundaryReportsThePlanBeforeDrawing(t *testing.T) {
	capture := vetoBandsCapture(t)
	absent := filepath.Join(t.TempDir(), "no-gold-set-here")
	plan := vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 2, DeepRows: 3}

	var buf bytes.Buffer
	if code := runBoundaryDrawing(vetoPlanDrawArgs(t, capture, absent, false, plan), &buf); code != 0 {
		t.Fatalf("exit code %d, want 0 with no -dir present\n%s", code, buf.String())
	}
	if _, err := os.Stat(absent); !os.IsNotExist(err) {
		t.Errorf("the report-only run touched %s", absent)
	}
	report := buf.String()
	for _, want := range []string{
		"band accepted         population     3  quota census would draw     3",
		"band near             population     6  quota 2      would draw     2",
		"band deep             population     8  quota 3      would draw     3",
		fmt.Sprintf("score in [%.6f, %.6f)", pagegate.VetoThreshold-plan.NearBand, pagegate.VetoThreshold),
		"these populations are PRE-exclusion",
		"wrote                nothing. Re-run with -draw to sample 8 rows from the 17-page drop set.",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the plan preview does not say %q:\n%s", want, report)
		}
	}
}

// TestVetoBoundaryRefusesAnUnusableBand keeps a plan that cannot mean what it says from
// reaching the capture at all. A band edge outside the score's own range leaves one of
// the three cells empty by construction, and a negative quota would slip through
// takeByHash's "n <= 0 takes everything" convention as a census.
func TestVetoBoundaryRefusesAnUnusableBand(t *testing.T) {
	capture := vetoBandsCapture(t)
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	before := readFileOrFail(t, filepath.Join(dir, goldSetFile))

	for _, tt := range []struct {
		name string
		plan vetoBandPlan
	}{
		{"a zero band", vetoBandPlan{NearBand: 0, NearRows: 2, DeepRows: 2}},
		{"a negative band", vetoBandPlan{NearBand: -0.1, NearRows: 2, DeepRows: 2}},
		{"a band wider than the threshold", vetoBandPlan{NearBand: pagegate.VetoThreshold, NearRows: 2, DeepRows: 2}},
		{"a negative near quota", vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: -1, DeepRows: 2}},
		{"a negative deep quota", vetoBandPlan{NearBand: defaultVetoNearBand, NearRows: 2, DeepRows: -1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, draw := range []bool{false, true} {
				if code := runBoundaryDrawing(vetoPlanDrawArgs(t, capture, dir, draw, tt.plan), &bytes.Buffer{}); code != 2 {
					t.Errorf("exit code %d with -draw=%v, want 2", code, draw)
				}
			}
			if after := readFileOrFail(t, filepath.Join(dir, goldSetFile)); before != after {
				t.Error("the verb rewrote the substrate despite refusing the plan")
			}
		})
	}
}

// TestVetoBoundaryBandsSplitOnTheScore pins the partition itself: the accepted band is
// the LIVE VERDICT's and never the score's -- one of its pages scores in the near band
// and it still belongs to the census -- while the two sampled bands split the abstain
// half at VetoThreshold - NearBand.
func TestVetoBoundaryBandsSplitOnTheScore(t *testing.T) {
	outcome := replayVetoCapture(t, vetoBandsCapture(t), learnedVetoBoundary)
	bands, err := bandDropSet(outcome.DropSet(learnedVetoBoundary), defaultVetoPlan())
	if err != nil {
		t.Fatalf("bandDropSet: %v", err)
	}

	for _, want := range []struct {
		band goldBand
		urls []string
	}{
		{bandAccepted, vetoAcceptedURLs},
		{bandNear, vetoNearURLs},
		{bandDeep, vetoDeepURLs},
	} {
		t.Run(string(want.band), func(t *testing.T) {
			if got := urlsOf(bands[want.band]); !equalStrings(sortedCopy(got), sortedCopy(want.urls)) {
				t.Errorf("the %s band holds %v, want %v", want.band, got, want.urls)
			}
			for _, c := range bands[want.band] {
				if c.Band != want.band {
					t.Errorf("%s was stamped %q but filed under %q", c.URL, c.Band, want.band)
				}
			}
		})
	}
}

// TestVetoBoundaryRefusesADropSetPageTheVetoKeeps exercises the banding's own refusal.
// A drop set holding a page whose Posting Score is ABOVE the cut means the pair replayed
// was not the Learned Veto's, so every band the drawing stamped would be a claim about a
// cut the page is on the other side of. The design below is a real, differently-paired
// one -- the gate is never faked -- declared stratified so the refusal is reachable.
func TestVetoBoundaryRefusesADropSetPageTheVetoKeeps(t *testing.T) {
	mispaired := learnedVetoBoundary
	mispaired.Verb = "goldset-sample-veto-boundary (mispaired, test only)"
	mispaired.Baseline, mispaired.Candidate = boundaryBaselineConfig, boundaryCandidateConfig
	mispaired.Floor = 0

	// A page the blanket accept extracts, the Positive Evidence rung sheds, and the
	// Posting Score ranks ABOVE the cut: three posting sections, no apply affordance.
	url := "https://acme.test/company/we-grew"
	u, err := crawler.NewURL(url)
	if err != nil {
		t.Fatalf("url %q: %v", url, err)
	}
	content := &crawler.Content{Title: "page", MainContent: unevidencedPostingBody}
	if !pagegate.ShouldExtract(u, content, boundaryBaselineConfig()) || pagegate.ShouldExtract(u, content, boundaryCandidateConfig()) {
		t.Fatalf("%s is no longer on the Positive Evidence boundary; pick another page the blanket accept extracts and the rung sheds", url)
	}
	if got := pagegate.Score(u, content); got < pagegate.VetoThreshold {
		t.Fatalf("%s scores %.6f, below VetoThreshold %.6f: this case needs a drop-set page the VETO would keep. "+
			"A retrain moved the weights -- pick a stronger fixture; never move the threshold to fit a test.", url, got, pagegate.VetoThreshold)
	}

	capture := writeCapture(t, capturedPageTitled(t, url, true, "2026-08-08T10:00:00Z", "page", nil, unevidencedPostingBody))
	dir := boundarySubstrate(t, "https://acme.test/jobs/already")
	before := readFileOrFail(t, filepath.Join(dir, goldSetFile))

	args := vetoDrawArgs(t, capture, dir, true)
	args.Design = mispaired
	if code := runBoundaryDrawing(args, &bytes.Buffer{}); code != 2 {
		t.Errorf("exit code %d, want 2: a drop set holding a page the veto keeps is a mispaired design, not a band", code)
	}
	if after := readFileOrFail(t, filepath.Join(dir, goldSetFile)); before != after {
		t.Error("the verb rewrote the substrate despite refusing the banding")
	}
}

// drawnVetoRows reads the substrate and returns the veto-boundary rows a draw appended,
// so a case reads what was DRAWN rather than what the file happens to hold.
func drawnVetoRows(t *testing.T, dir string) []goldRow {
	t.Helper()
	merged, err := readGoldSet(filepath.Join(dir, goldSetFile))
	if err != nil {
		t.Fatalf("readGoldSet: %v", err)
	}
	drawn := []goldRow{}
	for _, row := range merged {
		if row.Stratum == stratumVetoBoundary {
			drawn = append(drawn, row)
		}
	}
	return drawn
}

// countDrawn counts how many of urls the drawn rows hold.
func countDrawn(drawn []goldRow, urls []string) int {
	n := 0
	for _, row := range drawn {
		if contains(urls, row.URL) {
			n++
		}
	}
	return n
}

// contains reports whether urls holds url.
func contains(urls []string, url string) bool {
	return slices.Contains(urls, url)
}

// sortedCopy returns a sorted copy, so two URL sets compare as sets.
func sortedCopy(in []string) []string {
	out := append([]string{}, in...)
	sort.Strings(out)
	return out
}
