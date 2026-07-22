package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestWriteTSVHeader(t *testing.T) {
	var buf bytes.Buffer
	if err := writeTSV(&buf, nil); err != nil {
		t.Fatalf("writeTSV: %v", err)
	}
	out := buf.String()
	lower := strings.ToLower(out)

	for _, want := range []string{"cc by 4.0", "geonames", pinnedSnapshotDate} {
		if !strings.Contains(lower, strings.ToLower(want)) {
			t.Errorf("header missing %q", want)
		}
	}
	// Column legend, so a reader (and #173's parser author) knows the layout.
	if !strings.Contains(lower, "kind") || !strings.Contains(lower, "code") {
		t.Errorf("header missing the column legend")
	}
	// Every header line is a comment the resolver's parser skips.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			t.Errorf("non-comment line in header-only output: %q", line)
		}
	}
	// The pinned snapshot date is the ONLY date; no generation timestamp may
	// leak, or regeneration would stop being byte-identical.
	dates := regexp.MustCompile(`\d{4}-\d{2}-\d{2}`).FindAllString(out, -1)
	for _, d := range dates {
		if d != pinnedSnapshotDate {
			t.Errorf("unexpected date %q in output (only the pin %q is allowed)", d, pinnedSnapshotDate)
		}
	}
}

func TestWriteTSVRowsAndDeterminism(t *testing.T) {
	entries := []entry{
		{kindCity, "berlin", "DE"},
		{kindCountry, "germany", "DE"},
		{kindState, "tx", "US"},
	}
	var a, b bytes.Buffer
	if err := writeTSV(&a, entries); err != nil {
		t.Fatalf("writeTSV a: %v", err)
	}
	if err := writeTSV(&b, entries); err != nil {
		t.Fatalf("writeTSV b: %v", err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Error("writeTSV is not deterministic: same entries produced different bytes")
	}
	for _, want := range []string{"city\tberlin\tDE", "country\tgermany\tDE", "state\ttx\tUS"} {
		if !strings.Contains(a.String(), want) {
			t.Errorf("output missing row %q", want)
		}
	}
}
