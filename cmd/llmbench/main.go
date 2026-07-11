// Package main is llmbench: the career-page classifier benchmark over the Gold
// Set (ADR-0008). Its default verb, bench, runs each labeled HTML fixture
// through the real pipeline (parser.Parse -> pagegate.CareerPage) and prints a
// Gate scorecard (or, with -json, the full machine-readable Report), exiting
// non-zero on any Leak, False-Certain, or structural violation. The capture verb
// (#49) fetches a page through the crawler downloader and freezes it as a Gold-Set
// fixture; the label verb (#54) has a stronger LABELER_* model propose labels for
// unverified fixtures; the diff verb (#53) reads two -json reports and prints the
// per-metric delta between them.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
)

func main() {
	verb, rest := parseVerb(os.Args[1:])
	switch verb {
	case "bench":
		os.Exit(runBench(rest))
	case "capture":
		os.Exit(runCapture(rest))
	case "label":
		os.Exit(runLabel(rest))
	case "diff":
		os.Exit(runDiff(rest))
	default:
		fmt.Fprintf(os.Stderr, "llmbench: unknown verb %q\n", verb)
		os.Exit(2)
	}
}

// parseVerb splits the command-line args (os.Args[1:]) into a verb and the
// remaining flag args. The verb is the first arg when it is present and not
// flag-like (no leading "-"); otherwise it defaults to "bench" and every arg is
// treated as a flag, so `llmbench -gold x` runs the default verb.
func parseVerb(args []string) (verb string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "bench", args
}

// runBench loads the Gold Set, runs every fixture through the real parser and
// pre-LLM gate to produce verdict rows, scores them, prints the report, and
// returns the process exit code (1 on any gate regression, 2 on a wiring error).
// -mode selects which fixtures reach the LLM (as-wired: only gate-uncertain, the
// production wiring; isolated: every fixture, for LLM scoring only), and -n
// repeats each classification N times so the LLM scorecard can report a by-URL
// flip-rate; both are descriptive and never move the Gate-driven exit code.
func runBench(args []string) int {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	gold := fs.String("gold", "cmd/llmbench/testdata", "Gold-Set directory holding manifest.json and pages/*.html")
	gateConfig := fs.String("gate-config", "", "path to a JSON LLMGateConfig override; keys are the Go field names CareerPathSignals/RejectPathSignals (the struct has no json tags); empty uses DefaultLLMGateConfig")
	llm := fs.Bool("llm", true, "confirm gate-uncertain fixtures with the real openrouter classifier (LLM_* env); -llm=false runs gate-only")
	mode := fs.String("mode", "as-wired", "as-wired: classify only gate-uncertain fixtures (production wiring); isolated: classify every fixture, bypassing the gate for LLM scoring only")
	repeats := fs.Int("n", 1, "repeat each LLM classification N times (same seed); the scored verdict is the majority vote and the LLM scorecard reports a by-URL flip-rate")
	jsonOut := fs.Bool("json", false, "emit the full scorecard as JSON to stdout instead of the human-readable report; the exit code is unchanged")
	_ = fs.Parse(args)

	isolated := *mode == "isolated"
	if *mode != "as-wired" && !isolated {
		fmt.Fprintf(os.Stderr, "llmbench bench: -mode must be as-wired or isolated, got %q\n", *mode)
		return 2
	}
	if *repeats < 1 {
		fmt.Fprintf(os.Stderr, "llmbench bench: -n must be >= 1, got %d\n", *repeats)
		return 2
	}
	if isolated && !*llm {
		fmt.Fprintf(os.Stderr, "llmbench bench: -mode isolated requires -llm (nothing to classify with -llm=false)\n")
		return 2
	}

	cfg, err := loadGateConfig(*gateConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench bench: load gate config: %v\n", err)
		return 2
	}

	ctx := context.Background()
	var confirmer careerpageprocessor.Confirmer
	if *llm {
		cfg, err := loadLLMConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench bench: %v\n", err)
			return 2
		}
		confirmer = openrouter.NewCareerPageClassifier(cfg)
	}

	fsys := os.DirFS(*gold)
	m, err := bench.LoadManifest(fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench bench: %v\n", err)
		return 2
	}

	p := parser.NewHTMLParser()
	rows := []bench.VerdictRow{}
	for _, e := range m.Entries {
		html, err := e.ReadHTML(fsys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench bench: %v\n", err)
			return 2
		}
		content, err := p.Parse(html)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench bench: parse %q: %v\n", e.File, err)
			return 2
		}
		u, err := crawler.NewURL(e.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench bench: url %q: %v\n", e.URL, err)
			return 2
		}
		accept, certain := pagegate.CareerPage(u, content, cfg)
		gate := bench.GateOutcomeFrom(accept, certain)
		row := bench.VerdictRow{URL: e.URL, Category: e.Category, Label: e.Label, Gate: gate, Verified: e.Verified, ProposedLabel: e.ProposedLabel}
		if *llm && (isolated || gate == bench.GateUncertain) {
			votes := make([]bool, 0, *repeats)
			for range *repeats {
				confirmed, err := confirmer.Confirm(ctx, e.URL, content)
				if err != nil {
					fmt.Fprintf(os.Stderr, "llmbench bench: confirm %q: %v\n", e.URL, err)
					return 2
				}
				votes = append(votes, confirmed)
			}
			row.LLMVotes = votes
		}
		rows = append(rows, row)
	}

	report := bench.Score(rows)
	if *jsonOut {
		if err := bench.EncodeReport(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench bench: encode json: %v\n", err)
			return 2
		}
	} else {
		printReport(os.Stdout, report, *llm, *mode)
	}
	if report.Failed() {
		return 1
	}
	return 0
}

