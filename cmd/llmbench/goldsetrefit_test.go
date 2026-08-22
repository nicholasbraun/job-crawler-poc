package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// tinyGoldSet is a miniature Extract Gold Set: enough rows for the Positive Evidence
// rung to accept a SEPARABLE mix, spread over enough strata that all sixteen derived
// counts are non-trivially non-zero, and small enough that a whole refit costs
// milliseconds. It exists because refitting the committed set would add two more full
// fits to a package that already spends most of its runtime on two -- and because the
// SEQUENCE, not the fit, is what these tests are about.
//
// The URL shapes are the ones the rung's own table in internal/pagegate names:
//
//   - /o/<role> carrying five posting sections, an apply affordance and a role
//     designation is what the Posting Score was fitted to rank highest. (A /karriere/
//     path is not usable here -- the career-index reject rung sheds it several rungs
//     before Positive Evidence is consulted.)
//   - /jobs/<role> with EMPTY content is admitted by Positive Evidence on the URL alone
//     and emits no Score Signal at all, so it sits at the bare fitted intercept: the
//     lowest score available, and the least retrain-sensitive low fixture there is.
//     The cut therefore lands on exactly those rows, at zero detail loss, on data that
//     is separable by construction.
//
// The Stratum of each row is set AS DATA. These rows were never drawn, so no stratum
// here is a claim about a sampling design; it is what makes the counts the verb
// recomputes have something to count.
func tinyGoldSet() []goldRow {
	rows := []goldRow{}
	// Eight strong posting pages the rung accepts and the fit ranks high, spread across
	// the strata whose confirmation counts the verb tracks.
	strata := []struct {
		stratum   goldStratum
		confirmer string
	}{
		{stratumLonePosting, "a human"},
		{stratumLonePosting, "a human"},
		{stratumLonePosting, ""},
		{stratumRandom, "a human"},
		{stratumRandom, "a human"},
		{stratumBoundary, "a human"},
		{stratumBoundary, ""},
		{stratumNoPosting, "a human"},
	}
	for i, s := range strata {
		rows = append(rows, tinyRow(
			fmt.Sprintf("https://co-%d.test/o/ingenieur-%d", i+1, i+1),
			bench.ExtractDetail, s.stratum, s.confirmer,
			crawler.Content{Title: richPostingTitle, MainContent: richPostingBody},
		))
	}
	// The one row carrying an expected extraction, so pendingExpectedConfirmations has
	// something to fall from. It rides a lone-posting row, which is the only stratum
	// goldset-apply will accept an expectation on.
	rows[2].Expected = &goldExpected{
		Title: richPostingTitle, WorkArrangement: "unspecified",
		ProposedBy: "script:test", ProposedAt: "2026-08-08T10:00:00Z",
	}

	// The two weakest accepts: the rows the cut lands on.
	for i := range 2 {
		rows = append(rows, tinyRow(
			fmt.Sprintf("https://hub-%d.test/jobs/senior-engineer", i+1),
			bench.ExtractHubIndex, stratumBoundary, "", crawler.Content{},
		))
	}
	// A real Job Listing whose parsed body holds no role: the rung drops it, so it is
	// the stratum's one false-drop.
	rows = append(rows, tinyRow(
		"https://thin-1.test/o/ingenieur-thin", bench.ExtractDetail, stratumBoundary, "",
		crawler.Content{Title: "Ingenieur", MainContent: "Wir suchen."},
	))
	// A page a reading could not settle, with the tension stated -- an ambiguous row is
	// scored by nothing and counted apart from everything.
	ambiguous := tinyRow("https://amb-1.test/about", bench.ExtractAmbiguous, stratumBoundary, "",
		crawler.Content{Title: "Über uns", MainContent: "Wir sind ein Unternehmen."})
	ambiguous.LabelProvenance.Note = "reads as a company page carrying one role"
	rows = append(rows, ambiguous)
	// Two ordinary negatives, so the fit has something to push down.
	rows = append(rows,
		tinyRow("https://res-1.test/about", bench.ExtractResidue, stratumNoPosting, "a human",
			crawler.Content{Title: "Über uns", MainContent: "Wir sind ein Unternehmen."}),
		tinyRow("https://res-2.test/presse", bench.ExtractResidue, stratumRandom, "a human",
			crawler.Content{Title: "Presse", MainContent: "Unsere Pressemitteilungen."}),
	)
	// One row of the Learned Veto's own boundary drawing, so vetoBoundaryStratumRows is
	// a count with something in it.
	rows = append(rows, tinyRow("https://veto-1.test/jobs/quiet-role",
		bench.ExtractResidue, stratumVetoBoundary, "", crawler.Content{}))
	return rows
}

