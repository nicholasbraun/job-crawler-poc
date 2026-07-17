// The diff verb reads two saved -json Reports (A then B) and prints, for every
// scalar metric, B relative to A, plus the gate violations added and removed. It
// is a comparison tool: it never inspects Failed() and always exits 0 on success
// (2 only on a usage, open, or decode error). The pure delta math lives in
// bench.Diff; this file is only its presentation.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// runDiff parses two report paths, decodes each, and renders bench.Diff(a, b).
// Returns 0 on success, 2 on a usage, open, or decode error.
func runDiff(args []string) int {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	_ = fs.Parse(args)

	if fs.NArg() != 2 {
		fmt.Fprintln(os.Stderr, "usage: llmbench diff <a.json> <b.json>")
		return 2
	}

	a, err := readReport(fs.Arg(0))
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench diff: %v\n", err)
		return 2
	}
	b, err := readReport(fs.Arg(1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench diff: %v\n", err)
		return 2
	}

	renderDiff(os.Stdout, fs.Arg(0), fs.Arg(1), bench.Diff(a, b))
	return 0
}

// readReport opens path and decodes one JSON Report (as written by bench -json).
func readReport(path string) (bench.Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return bench.Report{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	rep, err := bench.DecodeReport(f)
	if err != nil {
		return bench.Report{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return rep, nil
}

// renderDiff writes the metric deltas (B relative to A) followed by the gate
// violations added in B and removed from A. Presentation only -- untested,
// mirroring the printReport split.
func renderDiff(w io.Writer, aName, bName string, d bench.ReportDiff) {
	fmt.Fprintf(w, "diff %s -> %s\n", aName, bName)
	for _, m := range d.Metrics {
		fmt.Fprintf(w, "  %-32s %+.4f  (%.4f -> %.4f)\n", m.Name, m.Delta, m.A, m.B)
	}
	for _, v := range d.ViolationsAdded {
		fmt.Fprintf(w, "  +violation %s [%s] want %s got %s\n", v.URL, v.Category, v.Want, v.Got)
	}
	for _, v := range d.ViolationsRemoved {
		fmt.Fprintf(w, "  -violation %s [%s] want %s got %s\n", v.URL, v.Category, v.Want, v.Got)
	}
}
