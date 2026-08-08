package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/freeextraction"
)

// This file is the Free Extraction fidelity check's replay layer (ADR-0042 /
// ADR-0043, #256). It drives the REAL freeextraction.Extractor over the committed
// Extract Gold Set with a stub in place of the model, so a delegated page is
// observable as "would have been an LLM call" with no network, no model and no
// Docker. bench.ScoreFreeExtraction folds the resulting rows; nothing here scores.

// modelStub stands in for the model-backed extractor: it counts the pages the Free
// Extraction handed on and returns an abstain. A page the stub never saw is a page
// no LLM call was made for -- the observation the whole guard rests on, made
// without trusting the extraction's own Free marker.
type modelStub struct{ calls int }

func (s *modelStub) Extract(context.Context, crawler.RawJobListing) (crawler.Extraction, error) {
	s.calls++
	return crawler.Extraction{}, nil
}

// runScoreFree replays the Free Extraction over the Extract Gold Set at -in
// (defaulting to the committed file) and prints the fidelity scorecard. Exit: 1
// when the check fails (a fire on a page that is not a posting, a field diverging
// from its expected value, or an expectation that is unscored or unused), 2 on a
// wiring error, 0 otherwise.
func runScoreFree(args []string) int {
	fs := flag.NewFlagSet("score-free", flag.ExitOnError)
	in := fs.String("in", filepath.Join(defaultGoldSetDir, goldSetFile), "Extract Gold Set JSONL to replay the Free Extraction over")
	jsonOut := fs.Bool("json", false, "emit the free-extraction scorecard as JSON to stdout instead of the human-readable report")
	_ = fs.Parse(args)

	rows, skipped, outOfScope, err := replayFreeExtraction(context.Background(), *in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench score-free: %v\n", err)
		return 2
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench score-free: no labeled rows in %s (skipped %d unlabeled)\n", *in, skipped)
		return 2
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "note: scored %d labeled rows, skipped %d unlabeled\n", len(rows), skipped)
	}
	if outOfScope > 0 {
		fmt.Fprintf(os.Stderr, "note: skipped %d rows outside the #256 drawing (only the structural drawing carries expected extractions)\n", outOfScope)
	}

	report := bench.ScoreFreeExtraction(rows)
	if *jsonOut {
		if err := bench.EncodeFreeExtractionReport(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench score-free: encode json: %v\n", err)
			return 2
		}
	} else {
		printFreeReport(os.Stdout, report)
	}
	if report.Failed() {
		return 1
	}
	return 0
}