// tinyRow builds one fixture row with the provenance shape goldset-apply leaves alone:
// a proposer, and a Proposed Label already present on every unconfirmed labelled row so
// ADR-0048's backfill is a no-op and the "nothing here edits a label" assertion can be
// exact.
func tinyRow(url string, label bench.ExtractLabel, stratum goldStratum, confirmer string, content crawler.Content) goldRow {
	prov := goldProvenance{ProposedBy: "llm:test", ProposedAt: "2026-08-08T10:00:00Z"}
	if confirmer == "" {
		prov.ProposedLabel = label
	} else {
		prov.ConfirmedBy, prov.ConfirmedAt = confirmer, "2026-08-08T11:00:00Z"
	}
	return goldRow{
		URL: url, Verdict: label == bench.ExtractDetail, TS: "2026-08-08T10:00:00Z",
		Label: label, Stratum: stratum, Weight: boundaryCensusWeight,
		LabelProvenance: prov, Content: content,
	}
}

// tinyGoldSetDir writes rows as a complete record -- substrate and both review sheets --
// in a temp directory, which is the shape goldset-apply reads.
func tinyGoldSetDir(t *testing.T, rows []goldRow) string {
	t.Helper()
	dir := t.TempDir()
	if err := writeGoldSetFiles(dir, rows); err != nil {
		t.Fatalf("write the fixture record: %v", err)
	}
	return dir
}

// writeCountsFixture writes a counts source declaring every derived count at a
// deliberately WRONG value, so the pass has something to rewrite. The wrong value is
// chosen per direction -- a pending count starts high, a confirmed count starts at zero
// -- so the fixture exercises a rewrite rather than tripping the ratchet refusal;
// override names the counts a test wants somewhere else.
func writeCountsFixture(t *testing.T, override map[string]int) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("package p\n\n// The counts the record determines.\nconst (\n")
	for _, c := range derivedCounts {
		value := 999
		if c.Direction == countMayRise {
			value = 0
		}
		if v, ok := override[c.Name]; ok {
			value = v
		}
		fmt.Fprintf(&b, "\t// %s is %s.\n\t%s = %d\n", c.Name, c.Why, c.Name, value)
	}
	b.WriteString(")\n")

	path := filepath.Join(t.TempDir(), "counts.go")
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write the counts fixture: %v", err)
	}
	return path
}

// TestRefitVerdict is the exit-code contract, exhaustively. The verdict is a pure
// function of the trainer's report and the suite's outcome precisely so it can be, and
// so nothing about whether an artifact ships depends on the order the phases ran in.
func TestRefitVerdict(t *testing.T) {
	met := trainReport{FloorMet: true, Chosen: vetoPoint{VetoShare: 0.2825}}
	unmet := trainReport{FloorMet: false, Chosen: vetoPoint{VetoShare: 0.04}}
	lossy := trainReport{FloorMet: true, Chosen: vetoPoint{VetoShare: 0.2825, DetailLost: 3}}

	tests := []struct {
		name          string
		report        trainReport
		verified      bool
		suiteErr      error
		wantCode      int
		wantShippable bool
		wantReason    string
	}{
		{
			name: "the condition is met and the suite is green", report: met, verified: true,
			wantCode: 0, wantShippable: true,
		},
		{
			name: "the floor is not met", report: unmet, verified: true,
			wantCode: 1, wantReason: "pre-registered",
		},
		{
			name: "the cut loses a detail row the rung accepts", report: lossy, verified: true,
			wantCode: 1, wantReason: "detail row(s)",
		},
		{
			name: "the guard suite is red", report: met, verified: true, suiteErr: errors.New("exit status 1"),
			wantCode: 1, wantReason: "RESTANDING",
		},
		{
			// Nothing compiled the new weights, so the word "shippable" is withheld --
			// but the pass did not fail, and a 1 here would report a refusal that never
			// happened.
			name: "the condition is met and the suite was not run", report: met, verified: false,
			wantCode: 0, wantShippable: false,
		},
		{
			name: "an unmet floor is still unmet without the suite", report: unmet, verified: false,
			wantCode: 1, wantReason: "pre-registered",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refitVerdict(tt.report, tt.verified, tt.suiteErr)
			if got.Code != tt.wantCode {
				t.Errorf("exit code %d, want %d (reasons %v)", got.Code, tt.wantCode, got.Reasons)
			}
			if got.Shippable != tt.wantShippable {
				t.Errorf("shippable %v, want %v", got.Shippable, tt.wantShippable)
			}
			if tt.wantReason == "" {
				if len(got.Reasons) != 0 {
					t.Errorf("reasons %v, want none", got.Reasons)
				}
				return
			}
			if !strings.Contains(strings.Join(got.Reasons, "\n"), tt.wantReason) {
				t.Errorf("reasons %v name nothing about %q", got.Reasons, tt.wantReason)
			}
		})
	}
}