// loadGateConfig returns the gate config to benchmark with: the built-in default
// for an empty path, otherwise a JSON LLMGateConfig read from path. LLMGateConfig
// carries no json tags, so the JSON keys are the Go field names.
func loadGateConfig(path string) (crawler.LLMGateConfig, error) {
	if path == "" {
		return crawler.DefaultLLMGateConfig(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return crawler.LLMGateConfig{}, fmt.Errorf("read gate config: %w", err)
	}
	cfg := crawler.LLMGateConfig{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return crawler.LLMGateConfig{}, fmt.Errorf("parse gate config: %w", err)
	}
	return cfg, nil
}

// printReport writes the Gate scorecard: the descriptive summary (total, LLM
// calls, call rate) to w, and each hard failure (Leak, False-Certain, structural
// Violation) to stderr in ANSI red so a regression stands out on the terminal.
// When llm is set it also appends the descriptive LLM and end-to-end scorecards
// to w; those numbers never move the exit code. mode ("as-wired"/"isolated")
// only labels the LLM scorecard header with the population it measured.
func printReport(w io.Writer, r Report, llm bool, mode string) {
	g := r.Gate
	fmt.Fprintln(w, "gate scorecard")
	fmt.Fprintf(w, "  total          %d\n", g.Total)
	fmt.Fprintf(w, "  llm-calls      %d\n", g.LLMCalls)
	fmt.Fprintf(w, "  llm-call-rate  %.4f\n", g.LLMCallRate)

	for _, url := range g.Leaks {
		fmt.Fprintln(os.Stderr, red("LEAK          "+url+" (real Career Page rejected by the gate)"))
	}
	for _, url := range g.FalseCertains {
		fmt.Fprintln(os.Stderr, red("FALSE-CERTAIN "+url+" (non-page certain-accepted, skips the LLM)"))
	}
	for _, v := range g.Violations {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf("VIOLATION     %s [%s] want %s got %s", v.URL, v.Category, v.Want, v.Got)))
	}

	// The review queue is descriptive: it never moves the exit code, so it prints
	// plain to w (never red). The labeler and gate axes work without the LLM, so
	// it prints regardless of the -llm flag, before the LLM-only scorecards below.
	fmt.Fprintln(w, "review queue (unverified, needs human confirm, descriptive)")
	fmt.Fprintf(w, "  items          %d\n", len(r.ReviewQueue))
	for _, item := range r.ReviewQueue {
		reasons := make([]string, len(item.Reasons))
		for i, reason := range item.Reasons {
			reasons[i] = string(reason)
		}
		fmt.Fprintf(w, "  %s [%s] label=%s reasons: %s\n", item.URL, item.Category, item.Label, strings.Join(reasons, ","))
	}

	if !llm {
		return
	}

	l := r.LLM
	if mode == "isolated" {
		fmt.Fprintln(w, "llm scorecard (all fixtures, isolated, descriptive)")
	} else {
		fmt.Fprintln(w, "llm scorecard (gate-uncertain subset, descriptive)")
	}
	fmt.Fprintf(w, "  classified     %d\n", l.Total)
	fmt.Fprintf(w, "  precision      %.4f\n", l.Precision)
	fmt.Fprintf(w, "  recall         %.4f\n", l.Recall)
	fmt.Fprintf(w, "  accuracy       %.4f\n", l.Accuracy)
	fmt.Fprintf(w, "  flip-rate      %.4f\n", l.FlipRate)
	for _, url := range l.Flips {
		fmt.Fprintf(w, "  flip           %s\n", url)
	}

	e := r.EndToEnd.Overall
	fmt.Fprintln(w, "end-to-end scorecard (production decision, descriptive)")
	fmt.Fprintf(w, "  precision      %.4f\n", e.Precision)
	fmt.Fprintf(w, "  recall         %.4f\n", e.Recall)
	fmt.Fprintf(w, "  f1             %.4f\n", e.F1)
	fmt.Fprintf(w, "  accuracy       %.4f\n", e.Accuracy)
	for _, cat := range bench.AllCategories {
		c, ok := r.EndToEnd.ByCategory[cat]
		if !ok || c.Total == 0 {
			continue
		}
		fmt.Fprintf(w, "  %-18s p %.4f r %.4f f1 %.4f (n=%d)\n", cat, c.Precision, c.Recall, c.F1, c.Total)
	}
}

// Report aliases bench.Report so printReport reads without the package qualifier.
type Report = bench.Report

// red wraps s in the ANSI red escape so gate regressions are visible on a
// terminal. Redirected output keeps the codes; that is acceptable for a dev tool.
func red(s string) string { return "\033[31m" + s + "\033[0m" }
