package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// This file is the rendering A/B (ADR-0046, #282): it replays the extract path
// twice over the same pages -- once with the parser producing Flattened Text and
// once with it producing a Structural Rendering -- at ONE shared prompt budget, and
// reports each arm's extract-call rate and false-drop count beside the delta
// between them. No network, no model, no Docker.
//
// Two halves, and they answer different questions.
//
// The GATE half is a neutrality check. Every input pagegate.ExtractDecision reads
// is either URL-derived, taken from a field the renderer does not touch
// (content.Embeds, content.ElementIDs, content.URLs, content.JSONLD), or derived
// from MainContent through crawler.FlattenedText -- whose round-trip back onto
// today's bytes is asserted byte for byte by the parser's
// TestStructuralRenderingRoundTrip. So the Gate's decision is invariant to the
// rendering BY CONSTRUCTION, and the expected delta is exactly zero. Measuring it
// anyway is worth an exit code because the invariant is asserted at the parser
// seam, and the extract path is a different, longer seam over pages the golden file
// does not cover: a flip here is a bug in the renderer or in a derivation, never a
// finding about the rendering.
//
// The BUDGET half is where the two renderings genuinely differ, and it is what
// informs the recommendation on PARSE_STRUCTURAL_RENDERING's default: structure
// costs prompt runes, and at a fixed cap those runes come out of the page text the
// model gets to read.

// renderingArm is one arm of the A/B: what one renderer's output does to the
// extract path -- the Gate's decision and its reject rung, and what survives the
// prompt budget.
type renderingArm struct {
	Extract bool                 `json:"extract"`
	Rung    pagegate.ExtractRung `json:"rung"`
	// PromptRunes is the size of openrouter.PromptForm(MainContent, budget) -- the
	// exact bytes the extractor sends, produced by the extractor's own function so
	// the benchmark cannot drift from production's prompt.
	PromptRunes int `json:"prompt_runes"`
	// PageWords is how many WORDS of the page survive the budget.
	//
	// Words, not characters, and that is not a stylistic choice. WithoutLinkTargets
	// leaves a link's brackets in place, so flattening a narrowed prompt keeps two
	// characters per link the page never had -- a character metric then reads the
	// structural arm as delivering MORE page text than the page contains (1.0136 on
	// the extract fixtures, an impossible number). Words are stable across that:
	// "[Apply now]" is the same two words as "Apply now".
	//
	// One honest imprecision remains and is deliberately NOT repaired: a cut can land
	// mid-marker ("...[input text: Vorn"), leaving a fragment FlattenedText will not
	// strip, so a truncated structural prompt can contribute a handful of synthetic
	// words at the cut. Repairing it would make the benchmark measure something the
	// extractor does not send.
	PageWords int  `json:"page_words"`
	Truncated bool `json:"truncated"`
}

// renderingRow is one page measured in BOTH arms.
type renderingRow struct {
	URL   string             `json:"url"`
	Label bench.ExtractLabel `json:"label,omitempty"` // "" for a page carrying no extract label
	// PageWords is the page text available before the budget, identical in both arms
	// by the round-trip invariant.
	PageWords int `json:"page_words_available"`
	// TwoArmed records that the arms really carry different bytes. Without it "the
	// arms agree" passes trivially whenever the renderer switch stops working.
	TwoArmed bool         `json:"two_armed"`
	Flat     renderingArm `json:"flattened"`
	Struct   renderingArm `json:"structural"`
}

// fixtureRef is a page on disk plus whatever ground truth it carries. The two
// committed fixture sets carry different manifests -- the classifier set's rows are
// labelled on the CAREER-PAGE axis and carry no extract label at all -- so they are
// projected onto this one shape rather than having their labels reinterpreted.
// Deriving an extract label from a career-page category would be fabricating ground
// truth, which is the one thing a benchmark may not do.
type fixtureRef struct {
	File  string
	URL   string
	Label bench.ExtractLabel // "" when the set carries no extract label
}