// TestGoldSetRefitRunsTheWholeSequence drives the verb as a human runs it and holds
// what each phase owes: the counts land at what the record determines, the weights are
// exactly the artifact the trainer alone would have produced, and not one label moved.
//
// The weights are compared against a direct trainer run over the SAME applied record
// rather than against the pre-apply fixture, because goldset-apply canonicalizes the
// file first and the question here is whether the refit's fit is the trainer's fit --
// not whether an apply is a no-op, which goldset-apply's own tests own.
func TestGoldSetRefitRunsTheWholeSequence(t *testing.T) {
	rows := tinyGoldSet()
	dir := tinyGoldSetDir(t, rows)
	counts := writeCountsFixture(t, nil)
	weights := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")

	if code := runGoldSetRefit([]string{"-dir", dir, "-counts", counts, "-weights", weights, "-verify=false"}); code != 0 {
		t.Fatalf("goldset-refit exited %d, want 0", code)
	}

	// The counts now say what the record says, and the file still parses.
	snapshot, err := snapshotRecord(dir)
	if err != nil {
		t.Fatalf("snapshot the applied record: %v", err)
	}
	_, lits, err := readCountLiterals(counts, derivedCountNames())
	if err != nil {
		t.Fatalf("re-read the rewritten counts: %v", err)
	}
	for _, c := range derivedCounts {
		if got, want := lits[c.Name].Value, c.Value(snapshot); got != want {
			t.Errorf("%s was left at %d, but the record determines %d", c.Name, got, want)
		}
	}
	// And they are not all the fixture's placeholder: a rewriter that wrote nothing
	// would otherwise pass the loop above only where the placeholder happened to be
	// right.
	if lits["boundaryStratumRows"].Value != 6 {
		t.Errorf("boundaryStratumRows = %d, want the fixture's 6 boundary rows", lits["boundaryStratumRows"].Value)
	}

	// The artifact is the trainer's own, byte for byte. The report cannot move a byte,
	// which is what lets the refit run the trainer WITH the report and still produce the
	// file `go generate` produces without it.
	direct := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")
	substrate, _ := goldSetPaths(dir)
	if code := runTrainScorer([]string{"-in", substrate, "-out", direct, "-report=false"}); code != 0 {
		t.Fatalf("the direct trainer run exited %d, want 0", code)
	}
	got, want := readFileOrFail(t, weights), readFileOrFail(t, direct)
	if got != want {
		t.Errorf("the refit's artifact (%d bytes) is not the trainer's (%d bytes); the refit must drive the SAME fit, at the same flag defaults", len(got), len(want))
	}

	// Nothing here edits a label (ADR-0043/ADR-0048). Asserted directly rather than
	// promised: label, provenance and expected extraction, row for row.
	before := map[string]goldRow{}
	for _, row := range rows {
		before[row.URL] = row
	}
	for _, row := range snapshot.Rows {
		was, ok := before[row.URL]
		if !ok {
			t.Errorf("%s appeared in the record; a refit appends nothing", row.URL)
			continue
		}
		if row.Label != was.Label {
			t.Errorf("%s: label %q became %q", row.URL, was.Label, row.Label)
		}
		if !reflect.DeepEqual(row.LabelProvenance, was.LabelProvenance) {
			t.Errorf("%s: label provenance changed\n was %+v\n now %+v", row.URL, was.LabelProvenance, row.LabelProvenance)
		}
		if !reflect.DeepEqual(row.Expected, was.Expected) {
			t.Errorf("%s: expected extraction changed\n was %+v\n now %+v", row.URL, was.Expected, row.Expected)
		}
	}
	if len(snapshot.Rows) != len(rows) {
		t.Errorf("the record holds %d rows, want the %d it started with", len(snapshot.Rows), len(rows))
	}
}

