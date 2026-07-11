// Package main is llmbench: the career-page classifier benchmark over the Gold
// Set (ADR-0008). Its default verb, bench, runs each labeled HTML fixture
// through the real pipeline (parser.Parse -> pagegate.CareerPage) and prints a
// Gate scorecard, exiting non-zero on any Leak, False-Certain, or structural
// violation. The capture verb (#49) fetches a page through the crawler
// downloader and freezes it as a Gold-Set fixture; the label/diff verbs are
// stubs later tickets fill in.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

func main() {
	verb, rest := parseVerb(os.Args[1:])
	switch verb {
	case "bench":
		os.Exit(runBench(rest))
	case "capture":
		os.Exit(runCapture(rest))
	case "label", "diff":
		os.Exit(runStub(verb, rest))
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
func runBench(args []string) int {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	gold := fs.String("gold", "cmd/llmbench/testdata", "Gold-Set directory holding manifest.json and pages/*.html")
	gateConfig := fs.String("gate-config", "", "path to a JSON LLMGateConfig override; keys are the Go field names CareerPathSignals/RejectPathSignals (the struct has no json tags); empty uses DefaultLLMGateConfig")
	_ = fs.Parse(args)

	cfg, err := loadGateConfig(*gateConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench bench: load gate config: %v\n", err)
		return 2
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
		rows = append(rows, bench.VerdictRow{
			URL:      e.URL,
			Category: e.Category,
			Label:    e.Label,
			Gate:     bench.GateOutcomeFrom(accept, certain),
		})
	}

	report := bench.Score(rows)
	printReport(os.Stdout, report)
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
func printReport(w io.Writer, r Report) {
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
}

// Report aliases bench.Report so printReport reads without the package qualifier.
type Report = bench.Report

// red wraps s in the ANSI red escape so gate regressions are visible on a
// terminal. Redirected output keeps the codes; that is acceptable for a dev tool.
func red(s string) string { return "\033[31m" + s + "\033[0m" }

// runStub handles the not-yet-implemented verbs (label -> #51, diff -> #53). It
// still builds the verb's FlagSet so the dispatch scaffold is real, then reports
// the verb is unimplemented.
func runStub(verb string, args []string) int {
	fs := flag.NewFlagSet(verb, flag.ExitOnError)
	_ = fs.Parse(args)
	fmt.Fprintf(os.Stderr, "llmbench %s: not yet implemented\n", verb)
	return 2
}
