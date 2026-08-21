package parser_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

const (
	// goldFixturesDir is the classifier Gold Set's page directory, relative to this
	// package (go test runs with the package directory as the working directory).
	goldFixturesDir = "../../cmd/llmbench/testdata/pages"

	goldenPath = "testdata/flattened_text.golden.jsonl"

	// goldenMinBytes is a tripwire, not an expectation: today the artefact records
	// 526159 bytes of Flattened Text over 90 fixtures, so a regeneration that quietly
	// emptied it — a parser returning "" everywhere — would slip past every other
	// check in this file, which only ever compares the artefact against itself.
	goldenMinBytes = 400_000
)

var updateGolden = flag.Bool("update", false,
	"rewrite testdata/flattened_text.golden.jsonl from today's parser output; "+
		"legitimate ONLY when the committed fixture set changed -- never to make a failing round-trip go green (ADR-0046)")

// goldenRow is one fixture's frozen Flattened Text. Neither field carries
// omitempty: five fixtures legitimately flatten to "" (JS-rendered pages the
// crawler never executes), and dropping them would silently shrink the invariant.
type goldenRow struct {
	Fixture       string `json:"fixture"`
	FlattenedText string `json:"flattened_text"`
}

// TestFlattenedTextGolden freezes the Flattened Text every content consumer reads
// today — the Posting Body, the search index, the Extract Gate's phrase marks — over
// the committed classifier Gold Set fixtures, and asserts the parser still produces
// it byte for byte (#277, spec #275).
//
// Byte equality, not similarity, because SourceHash is persisted on ~46k job_listing
// rows and a different value there silently re-extracts the whole Corpus (ADR-0046,
// internal/source_hash.go). The artefact is frozen BEFORE the parser moves, because
// "what it used to say" cannot be reconstructed from memory afterwards.
//
// #279 turns this same artefact into the round-trip assertion ADR-0046 rests on:
//
//	FlattenedText(StructuralRendering(html)) == golden
//
// Regenerate with:
//
//	go test ./internal/parser/ -run TestFlattenedTextGolden -update
//
// That is legitimate only when the committed fixture set itself changed. A failing
// round-trip is a real break in the derivation, and ADR-0046's stated fallback is
// #275's two-field design — never a regenerated golden.
func TestFlattenedTextGolden(t *testing.T) {
	names := goldenFixtureNames(t)

	if *updateGolden {
		rows := make([]goldenRow, 0, len(names))
		for _, name := range names {
			rows = append(rows, goldenRow{Fixture: name, FlattenedText: parseFixtureFlattenedText(t, name)})
		}
		logGoldenDeltas(t, rows)
		writeGoldenRows(t, rows)
	}

	golden := readGoldenRows(t)

	have := map[string]bool{}
	for _, name := range names {
		have[name] = true
		if _, ok := golden[name]; !ok {
			t.Errorf("fixture %s has no golden row -- regenerate with: go test ./internal/parser/ -run TestFlattenedTextGolden -update", name)
		}
	}
	for name := range golden {
		if !have[name] {
			t.Errorf("golden row %s has no fixture file in %s -- regenerate with: go test ./internal/parser/ -run TestFlattenedTextGolden -update", name, goldFixturesDir)
		}
	}

	total := 0
	for _, text := range golden {
		total += len(text)
	}
	if total < goldenMinBytes {
		t.Errorf("golden artefact records %d bytes of Flattened Text over %d rows, below the goldenMinBytes tripwire of %d (it held 526159 bytes over 90 fixtures when frozen) -- the parser or the regeneration lost content",
			total, len(golden), goldenMinBytes)
	}

	for _, name := range names {
		want, ok := golden[name]
		if !ok {
			continue // already reported above
		}
		t.Run(name, func(t *testing.T) {
			got := parseFixtureFlattenedText(t, name)
			if got == want {
				return
			}
			offset, gotWindow, wantWindow := firstDiff(got, want)
			t.Errorf("Flattened Text changed for %s: got %d bytes, golden has %d, first difference at byte %d\n got: %s\nwant: %s\n"+
				"this is the ADR-0046 round-trip invariant reporting a real break: fix the derivation, never the golden",
				name, len(got), len(want), offset, gotWindow, wantWindow)
		})
	}
}