// TestGoldSetRefitRefusesToRaiseARatchet is ADR-0048's rule made executable: a pending
// count rising means a human signature vanished, and only the person who withdraws a
// confirmation may move it that way. The refusal has to land BEFORE anything is
// written, or the pass would leave the tree carrying a change nobody agreed to.
func TestGoldSetRefitRefusesToRaiseARatchet(t *testing.T) {
	dir := tinyGoldSetDir(t, tinyGoldSet())
	counts := writeCountsFixture(t, map[string]int{"pendingBoundaryConfirmations": 0})
	weights := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")
	was := readFileOrFail(t, counts)

	var out bytes.Buffer
	code := goldSetRefit(refitConfig{Dir: dir, Counts: counts, Weights: weights, Out: &out})
	if code != 2 {
		t.Fatalf("goldset-refit exited %d, want 2", code)
	}
	if !strings.Contains(out.String(), "pendingBoundaryConfirmations") {
		t.Errorf("the refusal does not name the constant:\n%s", out.String())
	}
	if got := readFileOrFail(t, counts); got != was {
		t.Errorf("the counts file was rewritten despite the refusal")
	}
	if _, err := os.Stat(weights); err == nil {
		t.Errorf("%s was written despite the refusal", weights)
	}
}

// TestGoldSetRefitReportsAnUnmetFloor holds the other half of the exit-code contract:
// an unshippable RESULT is not a failed PASS. Every rung-8 accept in this fixture is
// detail, so the zero-detail-loss constraint can cut nothing and the veto share is 0 by
// construction, independent of the fit -- and the weights are still written, because
// the tree must be left consistent whatever the verdict says.
func TestGoldSetRefitReportsAnUnmetFloor(t *testing.T) {
	rows := []goldRow{}
	for _, row := range tinyGoldSet() {
		// The weakest accepts are exactly the empty-content rows: drop them and the
		// accept set is all detail.
		if row.Content.Title == "" && row.Content.MainContent == "" {
			continue
		}
		rows = append(rows, row)
	}
	dir := tinyGoldSetDir(t, rows)
	counts := writeCountsFixture(t, nil)
	weights := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")

	var out bytes.Buffer
	code := goldSetRefit(refitConfig{Dir: dir, Counts: counts, Weights: weights, Out: &out})
	if code != 1 {
		t.Fatalf("goldset-refit exited %d, want 1", code)
	}
	if !strings.Contains(out.String(), "NOT MET") {
		t.Errorf("the verdict does not report the pre-registered condition as unmet:\n%s", out.String())
	}
	if _, err := os.Stat(weights); err != nil {
		t.Errorf("the artifact was not written: %v. The exit code is a verdict on the result, not a sign the pass aborted -- the tree must be left consistent.", err)
	}
}

