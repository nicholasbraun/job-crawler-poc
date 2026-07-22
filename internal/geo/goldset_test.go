package geo_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

// The gold-set (testdata/goldset.json) is a committed, hand-labelled sample of
// real crawl locations that proves the Country Resolver fix (spec #171): US
// states/cities and the bare-US token now resolve, while clearly-DACH strings are
// never mis-assigned to a wrong country (ADR-0028/0029/0031). It is the static
// regression guard that keeps a future gazetteer regeneration from reintroducing
// a false-drop.
//
// Provenance: the distinct location set of the named Germany crawl (definition
// 8af979d1-4163-42b9-b802-6532e205c37f, run 49f8db23..., Postgres container
// job-crawler-poc-postgres-1, sampled 2026-07-22), supplemented with equivalent
// real strings drawn from other definitions in the same database to represent the
// region and genuinely-ambiguous categories the Germany crawl's own strings do
// not contain. Every row is real crawl data; the `prev` field records the country
// each string was stored with at sample time (the pre-fix resolution), which
// powers the US-coverage metric.

// goldRow is one hand-labelled gold-set entry. All fields are strings so the
// fixture stays diffable and lossless for location text carrying pipes, umlauts,
// and delimiters.
type goldRow struct {
	// Location is the raw job_listing.location, verbatim.
	Location string `json:"location"`
	// Expected is the hand-labelled correct ISO code; "" means genuinely
	// unresolvable / keep-on-doubt (for ambiguous rows, no single defensible code).
	Expected string `json:"expected"`
	// Category selects which assertion applies: us, dach, remote, region,
	// ambiguous, or other.
	Category string `json:"category"`
	// Prev is the country the row was stored with in the DB at sample time.
	Prev string `json:"prev"`
	// Note is a short human rationale for the label (required for ambiguous rows).
	Note string `json:"note"`
}

// loadGoldSet reads and parses the committed gold-set fixture. The working
// directory under `go test` is the package directory, so the relative testdata
// path resolves without configuration.
func loadGoldSet(t *testing.T) []goldRow {
	t.Helper()
	data, err := os.ReadFile("testdata/goldset.json")
	if err != nil {
		t.Fatalf("read gold-set: %v", err)
	}
	var rows []goldRow
	if err := json.Unmarshal(data, &rows); err != nil {
		t.Fatalf("parse gold-set: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("gold-set is empty, want the curated sample")
	}
	return rows
}

// TestGoldSetNoFalseDrops is the hard gate (spec #171 acceptance): no clearly-DACH
// location may resolve to a wrong country. For each dach row, Resolve must return
// either the empty Country (keep-on-doubt) or the row's expected code — any other
// non-empty result is a false-drop that would let the Country Constraint discard a
// real DACH listing.
func TestGoldSetNoFalseDrops(t *testing.T) {
	for _, r := range loadGoldSet(t) {
		if r.Category != "dach" {
			continue
		}
		got := geo.Resolve(r.Location)
		if got != "" && got != r.Expected {
			t.Errorf("Resolve(%q) = %q, want %q or \"\" (DACH false-drop)", r.Location, got, r.Expected)
		}
	}
}

// TestGoldSetUSCoverage reports the fix's payoff as a metric rather than a gate
// (spec #171 acceptance): how many US strings resolve to US, and how many that
// were stored empty before the fix now resolve. It is report-only on purpose —
// gating a legitimately-US string that happens to contain another country token
// would be brittle, and the ticket asks for the number surfaced, not enforced.
// Run with `go test -v` to see the logged line.
func TestGoldSetUSCoverage(t *testing.T) {
	var usTotal, usResolved, prevEmpty, prevEmptyNowUS int
	for _, r := range loadGoldSet(t) {
		if r.Category != "us" {
			continue
		}
		usTotal++
		nowUS := geo.Resolve(r.Location) == "US"
		if nowUS {
			usResolved++
		}
		if r.Prev == "" {
			prevEmpty++
			if nowUS {
				prevEmptyNowUS++
			}
		}
	}
	t.Logf("US coverage: %d/%d US strings resolve to US; %d/%d previously-empty US strings now resolve to US",
		usResolved, usTotal, prevEmptyNowUS, prevEmpty)
}

// TestGoldSetRegionsKept asserts the keep-on-doubt contract over the sample's
// remote and region rows: a remote-work indicator or a geographic aggregate with
// no placeable country must resolve to the empty Country, never be assigned one.
func TestGoldSetRegionsKept(t *testing.T) {
	for _, r := range loadGoldSet(t) {
		if r.Category != "remote" && r.Category != "region" {
			continue
		}
		if got := geo.Resolve(r.Location); got != "" {
			t.Errorf("Resolve(%q) = %q, want \"\" (region/remote must be kept, not assigned)", r.Location, got)
		}
	}
}

// TestGoldSetOutputAlwaysValid is the safety property over the whole real sample:
// Resolve returns either a real ISO code or the empty Country for every row, so
// the Country Constraint can trust the output regardless of category. It mirrors
// the adversarial TestResolveOutputAlwaysValidOrEmpty but over real crawl strings,
// and logs the resolved value of ambiguous/other rows for the reviewer.
func TestGoldSetOutputAlwaysValid(t *testing.T) {
	for _, r := range loadGoldSet(t) {
		got := geo.Resolve(r.Location)
		if !geo.Valid(got) && got != "" {
			t.Errorf("Resolve(%q) = %q, which is neither Valid nor empty", r.Location, got)
		}
		if r.Category == "ambiguous" || r.Category == "other" {
			t.Logf("[%s] Resolve(%q) = %q", r.Category, r.Location, got)
		}
	}
}