// runScoreRendering scores the extract path under both renderings over the
// committed fixture sets and the Extract Gold Set, at one matched prompt budget,
// and prints the A/B report. Exit: 2 on a wiring error, 1 when the two arms
// DISAGREE (a gate flip, or a false-drop present in one arm and not the other), 0
// otherwise.
//
// The exit rule is on the delta, never on the level, and that distinction is the
// whole design of this verb. score-capture already exits 1 over the committed gold
// set -- 13 tolerated false-drops, named in extractGoldSetFalseDrops and guarded by
// TestExtractGoldSetFalseDropGuard. A verb that inherited "fail on any false-drop"
// would be red on day one for reasons that have nothing to do with the rendering,
// and the A/B's actual finding would be invisible underneath it. Absolute
// false-drops are extract's and score-capture's business; this verb owns the
// difference between the arms.
func runScoreRendering(args []string) int {
	fs := flag.NewFlagSet("score-rendering", flag.ExitOnError)
	gold := fs.String("gold", "cmd/llmbench/extract-testdata", "extract-labelled HTML fixture directory (manifest.json + pages/*.html); the only pages that are BOTH extract-labelled and re-renderable, so the per-arm false-drop count comes from here")
	pages := fs.String("pages", "cmd/llmbench/testdata", "classifier Gold-Set HTML fixture directory; real committed pages carrying no extract label, so this block reports the call rate and the budget cost and never a false-drop")
	in := fs.String("in", filepath.Join(defaultGoldSetDir, goldSetFile), "labelled Extract Gold Set JSONL; empty skips that block")
	budget := fs.Int("budget", openrouter.DefaultExtractMaxChars, "prompt budget in RUNES, applied identically to BOTH arms (LLM_EXTRACT_MAX_CHARS' default)")
	gateConfig := fs.String("gate-config", "", "path to a JSON LLMGateConfig override applied on top of DefaultLLMGateConfig; keys are the Go field names (the struct has no json tags), so a partial file overrides only the fields it names; empty uses DefaultLLMGateConfig unchanged")
	jsonOut := fs.Bool("json", false, "emit the A/B report as JSON to stdout instead of the human-readable report; the exit code is unchanged")
	_ = fs.Parse(args)

	if *budget < 1 {
		fmt.Fprintf(os.Stderr, "llmbench score-rendering: -budget must be >= 1, got %d\n", *budget)
		return 2
	}
	cfg, err := loadGateConfig(*gateConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench score-rendering: load gate config: %v\n", err)
		return 2
	}

	report := renderingReport{Budget: *budget, GateConfig: *gateConfig}

	if *gold != "" {
		rows, err := replayRenderingFixtures(os.DirFS(*gold), extractFixtureRefs, cfg, *budget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench score-rendering: %s: %v\n", *gold, err)
			return 2
		}
		block := scoreRenderingBlock(*gold, rows)
		report.LabelledFixtures = &block
	}
	if *pages != "" {
		rows, err := replayRenderingFixtures(os.DirFS(*pages), classifierFixtureRefs, cfg, *budget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench score-rendering: %s: %v\n", *pages, err)
			return 2
		}
		block := scoreRenderingBlock(*pages, rows)
		report.RealPages = &block
	}
	if *in != "" {
		rows, census, skipped, err := replayRenderingGoldSet(*in, cfg, *budget)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench score-rendering: %v\n", err)
			return 2
		}
		if skipped > 0 {
			fmt.Fprintf(os.Stderr, "note: scored %d labelled gold-set rows, skipped %d unlabelled\n", len(rows), skipped)
		}
		block := scoreRenderingBlock(*in, rows)
		block.Renderers = census
		report.GoldSet = &block
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench score-rendering: encode json: %v\n", err)
			return 2
		}
	} else {
		printRenderingReport(os.Stdout, report)
	}
	printRenderingAlarms(report)
	if report.Failed() {
		return 1
	}
	return 0
}

// extractFixtureRefs projects the reject-rung fixture manifest onto fixtureRef,
// carrying its extract label through.
func extractFixtureRefs(fsys fs.FS) ([]fixtureRef, error) {
	m, err := bench.LoadExtractManifest(fsys)
	if err != nil {
		return nil, err
	}
	refs := make([]fixtureRef, 0, len(m.Entries))
	for _, e := range m.Entries {
		refs = append(refs, fixtureRef{File: e.File, URL: e.URL, Label: e.Label})
	}
	return refs, nil
}