// goldenFixtureNames lists the committed classifier fixtures by file name, sorted.
// The directory is read rather than manifest.json so a page added without a manifest
// entry still falls under the invariant.
func goldenFixtureNames(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(goldFixturesDir)
	if err != nil {
		t.Fatalf("read fixture dir %s: %v", goldFixturesDir, err)
	}
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".html") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("no .html fixtures in %s -- an empty set would make this test vacuous", goldFixturesDir)
	}
	sort.Strings(names)
	return names
}

// parseFixtureFlattenedText returns the fixture's Flattened Text exactly as the
// production parser produces it. Nothing here trims, re-spaces or re-encodes it:
// words running together across block boundaries are today's real output and are
// precisely what the invariant pins.
func parseFixtureFlattenedText(t *testing.T, name string) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(goldFixturesDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	content, err := parser.NewHTMLParser().Parse(b)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return content.MainContent
}

// readGoldenRows loads the artefact keyed by fixture name. It streams with a
// json.Decoder rather than a bufio.Scanner: the largest row is ~170 KB, well past
// the Scanner's default 64 KB token limit.
func readGoldenRows(t *testing.T) map[string]string {
	t.Helper()

	f, err := os.Open(goldenPath)
	if err != nil {
		t.Fatalf("open %s: %v -- regenerate with: go test ./internal/parser/ -run TestFlattenedTextGolden -update", goldenPath, err)
	}
	defer f.Close()

	rows := map[string]string{}
	dec := json.NewDecoder(f)
	for {
		var row goldenRow
		if err := dec.Decode(&row); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode %s after %d rows: %v", goldenPath, len(rows), err)
		}
		if row.Fixture == "" {
			t.Fatalf("%s has a row with an empty fixture name after %d rows", goldenPath, len(rows))
		}
		if _, dup := rows[row.Fixture]; dup {
			t.Fatalf("%s records fixture %s twice", goldenPath, row.Fixture)
		}
		rows[row.Fixture] = row.FlattenedText
	}
	if len(rows) == 0 {
		t.Fatalf("%s is empty", goldenPath)
	}
	return rows
}

// writeGoldenRows rewrites the artefact as JSONL, one row per line in the given
// order. HTML escaping is off so the file stays reviewable — page text is full of
// <, > and & — and Encode's trailing newline per row is what makes it JSONL.
func writeGoldenRows(t *testing.T, rows []goldenRow) {
	t.Helper()

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, row := range rows {
		if err := enc.Encode(row); err != nil {
			t.Fatalf("encode golden row %s: %v", row.Fixture, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		t.Fatalf("create %s: %v", filepath.Dir(goldenPath), err)
	}
	if err := os.WriteFile(goldenPath, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", goldenPath, err)
	}
	t.Logf("wrote %s: %d rows", goldenPath, len(rows))
}

// logGoldenDeltas reports what a regeneration is about to change, so whoever runs
// -update sees the blast radius instead of a mute rewrite.
func logGoldenDeltas(t *testing.T, rows []goldenRow) {
	t.Helper()

	if _, err := os.Stat(goldenPath); err != nil {
		return // first generation: nothing to compare against
	}
	old := readGoldenRows(t)
	fresh := map[string]bool{}
	for _, row := range rows {
		fresh[row.Fixture] = true
		prev, ok := old[row.Fixture]
		switch {
		case !ok:
			t.Logf("-update: %s added (%d bytes)", row.Fixture, len(row.FlattenedText))
		case prev != row.FlattenedText:
			t.Logf("-update: %s changed (%d -> %d bytes)", row.Fixture, len(prev), len(row.FlattenedText))
		}
	}
	for name := range old {
		if !fresh[name] {
			t.Logf("-update: %s removed", name)
		}
	}
}

// firstDiff reports the first byte offset at which got and want differ, plus a
// bounded window of each around it, so a failing fixture prints where it moved
// rather than 170 KB of page text.
func firstDiff(got, want string) (offset int, gotWindow, wantWindow string) {
	n := min(len(got), len(want))
	offset = n
	for i := 0; i < n; i++ {
		if got[i] != want[i] {
			offset = i
			break
		}
	}
	return offset, window(got, offset), window(want, offset)
}

// window renders ~120 bytes of s around offset, quoted and clamped to the bounds.
func window(s string, offset int) string {
	start := max(offset-40, 0)
	end := min(start+120, len(s))
	if start >= end {
		return fmt.Sprintf("<no bytes at offset %d, length %d>", offset, len(s))
	}
	return fmt.Sprintf("%q", s[start:end])
}
