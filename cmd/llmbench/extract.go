package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// runExtract loads the Extract Gold Set, replays each fixture through the real
// parser and the Extract Gate (parser.Parse -> pagegate.ShouldExtract) to produce
// binary extract-vs-skip verdict rows, scores them, prints the extract scorecard,
// and returns the process exit code: 1 on any false-drop (a real single-posting
// detail the gate rejected), 2 on a wiring error, 0 otherwise. The LLM extractor
// stage is deliberately not invoked here -- every scored artifact (false-drop
// guard, extract-call rate, per-class slices, residue counts) is produced entirely
// by the URL-only ShouldExtract decision, and the descriptive Empty-Extraction
// layer is owned by #113. Mirrors runBench's structure.
func runExtract(args []string) int {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	gold := fs.String("gold", "cmd/llmbench/extract-testdata", "Extract Gold-Set directory holding manifest.json and pages/*.html")
	gateConfig := fs.String("gate-config", "", "path to a JSON LLMGateConfig override applied on top of DefaultLLMGateConfig; keys are the Go field names (the struct has no json tags), so a partial file overrides only the fields it names; empty uses DefaultLLMGateConfig unchanged")
	jsonOut := fs.Bool("json", false, "emit the full extract scorecard as JSON to stdout instead of the human-readable report; the exit code is unchanged")
	_ = fs.Parse(args)

	cfg, err := loadGateConfig(*gateConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench extract: load gate config: %v\n", err)
		return 2
	}

	fsys := os.DirFS(*gold)
	m, err := bench.LoadExtractManifest(fsys)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench extract: %v\n", err)
		return 2
	}

	p := parser.NewHTMLParser()
	rows := []bench.ExtractVerdictRow{}
	for _, e := range m.Entries {
		html, err := e.ReadHTML(fsys)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench extract: %v\n", err)
			return 2
		}
		content, err := p.Parse(html)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench extract: parse %q: %v\n", e.File, err)
			return 2
		}
		u, err := crawler.NewURL(e.URL)
		if err != nil {
			fmt.Fprintf(os.Stderr, "llmbench extract: url %q: %v\n", e.URL, err)
			return 2
		}
		rows = append(rows, bench.ExtractVerdictRow{
			URL:     e.URL,
			Label:   e.Label,
			Extract: gateDecision(u, content, cfg),
		})
	}

	report := bench.ScoreExtract(rows)
	if *jsonOut {
		if err := bench.EncodeExtractReport(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench extract: encode json: %v\n", err)
			return 2
		}
	} else {
		printExtractReport(os.Stdout, report)
	}
	if report.Failed() {
		return 1
	}
	return 0
}

// gateDecision runs the current Extract Gate for one fixture. content is parsed
// and threaded here so #115 can flip the call to pagegate.ShouldExtract(u,
// content, cfg) -- adding the content reject rungs -- without touching this
// harness. Until then the parsed content only validates the frozen bytes; the
// scored decision is purely URL-driven.
func gateDecision(u crawler.URL, content *crawler.Content, cfg crawler.LLMGateConfig) bool {
	_ = content
	return pagegate.ShouldExtract(u, cfg)
}

// printExtractReport writes the extract scorecard: the descriptive summary (total,
// extract calls and rate, overall and per-class scores, residue counts, and any
// leaked non-postings) to w, and each fatal false-drop to stderr in ANSI red so a
// real detail page the gate rejected stands out on the terminal.
func printExtractReport(w io.Writer, r bench.ExtractReport) {
	e := r.Extract
	fmt.Fprintln(w, "extract scorecard")
	fmt.Fprintf(w, "  total             %d\n", e.Total)
	fmt.Fprintf(w, "  extract-calls     %d\n", e.ExtractCalls)
	fmt.Fprintf(w, "  extract-call-rate %.4f  (soft, no threshold)\n", e.ExtractCallRate)
	fmt.Fprintf(w, "  overall           precision %.4f  recall %.4f  f1 %.4f  accuracy %.4f\n",
		e.Overall.Precision, e.Overall.Recall, e.Overall.F1, e.Overall.Accuracy)

	// Per-class lines in the fixed AllExtractLabels order. Each label is
	// single-polarity: detail's recall is the fraction extracted (the recall-safety
	// number the false-drop guard protects); hub-index/residue's accuracy is the
	// fraction correctly skipped (the shed rate). Extracted+skipped are surfaced so
	// the leak counts read directly.
	for _, label := range bench.AllExtractLabels {
		c, ok := e.ByClass[label]
		if !ok || c.Total == 0 {
			continue
		}
		if label == bench.ExtractDetail {
			// detail: all gold-positive, so extracted = TP, skipped = FN (false-drops).
			fmt.Fprintf(w, "  %-10s recall %.4f  (n=%d, extracted %d, skipped %d)\n",
				label, c.Recall, c.Total, c.TP, c.FN)
			continue
		}
		// hub-index / residue: all gold-negative, so extracted = FP (leaks),
		// skipped = TN (correctly shed).
		fmt.Fprintf(w, "  %-10s accuracy %.4f  (n=%d, skipped %d, leaked %d)\n",
			label, c.Accuracy, c.Total, c.TN, c.FP)
	}

	fmt.Fprintf(w, "  residue-count     %d\n", e.ResidueCount)
	fmt.Fprintf(w, "  residue-extracted %d\n", e.ResidueExtracted)

	// Leaks print plainly to w (never red): a non-posting the gate extracted is a
	// descriptive finding the reject rungs (#115) will target, not a regression.
	for _, url := range e.Leaks {
		fmt.Fprintf(w, "  leak              %s (gate extracted a non-posting -- descriptive)\n", url)
	}

	for _, url := range e.FalseDrops {
		fmt.Fprintln(os.Stderr, red("FALSE-DROP  "+url+" (real single-posting detail rejected by the extract gate)"))
	}
}