// classifierFixtureRefs projects the career-page Gold Set's manifest onto
// fixtureRef with NO label. Its entries are labelled career_page /
// not_career_page and categorised on that axis; mapping a category like
// job_posting_single onto the extract label detail would manufacture ground truth
// on an axis nobody labelled, so this block is scored for call rate and budget cost
// only.
func classifierFixtureRefs(fsys fs.FS) ([]fixtureRef, error) {
	m, err := bench.LoadManifest(fsys)
	if err != nil {
		return nil, err
	}
	refs := make([]fixtureRef, 0, len(m.Entries))
	for _, e := range m.Entries {
		refs = append(refs, fixtureRef{File: e.File, URL: e.URL})
	}
	return refs, nil
}

// replayRenderingFixtures parses every fixture in fsys TWICE -- once through a
// flattening parser and once through a rendering one -- and measures the extract
// path over each result. loadRefs selects which manifest shape the directory
// carries. It is the shared body of the verb's two fixture blocks and their
// regression test, so both drive the identical live pipeline; any wiring fault is
// wrapped and returned rather than exiting, so the test can assert on it. Mirrors
// replayExtractGate.
func replayRenderingFixtures(fsys fs.FS, loadRefs func(fs.FS) ([]fixtureRef, error), cfg crawler.LLMGateConfig, budget int) ([]renderingRow, error) {
	refs, err := loadRefs(fsys)
	if err != nil {
		return nil, err
	}

	// Two parsers, built once. A single parser whose switch is flipped between
	// parses would make every measurement depend on call order.
	flatParser := parser.NewHTMLParser(parser.WithStructuralRendering(false))
	structParser := parser.NewHTMLParser(parser.WithStructuralRendering(true))

	rows := []renderingRow{}
	for _, ref := range refs {
		html, err := fs.ReadFile(fsys, "pages/"+ref.File)
		if err != nil {
			return nil, fmt.Errorf("read fixture %q: %w", ref.File, err)
		}
		flatContent, err := flatParser.Parse(html)
		if err != nil {
			return nil, fmt.Errorf("parse %q flattened: %w", ref.File, err)
		}
		structContent, err := structParser.Parse(html)
		if err != nil {
			return nil, fmt.Errorf("parse %q structural: %w", ref.File, err)
		}
		u, err := crawler.NewURL(ref.URL)
		if err != nil {
			return nil, fmt.Errorf("url %q: %w", ref.URL, err)
		}
		rows = append(rows, renderingRow{
			URL:       ref.URL,
			Label:     ref.Label,
			PageWords: pageWords(flatContent.MainContent),
			TwoArmed:  flatContent.MainContent != structContent.MainContent,
			Flat:      measureArm(u, flatContent, cfg, budget),
			Struct:    measureArm(u, structContent, cfg, budget),
		})
	}
	return rows, nil
}

// replayRenderingGoldSet measures both arms over the committed Extract Gold Set.
//
// The gold set stores the PARSED Content and never the HTML (ADR-0043), so a
// renderer cannot be replayed over it and no fetch may substitute (this verb is
// offline). What it can do exactly is the reduction: a row's stored MainContent is
// the structural arm as captured, and crawler.FlattenedText of it is the flattened
// arm. On a row captured before the renderer stamp existed (ADR-0047) the reduction
// is the identity -- FlattenedText is idempotent -- so both arms read the same bytes
// and the row reports TwoArmed false, which is the honest statement rather than a
// hidden zero. A row stamped structural-v1 is genuinely two-armed and is scored as
// such the moment a harvest under the flag produces one.
//
// Returns the measured rows, the renderer census, the count of skipped unlabelled
// lines, and any read error.
func replayRenderingGoldSet(path string, cfg crawler.LLMGateConfig, budget int) (rows []renderingRow, census map[string]int, skipped int, err error) {
	goldRows, err := readGoldSet(path)
	if err != nil {
		return nil, nil, 0, err
	}
	census = map[string]int{}
	rows = []renderingRow{}
	for _, rec := range goldRows {
		census[rendererName(rec.Renderer)]++
		if !rec.Label.Valid() {
			skipped++
			continue
		}
		u, err := crawler.NewURL(rec.URL)
		if err != nil {
			return nil, nil, skipped, fmt.Errorf("gold set %q: url %q: %w", path, rec.URL, err)
		}
		structContent := rec.Content
		flatContent := rec.Content
		flatContent.MainContent = crawler.FlattenedText(rec.Content.MainContent)
		rows = append(rows, renderingRow{
			URL:       rec.URL,
			Label:     rec.Label,
			PageWords: pageWords(flatContent.MainContent),
			TwoArmed:  rec.Renderer == parser.RendererStructural,
			Flat:      measureArm(u, &flatContent, cfg, budget),
			Struct:    measureArm(u, &structContent, cfg, budget),
		})
	}
	return rows, census, skipped, nil
}