// TestGoldSetRefitVerdictOnAFailingSuite is the third reason a result is unshippable.
// It drives goldSetRefit one level below the entry point with a stubbed suite, because
// the real one is `go test ./...` -- inside `go test` that recurses, and it needs
// Docker.
func TestGoldSetRefitVerdictOnAFailingSuite(t *testing.T) {
	dir := tinyGoldSetDir(t, tinyGoldSet())
	counts := writeCountsFixture(t, nil)
	weights := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")

	var out bytes.Buffer
	code := goldSetRefit(refitConfig{
		Dir: dir, Counts: counts, Weights: weights, Verify: true, Out: &out,
		Suite: func(w io.Writer) error {
			fmt.Fprintln(w, "--- FAIL: TestExtractGoldSetFalseDropGuard")
			return errors.New("exit status 1")
		},
	})
	if code != 1 {
		t.Fatalf("goldset-refit exited %d, want 1", code)
	}
	if !strings.Contains(out.String(), "FAIL") {
		t.Errorf("the verdict does not report the suite as red:\n%s", out.String())
	}
}

// TestGoldSetRefitRefusesAWiringError covers phase 0. Each of these is refused before
// anything is read or written, so a mistyped knob can never leave the record applied,
// the counts moved and the weights behind.
func TestGoldSetRefitRefusesAWiringError(t *testing.T) {
	goodDir := tinyGoldSetDir(t, tinyGoldSet())
	goodCounts := writeCountsFixture(t, nil)

	missingOne := filepath.Join(t.TempDir(), "counts.go")
	src := readFileOrFail(t, writeCountsFixture(t, nil))
	trimmed := strings.Replace(src, "\tambiguousRows = 999\n", "", 1)
	if trimmed == src {
		t.Fatal("the counts fixture no longer declares ambiguousRows; this case has nothing to remove")
	}
	if err := os.WriteFile(missingOne, []byte(trimmed), 0o644); err != nil {
		t.Fatalf("write the trimmed counts: %v", err)
	}

	for name, args := range map[string][]string{
		"an empty gold set directory": {"-dir", t.TempDir(), "-counts", goodCounts},
		"no such counts file":         {"-dir", goodDir, "-counts", filepath.Join(t.TempDir(), "nope.go")},
		"a counts file missing one":   {"-dir", goodDir, "-counts", missingOne},
		"no such weights directory":   {"-dir", goodDir, "-counts", goodCounts, "-weights", filepath.Join(t.TempDir(), "nope", "weights.go")},
	} {
		t.Run(name, func(t *testing.T) {
			weights := filepath.Join(t.TempDir(), "posting_score_weights_gen.go")
			full := append([]string{"-weights", weights, "-verify=false"}, args...)
			if code := runGoldSetRefit(full); code != 2 {
				t.Errorf("exit code %d, want 2", code)
			}
			if _, err := os.Stat(weights); err == nil {
				t.Errorf("%s was written despite the wiring error", weights)
			}
		})
	}
}

// TestGoldSetRefitCarriesTheGenerateDirectivesArguments is what buys back the one risk
// of calling the trainer in-process instead of shelling out to `go generate`: the
// runbook's claim that this verb replaces that step is otherwise unchecked. The
// directive and the refit must name the same two files.
func TestGoldSetRefitCarriesTheGenerateDirectivesArguments(t *testing.T) {
	src := readFileOrFail(t, committedWeights())
	directive := ""
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "//go:generate ") {
			directive = line
			break
		}
	}
	if directive == "" {
		t.Fatalf("%s carries no //go:generate directive; the equivalence this pins has nothing to compare against", committedWeights())
	}

	// The directive runs with internal/pagegate as its working directory, and the verb
	// runs from the repo root, so both sides are resolved to a path relative to
	// cmd/llmbench -- the tests' own working directory.
	const pagegateDir = "../../internal/pagegate"
	const repoRoot = "../.."
	fields := strings.Fields(directive)
	got := map[string]string{}
	for i, f := range fields {
		if (f == "-in" || f == "-out") && i+1 < len(fields) {
			got[f] = filepath.Clean(filepath.Join(pagegateDir, fields[i+1]))
		}
	}
	opts := defaultTrainOptions()
	for flag, want := range map[string]string{
		"-in":  filepath.Clean(filepath.Join(repoRoot, opts.In)),
		"-out": filepath.Clean(filepath.Join(repoRoot, opts.Out)),
	} {
		if got[flag] != want {
			t.Errorf("the //go:generate directive's %s resolves to %q, but goldset-refit's default is %q; the runbook says the verb replaces that step, so the two must name one file",
				flag, got[flag], want)
		}
	}
}
