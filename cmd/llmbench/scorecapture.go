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

	report := bench.ScoreExtract(verdictRows(rows))
	// The stream estimates are computed over the RANDOM stratum alone: it is the only
	// drawing sampled at random from the stream, so it is the only one whose weights
	// map back to a population rather than to a structural design (ADR-0043, #262).
	if stream := verdictRowsIn(rows, stratumRandom); len(stream) > 0 {
		s := bench.ScoreExtractStream(stream)
		report.Stream = &s
	}
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

// captureRow is one replayed gold-set row: the scored verdict row plus the stratum
// it was drawn from, which selects the subset the stream estimates are computed
// over. The verdict row is EMBEDDED rather than wrapped so every reader keeps
// reaching straight through to URL / Label / Extract.
type captureRow struct {
	bench.ExtractVerdictRow
	Stratum goldStratum
}

// verdictRows projects the replayed rows onto what ScoreExtract folds.
func verdictRows(rows []captureRow) []bench.ExtractVerdictRow {
	out := make([]bench.ExtractVerdictRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ExtractVerdictRow)
	}
	return out
}

// verdictRowsIn projects the replayed rows of one stratum, which is how the
// weighted stream scorecard is scoped to a single drawing: weights normalize within
// a drawing and are meaningless pooled across two.
func verdictRowsIn(rows []captureRow, s goldStratum) []bench.ExtractVerdictRow {
	out := []bench.ExtractVerdictRow{}
	for _, row := range rows {
		if row.Stratum == s {
			out = append(out, row.ExtractVerdictRow)
		}
	}
	return out
}

// replayCaptured reads the labeled capture file and replays the Extract Gate over
// each captured *Content (parser.Parse is skipped -- the content is already
// parsed and carries every field the gate reads). Returns the scored rows, the
// count of skipped unlabeled lines, and any read/parse error. A line with an
// invalid label is skipped (not fatal) so an operator can label incrementally.
//
// Lines decode as goldRow, the Extract Gold Set row: a superset of the tap's own
// record, so a raw capture and a committed, labelled, weighted gold-set file both
// score through this one path (ADR-0043). The GATE still sees only url and content:
// the stratum and weight ride alongside the decision for the scorer's benefit and
// never reach pagegate.ShouldExtract.
func replayCaptured(path string, cfg crawler.LLMGateConfig) (rows []captureRow, skipped int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	rows = []captureRow{}
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
		var rec goldRow
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
		rows = append(rows, captureRow{
			ExtractVerdictRow: bench.ExtractVerdictRow{
				URL:     rec.URL,
				Label:   rec.Label,
				Extract: pagegate.ShouldExtract(u, &rec.Content, cfg),
				Weight:  rec.Weight,
			},
			Stratum: rec.Stratum,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, skipped, fmt.Errorf("read %q: %w", path, err)
	}
	return rows, skipped, nil
}