// rendererUnstamped names the census bucket for a row drawn before the renderer
// stamp existed. Such a row carries an UNKNOWN renderer, not an empty one
// (ADR-0047), and the report says so rather than filing it under flattened-v1 --
// which would assert something about its bytes that nobody recorded.
const rendererUnstamped = "unstamped"

func rendererName(stamp string) string {
	if stamp == "" {
		return rendererUnstamped
	}
	return stamp
}

// measureArm runs the real Extract Gate over one arm's Content and measures what
// that arm's prompt costs at the shared budget.
func measureArm(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig, budget int) renderingArm {
	extract, rung := pagegate.ExtractDecision(u, content, cfg)
	prompt := openrouter.PromptForm(content.MainContent, budget)
	return renderingArm{
		Extract:     extract,
		Rung:        rung,
		PromptRunes: utf8.RuneCountInString(prompt),
		PageWords:   pageWords(prompt),
		// The prompt form is what the cap bites on, so truncation is measured against
		// the NARROWED text and not against the stored rendering: an href the model
		// never sees cannot push a page over the budget.
		Truncated: utf8.RuneCountInString(crawler.WithoutLinkTargets(content.MainContent)) > budget,
	}
}

// pageWords counts the words a rendering carries, reducing it to Flattened Text
// first so the two arms are counted on one scale. See renderingArm.PageWords for
// why words are the unit.
func pageWords(rendering string) int {
	return len(strings.Fields(crawler.FlattenedText(rendering)))
}

// renderingBlock is one measured population's A/B result: the per-arm scorecards,
// the deltas between them, and the budget cost.
type renderingBlock struct {
	Source string `json:"source"`
	Rows   int    `json:"rows"`
	// Labelled is how many rows carry an extract label, and so how many rows the
	// per-arm false-drop counts are computed over. Zero for the classifier fixtures.
	Labelled int `json:"labelled"`
	TwoArmed int `json:"two_armed"`
	// Renderers is the renderer census, present only for the gold-set block: every
	// other population is re-rendered here rather than read off a stamp.
	Renderers map[string]int `json:"renderers,omitempty"`

	Flat   renderingArmScore `json:"flattened"`
	Struct renderingArmScore `json:"structural"`

	// Flips are the URLs the two arms decided differently on. Non-empty is a hard
	// failure: the Gate is invariant to the rendering by construction, so a flip is a
	// bug in the renderer or in a derivation.
	Flips []string `json:"flips"`
	// DriftedDrops are URLs that are a false-drop in one arm and not the other. Also
	// a hard failure, and reported separately because a flip on an UNLABELLED page
	// cannot surface as one.
	DriftedDrops []string `json:"drifted_drops"`

	PageWordsAvailable int `json:"page_words_available"`
	// NewlyTruncated are URLs the structural arm truncates and the flattened arm does
	// not: the concrete pages that pay for the structure at this budget.
	NewlyTruncated []string `json:"newly_truncated"`
}

// renderingArmScore is one arm's numbers over one population.
type renderingArmScore struct {
	ExtractCalls    int      `json:"extract_calls"`
	ExtractCallRate float64  `json:"extract_call_rate"`
	FalseDrops      []string `json:"false_drops"`
	PromptRunes     int      `json:"prompt_runes"`
	PageWordsKept   int      `json:"page_words_kept"`
	PageWordsShare  float64  `json:"page_words_share"`
	TruncatedRows   int      `json:"truncated_rows"`
}

