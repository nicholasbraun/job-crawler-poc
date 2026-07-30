package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

// captureRecord is one line of the extract-capture JSONL (internal/extractcapture)
// after a human has added a `label`. content is the parsed page the live extractor
// and Extract Gate saw, so scoring replays pagegate.ShouldExtract against the exact
// bytes the pipeline produced -- no re-fetch, no drift. verdict is the extractor's
// own accept/abstain, kept for stratification but not used by the gate score.
type captureRecord struct {
	URL     string             `json:"url"`
	Verdict bool               `json:"verdict"`
	Label   bench.ExtractLabel `json:"label"`
	Content crawler.Content    `json:"content"`
}

// runScoreCapture replays the Extract Gate over a labeled extract-capture JSONL
// file (-in) and prints the same extract scorecard as the `extract` verb, but
// sourcing each row from a captured *Content instead of re-parsing an HTML
// fixture. Lines with no valid label are skipped (unlabeled captures), so a
// partially-labeled file scores only its labeled rows. Exit: 1 on any false-drop
// (a real detail page the gate skipped), 2 on a wiring error, 0 otherwise.
func runScoreCapture(args []string) int {
	fs := flag.NewFlagSet("score-capture", flag.ExitOnError)
	in := fs.String("in", "", "labeled extract-capture JSONL (one {url,label,content} per line)")
	gateConfig := fs.String("gate-config", "", "path to a JSON LLMGateConfig override applied on top of DefaultLLMGateConfig; empty uses DefaultLLMGateConfig")
	jsonOut := fs.Bool("json", false, "emit the extract scorecard as JSON to stdout instead of the human-readable report")
	_ = fs.Parse(args)

	if *in == "" {
		fmt.Fprintln(os.Stderr, "usage: llmbench score-capture -in <labeled.jsonl> [-gate-config f] [-json]")
		return 2
	}
	cfg, err := loadGateConfig(*gateConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench score-capture: load gate config: %v\n", err)
		return 2
	}

	rows, skipped, err := replayCaptured(*in, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "llmbench score-capture: %v\n", err)
		return 2
	}
	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "llmbench score-capture: no labeled rows in %s (skipped %d unlabeled)\n", *in, skipped)
		return 2
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "note: scored %d labeled rows, skipped %d unlabeled\n", len(rows), skipped)
	}

	report := bench.ScoreExtract(rows)
	if *jsonOut {
		if err := bench.EncodeExtractReport(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "llmbench score-capture: encode json: %v\n", err)
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

// replayCaptured reads the labeled capture file and replays the Extract Gate over
// each captured *Content (parser.Parse is skipped -- the content is already
// parsed and carries every field the gate reads). Returns the scored rows, the
// count of skipped unlabeled lines, and any read/parse error. A line with an
// invalid label is skipped (not fatal) so an operator can label incrementally.
func replayCaptured(path string, cfg crawler.LLMGateConfig) (rows []bench.ExtractVerdictRow, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	rows = []bench.ExtractVerdictRow{}
	sc := bufio.NewScanner(f)
	// Captured lines carry the full parsed MainContent, so raise the token cap well
	// above bufio's 64 KiB default.
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		if len(sc.Bytes()) == 0 {
			continue
		}
		var rec captureRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, skipped, fmt.Errorf("line %d: %w", lineNo, err)
		}
		if !rec.Label.Valid() {
			skipped++
			continue
		}
		u, err := crawler.NewURL(rec.URL)
		if err != nil {
			return nil, skipped, fmt.Errorf("line %d: url %q: %w", lineNo, rec.URL, err)
		}
		rows = append(rows, bench.ExtractVerdictRow{
			URL:     rec.URL,
			Label:   rec.Label,
			Extract: pagegate.ShouldExtract(u, &rec.Content, cfg),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, fmt.Errorf("read %q: %w", path, err)
	}
	return rows, skipped, nil
}
