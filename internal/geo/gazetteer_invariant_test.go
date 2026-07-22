package geo_test

import (
	"cmp"
	"os"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

// TestGazetteerDataInvariants is the full-gazetteer data-invariant test (spec #171
// acceptance) that matcher_test.go defers to it. It reads the raw, committed
// gazetteer.tsv from disk — not the parsed in-memory tables — so it can assert row
// order, and holds every property the resolver silently relies on: every code is a
// real ISO code, no key maps to two different countries, rows are strictly sorted
// in the generator's (kind, key) order, and the anchor cities resolve end-to-end.
// A botched regeneration (invalid code, conflicting duplicate, lost sort, dropped
// city) fails here rather than silently degrading Resolve.
func TestGazetteerDataInvariants(t *testing.T) {
	data, err := os.ReadFile("gazetteer.tsv")
	if err != nil {
		t.Fatalf("read gazetteer.tsv (run from the package dir): %v", err)
	}

	// Parse exactly as internal/geo/gazetteer.go's parseGazetteer does — skip the
	// '#'-comment header and blank lines, split on tabs, require three fields — so
	// the invariants are measured over the same rows the resolver actually sees.
	type row struct{ kind, key, code string }
	rows := []row{}
	keyToCode := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		r := row{fields[0], fields[1], fields[2]}
		switch r.kind {
		case "country", "state", "city":
		default:
			continue
		}
		rows = append(rows, r)

		// No key may map to two different countries. Keys are globally unique across
		// layers (ADR-0031), so a recurring key with a different code is a
		// regeneration bug.
		if prev, ok := keyToCode[r.key]; ok && prev != r.code {
			t.Errorf("key %q maps to two countries: %q and %q", r.key, prev, r.code)
		}
		keyToCode[r.key] = r.code

		// Every code must be a real, officially-assigned ISO 3166-1 alpha-2 code.
		if !geo.Valid(r.code) {
			t.Errorf("row %s/%q has invalid ISO code %q", r.kind, r.key, r.code)
		}
	}

	// An empty or truncated file must fail loudly, not pass vacuously.
	if len(rows) < 1000 {
		t.Fatalf("gazetteer has %d data rows, want > 1000 (file truncated?)", len(rows))
	}

	// Rows are strictly increasing in the generator's (kind, key) order
	// (build.go: cmp.Or(cmp.Compare(a.kind,b.kind), cmp.Compare(a.key,b.key))).
	// Strict because globally-unique keys make an equal-adjacent pair a duplicate.
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1], rows[i]
		if cmp.Or(cmp.Compare(prev.kind, cur.kind), cmp.Compare(prev.key, cur.key)) >= 0 {
			t.Errorf("rows not strictly sorted at %d: (%s,%q) not before (%s,%q)",
				i, prev.kind, prev.key, cur.kind, cur.key)
		}
	}

	// Anchor cities must be present with the right country, both in the raw data
	// and end-to-end through the embedded resolver path.
	for _, a := range []struct{ key, code string }{{"berlin", "DE"}, {"paris", "FR"}} {
		if got := keyToCode[a.key]; got != a.code {
			t.Errorf("anchor key %q maps to %q, want %q", a.key, got, a.code)
		}
	}
	for _, a := range []struct{ in, want string }{{"Berlin", "DE"}, {"Paris", "FR"}} {
		if got := geo.Resolve(a.in); got != a.want {
			t.Errorf("Resolve(%q) = %q, want %q", a.in, got, a.want)
		}
	}
}