// renderingReport is the whole A/B: three populations measured at one budget.
type renderingReport struct {
	Budget     int    `json:"budget"`
	GateConfig string `json:"gate_config,omitempty"`
	// Each block is nil when its source was not scored (an empty flag).
	LabelledFixtures *renderingBlock `json:"labelled_fixtures,omitempty"`
	RealPages        *renderingBlock `json:"real_pages,omitempty"`
	GoldSet          *renderingBlock `json:"gold_set,omitempty"`
}

// Failed reports whether any block's arms disagreed. It is the sole fatal signal:
// see runScoreRendering on why the level is not one.
func (r renderingReport) Failed() bool {
	for _, b := range []*renderingBlock{r.LabelledFixtures, r.RealPages, r.GoldSet} {
		if b != nil && (len(b.Flips) > 0 || len(b.DriftedDrops) > 0) {
			return true
		}
	}
	return false
}

// scoreRenderingBlock folds one population's measured rows into its block.
func scoreRenderingBlock(source string, rows []renderingRow) renderingBlock {
	b := renderingBlock{Source: source, Rows: len(rows), NewlyTruncated: []string{}}
	for _, row := range rows {
		if row.Label.Valid() {
			b.Labelled++
		}
		if row.TwoArmed {
			b.TwoArmed++
		}
		b.PageWordsAvailable += row.PageWords
		if row.Struct.Truncated && !row.Flat.Truncated {
			b.NewlyTruncated = append(b.NewlyTruncated, row.URL)
		}
	}
	b.Flat = scoreRenderingArm(rows, func(r renderingRow) renderingArm { return r.Flat }, b.PageWordsAvailable)
	b.Struct = scoreRenderingArm(rows, func(r renderingRow) renderingArm { return r.Struct }, b.PageWordsAvailable)
	b.Flips, b.DriftedDrops = armsDisagree(rows)
	return b
}

// scoreRenderingArm folds one arm of one population. The false-drop list is
// produced by bench.ScoreExtract over the LABELLED rows, so this verb and
// score-capture agree on what a false-drop is by construction; a population with no
// labelled row contributes no false-drop list at all rather than an empty one that
// could be read as "none found".
func scoreRenderingArm(rows []renderingRow, pick func(renderingRow) renderingArm, available int) renderingArmScore {
	s := renderingArmScore{FalseDrops: []string{}}
	verdicts := []bench.ExtractVerdictRow{}
	for _, row := range rows {
		arm := pick(row)
		if arm.Extract {
			s.ExtractCalls++
		}
		s.PromptRunes += arm.PromptRunes
		s.PageWordsKept += arm.PageWords
		if arm.Truncated {
			s.TruncatedRows++
		}
		if row.Label.Valid() {
			verdicts = append(verdicts, bench.ExtractVerdictRow{URL: row.URL, Label: row.Label, Extract: arm.Extract})
		}
	}
	if len(rows) > 0 {
		s.ExtractCallRate = float64(s.ExtractCalls) / float64(len(rows))
	}
	if available > 0 {
		s.PageWordsShare = float64(s.PageWordsKept) / float64(available)
	}
	if len(verdicts) > 0 {
		s.FalseDrops = bench.ScoreExtract(verdicts).Extract.FalseDrops
	}
	return s
}

// armsDisagree reports the A/B's only fatal condition: a page the two renderings
// took a different extract decision on (flips), and a page that is a false-drop
// under one rendering and not the other (driftedDrops). Both are empty on a healthy
// run -- the Gate reads no renderer-dependent input that FlattenedText does not
// reduce -- so a non-empty list is a defect in the renderer or in a derivation, and
// the fix belongs there rather than in this benchmark.
//
// It is a pure function over rows so it can be unit-tested with hand-built
// disagreements, which the real parser can never produce.
func armsDisagree(rows []renderingRow) (flips []string, driftedDrops []string) {
	flips, driftedDrops = []string{}, []string{}
	for _, row := range rows {
		if row.Flat.Extract != row.Struct.Extract {
			flips = append(flips, row.URL)
		}
		if row.Label != bench.ExtractDetail {
			continue
		}
		// A detail the gate skips is a false-drop, so on a detail row a drift is
		// necessarily also a flip. The list is kept separate anyway because it names the
		// flips that COST a Job Listing, and because comparing the two arms' drop COUNTS
		// would miss a swap -- one page starting to drop as another stops.
		flatDrop, structDrop := !row.Flat.Extract, !row.Struct.Extract
		if flatDrop != structDrop {
			driftedDrops = append(driftedDrops, row.URL)
		}
	}
	return flips, driftedDrops
}

