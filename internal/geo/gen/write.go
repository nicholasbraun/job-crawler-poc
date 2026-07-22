package main

import (
	"bufio"
	"fmt"
	"io"
)

// pinnedSnapshotDate is the download date of the vendored GeoNames + ISO 3166
// reference files (they carry no version). It is the sole date in the artifact —
// a generation timestamp would break byte-identical regeneration.
const pinnedSnapshotDate = "2026-07-22"

// header is the '#'-prefixed comment block written above the rows. It stamps the
// CC BY 4.0 attribution to GeoNames the license requires, records the pinned
// snapshot and the regenerate command, and documents the column layout. #173's
// parser skips '#' lines, so the header is inert data to the resolver.
var header = []string{
	"# Country Resolver gazetteer — GENERATED, do not edit by hand.",
	"# Regenerate with: go generate ./internal/geo/gen",
	"#",
	"# Source data: GeoNames (cities5000, countryInfo, admin1CodesASCII) + ISO 3166,",
	"# plus a small hand synonym supplement. Pinned snapshot: " + pinnedSnapshotDate + ".",
	"# GeoNames data is licensed CC BY 4.0, © GeoNames (https://www.geonames.org/).",
	"#",
	"# Columns: kind\tkey\tcode  (kind ∈ {country, state, city}; code is ISO 3166-1 alpha-2).",
}

// writeTSV serializes the header block and one "kind\tkey\tcode" line per entry,
// in the order given (build already sorts). It is deterministic: the same
// entries always yield byte-identical output.
func writeTSV(w io.Writer, entries []entry) error {
	bw := bufio.NewWriter(w)
	for _, line := range header {
		if _, err := fmt.Fprintln(bw, line); err != nil {
			return fmt.Errorf("write header: %w", err)
		}
	}
	for _, e := range entries {
		if _, err := fmt.Fprintf(bw, "%s\t%s\t%s\n", e.kind, e.key, e.code); err != nil {
			return fmt.Errorf("write entry %q: %w", e.key, err)
		}
	}
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}