// replayFreeExtraction reads the gold set at path and replays the REAL
// freeextraction.Extractor over every labelled row, observing from a per-row model
// stub whether the page was served free. It is the shared body of the score-free
// verb and its committed-set regression test, so both drive the identical
// mechanism. Rows with no valid label are skipped and counted, so an unlabeled raw
// capture replays too.
//
// Rows outside the #256 DRAWING are skipped and counted separately. The #256 ground
// truth was proposed over the structural drawing alone, and a fired row carrying no
// expected extraction is FATAL, so replaying a later drawing here would turn the
// guard red for rows nobody ever proposed an expectation for. Extending that ground
// truth is #256's business, not this replay's. A row with an empty stratum still
// replays, so a raw capture file scores as before.
//
// A row whose stub observation disagrees with the extraction's own Free marker is
// an ERROR, not a score: the two must agree by construction, and a disagreement is
// a wiring fault in the mechanism the guard cannot meaningfully measure around.
func replayFreeExtraction(ctx context.Context, path string) (rows []bench.FreeExtractionRow, skipped, outOfScope int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	rows = []bench.FreeExtractionRow{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), captureScanBuffer)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec goldRow
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, skipped, outOfScope, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if rec.Stratum != "" && rec.Stratum.Drawing() != drawingStructural {
			outOfScope++
			continue
		}
		if !rec.Label.Valid() {
			skipped++
			continue
		}
		u, err := crawler.NewURL(rec.URL)
		if err != nil {
			return nil, skipped, outOfScope, fmt.Errorf("line %d: url %q: %w", lineNo, rec.URL, err)
		}

		// A fresh stub per row, so calls == 0 means THIS page was served free.
		stub := &modelStub{}
		got, err := freeextraction.NewExtractor(stub).Extract(ctx, crawler.RawJobListing{URL: u, Content: rec.Content})
		if err != nil {
			return nil, skipped, outOfScope, fmt.Errorf("extract %q: %w", rec.URL, err)
		}
		free := stub.calls == 0
		if free != got.Free {
			return nil, skipped, outOfScope, fmt.Errorf("extract %q: the extraction's Free marker is %v but the model stub was called %d times; the mechanism disagrees with itself", rec.URL, got.Free, stub.calls)
		}

		row := bench.FreeExtractionRow{
			URL:    rec.URL,
			Label:  rec.Label,
			Weight: rec.Weight,
			Free:   free,
			Got: bench.FreeExtractionFields{
				Title:           got.Listing.Title,
				Location:        got.Listing.Location,
				WorkArrangement: string(got.Listing.WorkArrangement),
			},
		}
		if rec.Expected != nil {
			row.Want = &bench.FreeExtractionFields{
				Title:           rec.Expected.Title,
				Location:        rec.Expected.Location,
				WorkArrangement: rec.Expected.WorkArrangement,
			}
			row.AcceptedFire = rec.Expected.FreeOK
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, outOfScope, fmt.Errorf("read %q: %w", path, err)
	}
	return rows, skipped, outOfScope, nil
}

// printFreeReport writes the free-extraction scorecard: the descriptive summary
// (totals, the soft rates, and the per-row accepted exceptions) to w, and every
// fatal line to stderr in ANSI red, so a Free Extraction that saved a non-posting
// or mis-read a field stands out on the terminal. Mirrors printExtractReport.
func printFreeReport(w io.Writer, r bench.FreeExtractionReport) {
	f := r.Free
	fmt.Fprintln(w, "free-extraction scorecard")
	fmt.Fprintf(w, "  total             %d\n", f.Total)
	fmt.Fprintf(w, "  free              %d\n", f.Free)
	fmt.Fprintf(w, "  free-rate         %.4f  (soft, no threshold)\n", f.FreeRate)
	fmt.Fprintf(w, "  stream-free-share %.4f  (soft, sampling-weighted, no threshold)\n", f.StreamFreeShare)
	fmt.Fprintf(w, "  detail            %d (free %d)\n", f.DetailTotal, f.DetailFree)
	fmt.Fprintf(w, "  coverage          %.4f  (soft, no threshold)\n", f.Coverage)
	fmt.Fprintf(w, "  weighted-coverage %.4f  (soft, sampling-weighted, no threshold)\n", f.WeightedCoverage)

	// Accepted fires print plainly to w (never red): they are argued, per-row
	// exceptions a human owns, kept visible but deliberately non-fatal.
	for _, url := range f.AcceptedFires {
		fmt.Fprintf(w, "  accepted-fire     %s (residue, human-accepted exception, descriptive)\n", url)
	}

	for _, url := range f.FiredOnNonPosting {
		fmt.Fprintln(os.Stderr, red("FIRED-ON-NON-POSTING "+url+" (a Free Extraction saved a page that is not a posting, with no model in the loop)"))
	}
	for _, d := range f.FieldDivergences {
		fmt.Fprintln(os.Stderr, red(fmt.Sprintf("FIELD-DIVERGENCE     %s [%s] want %q got %q", d.URL, d.Field, d.Want, d.Got)))
	}
	for _, url := range f.MissingExpectation {
		fmt.Fprintln(os.Stderr, red("MISSING-EXPECTATION  "+url+" (fired with no confirmed expected extraction)"))
	}
	for _, url := range f.UnusedExpectation {
		fmt.Fprintln(os.Stderr, red("UNUSED-EXPECTATION   "+url+" (carries an expected extraction but the Free Extraction did not fire)"))
	}
}