// printRenderingReport writes the human-readable A/B. Each block leads with the
// population it describes, because the populations answer different questions and
// reading one block's number as another's is the mistake available here: only the
// labelled fixtures can produce a false-drop count, only the real pages show what
// the rendering costs on real markup, and the gold set is a baseline whose rows no
// renderer can be replayed over.
func printRenderingReport(w io.Writer, r renderingReport) {
	fmt.Fprintln(w, "rendering A/B -- Flattened Text against the Structural Rendering (offline: no network, no model)")
	fmt.Fprintf(w, "  budget            %d runes, applied identically to both arms (LLM_EXTRACT_MAX_CHARS' default)\n", r.Budget)
	cfg := r.GateConfig
	if cfg == "" {
		cfg = "default"
	}
	fmt.Fprintf(w, "  gate config       %s\n", cfg)

	if b := r.LabelledFixtures; b != nil {
		fmt.Fprintf(w, "\nlabelled fixtures (%s, n=%d)\n", b.Source, b.Rows)
		fmt.Fprintln(w, "  The only pages in the tree that are BOTH extract-labelled and re-renderable, so the")
		fmt.Fprintln(w, "  per-arm false-drop count comes from here. They are synthetic and are evidence of")
		fmt.Fprintln(w, "  nothing about the live stream (ADR-0043); what they are evidence FOR is that the two")
		fmt.Fprintln(w, "  renderings decide identically.")
		printRenderingBlock(w, *b)
	}
	if b := r.RealPages; b != nil {
		fmt.Fprintf(w, "\nreal pages (%s, n=%d)\n", b.Source, b.Rows)
		fmt.Fprintln(w, "  Real committed pages, human-verified on the CAREER-PAGE axis only -- they carry no")
		fmt.Fprintln(w, "  extract label, so nothing here is scored for false-drops. This is where the rendering's")
		fmt.Fprintln(w, "  real cost shows: real markup density, real page lengths, real truncation.")
		printRenderingBlock(w, *b)
	}
	if b := r.GoldSet; b != nil {
		fmt.Fprintf(w, "\nExtract Gold Set (%s, n=%d labelled real rows)\n", b.Source, b.Labelled)
		fmt.Fprintln(w, "  These rows are parser-blind BY DESIGN: ADR-0043 stores the parsed Content and never")
		fmt.Fprintln(w, "  the HTML, so no renderer can be replayed over them. A row stamped structural-v1 IS")
		fmt.Fprintln(w, "  measured in both arms (its stored bytes are one arm, their reduction the other); a row")
		fmt.Fprintln(w, "  drawn before the stamp (ADR-0047) reduces to itself, so its two arms read the same")
		fmt.Fprintln(w, "  bytes and their delta is 0 by derivation rather than by measurement. What this block")
		fmt.Fprintln(w, "  contributes either way is the real-page baseline the deltas above apply to.")
		fmt.Fprintf(w, "  renderers         %s   (two-armed rows: %d)\n", censusLine(b.Renderers), b.TwoArmed)
		printRenderingBlock(w, *b)
	}
}

// printRenderingBlock writes one population's two arms, their delta, the budget
// cost, and any disagreement between the arms. The false-drop columns appear only
// when the population carries extract labels: on a population nobody labelled on
// that axis a "false-drops 0" would read as a measurement instead of as an absence
// of ground truth.
func printRenderingBlock(w io.Writer, b renderingBlock) {
	if b.Labelled > 0 {
		fmt.Fprintf(w, "  arm flattened     extract-calls %d  rate %.4f  false-drops %d\n", b.Flat.ExtractCalls, b.Flat.ExtractCallRate, len(b.Flat.FalseDrops))
		fmt.Fprintf(w, "  arm structural    extract-calls %d  rate %.4f  false-drops %d\n", b.Struct.ExtractCalls, b.Struct.ExtractCallRate, len(b.Struct.FalseDrops))
		fmt.Fprintf(w, "  delta             extract-call-rate %+.4f   false-drops %+d\n",
			b.Struct.ExtractCallRate-b.Flat.ExtractCallRate, len(b.Struct.FalseDrops)-len(b.Flat.FalseDrops))
	} else {
		fmt.Fprintf(w, "  arm flattened     extract-calls %d  rate %.4f\n", b.Flat.ExtractCalls, b.Flat.ExtractCallRate)
		fmt.Fprintf(w, "  arm structural    extract-calls %d  rate %.4f\n", b.Struct.ExtractCalls, b.Struct.ExtractCallRate)
		fmt.Fprintf(w, "  delta             extract-call-rate %+.4f\n", b.Struct.ExtractCallRate-b.Flat.ExtractCallRate)
	}
	fmt.Fprintf(w, "  gate flips        %d of %d   (arms carry different bytes on %d)\n", len(b.Flips), b.Rows, b.TwoArmed)
	fmt.Fprintf(w, "  budget            page words %d available; delivered flat %d (%.4f), structural %d (%.4f), delta %d (%+.2f%%)\n",
		b.PageWordsAvailable, b.Flat.PageWordsKept, b.Flat.PageWordsShare, b.Struct.PageWordsKept, b.Struct.PageWordsShare,
		b.Struct.PageWordsKept-b.Flat.PageWordsKept, wordDeltaPercent(b))
	fmt.Fprintf(w, "  prompt size       flat %d runes, structural %d (%.4fx)\n",
		b.Flat.PromptRunes, b.Struct.PromptRunes, ratioOf(b.Struct.PromptRunes, b.Flat.PromptRunes))
	fmt.Fprintf(w, "  over budget       flat %d of %d (%.4f), structural %d (%.4f)\n",
		b.Flat.TruncatedRows, b.Rows, ratioOf(b.Flat.TruncatedRows, b.Rows), b.Struct.TruncatedRows, ratioOf(b.Struct.TruncatedRows, b.Rows))
	for _, url := range b.NewlyTruncated {
		fmt.Fprintf(w, "  newly truncated   %s\n", url)
	}
	// The disagreements go into the REPORT and not only onto stderr, unlike the
	// extract scorecard's false-drops: a flip is this verb's entire finding, so a
	// report redirected to a file has to carry it. runScoreRendering repeats them on
	// stderr in red so they also stand out on a terminal.
	for _, url := range b.Flips {
		fmt.Fprintf(w, "  arm-flip          %s\n", url)
	}
	for _, url := range b.DriftedDrops {
		fmt.Fprintf(w, "  drop-drift        %s\n", url)
	}
}

// printRenderingAlarms repeats each disagreement on stderr in ANSI red, so the one
// fatal signal stands out on the terminal whichever output form was asked for.
func printRenderingAlarms(r renderingReport) {
	for _, b := range []*renderingBlock{r.LabelledFixtures, r.RealPages, r.GoldSet} {
		if b == nil {
			continue
		}
		for _, url := range b.Flips {
			fmt.Fprintln(os.Stderr, red("ARM-FLIP    "+url+" (the two renderings took different extract decisions; the Gate is invariant to the rendering by construction, so this is a renderer or derivation bug)"))
		}
		for _, url := range b.DriftedDrops {
			fmt.Fprintln(os.Stderr, red("DROP-DRIFT  "+url+" (a real single-posting detail dropped under one rendering and kept under the other)"))
		}
	}
}

// wordDeltaPercent is the structural arm's page-word delivery as a percentage
// change from the flattened arm's -- the headline cost of the rendering at a fixed
// budget.
func wordDeltaPercent(b renderingBlock) float64 {
	if b.Flat.PageWordsKept == 0 {
		return 0
	}
	return 100 * float64(b.Struct.PageWordsKept-b.Flat.PageWordsKept) / float64(b.Flat.PageWordsKept)
}

func ratioOf(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// censusLine renders the renderer census in a fixed order, so two runs of the verb
// diff line by line.
func censusLine(census map[string]int) string {
	parts := []string{}
	for _, name := range []string{parser.RendererStructural, parser.RendererFlattened, rendererUnstamped} {
		parts = append(parts, fmt.Sprintf("%s %d", name, census[name]))
	}
	return strings.Join(parts, ", ")
}
