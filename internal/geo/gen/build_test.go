package main

import (
	"reflect"
	"testing"
)

// baseCountries is the synthetic ISO set shared by the build tests. It supplies
// every code the cases assign to (so validCountry admits them) and every code
// whose collision the state layer must honor (so isoCodes bars DE/CA/CO/IN/GA).
func baseCountries() []countryRow {
	return []countryRow{
		{"DE", "Germany"}, {"US", "United States"}, {"AT", "Austria"},
		{"CR", "Costa Rica"}, {"GE", "Georgia"}, {"GA", "Gabon"},
		{"MC", "Monaco"}, {"CA", "Canada"}, {"CO", "Colombia"},
		{"IN", "India"}, {"FR", "France"}, {"IT", "Italy"},
	}
}

// codeOf returns the code an (kind, key) entry maps to, and whether it exists.
func codeOf(entries []entry, kind, key string) (string, bool) {
	for _, e := range entries {
		if e.kind == kind && e.key == key {
			return e.code, true
		}
	}
	return "", false
}

func TestBuildCityDominance(t *testing.T) {
	cities := []cityRow{
		// Single country -> assigned, no floor (recovers the DACH long-tail).
		{name: "Walldorf", countryCode: "DE", population: 15000},
		// Dominant -> assigned to the top country.
		{name: "Berlin", countryCode: "DE", population: 3644826},
		{name: "Berlin", countryCode: "US", population: 20000},
		// Ratio too low (~2.9x < 8) -> dropped.
		{name: "San Jose", countryCode: "US", population: 1000000},
		{name: "San Jose", countryCode: "CR", population: 340000},
		// Ratio met (10x) but top below floor (50k < 100k) -> dropped.
		{name: "Smalltown", countryCode: "DE", population: 50000},
		{name: "Smalltown", countryCode: "AT", population: 5000},
		// Equal population, both above floor -> ratio 1 < 8 -> dropped.
		{name: "Bigtwin", countryCode: "DE", population: 200000},
		{name: "Bigtwin", countryCode: "FR", population: 200000},
	}
	got := build(defaultPolicy, baseCountries(), cities, nil, nil)

	if code, ok := codeOf(got, kindCity, "walldorf"); !ok || code != "DE" {
		t.Errorf("walldorf: got (%q,%v), want (DE,true) — single country needs no floor", code, ok)
	}
	if code, ok := codeOf(got, kindCity, "berlin"); !ok || code != "DE" {
		t.Errorf("berlin: got (%q,%v), want (DE,true)", code, ok)
	}
	for _, key := range []string{"san jose", "smalltown", "bigtwin"} {
		if code, ok := codeOf(got, kindCity, key); ok {
			t.Errorf("%s: got (%q,true), want dropped (keep-on-doubt)", key, code)
		}
	}
}

func TestBuildCityStoplist(t *testing.T) {
	// Obscure GeoNames towns shadow region/aggregate words; the city stoplist bars
	// the region words so a region-only location stays at the empty Country, while
	// a real city (Berlin) is unaffected.
	cities := []cityRow{
		{name: "America", countryCode: "AR", population: 5000},
		{name: "Asia", countryCode: "PH", population: 5000},
		{name: "Eu", countryCode: "FR", population: 5000},
		{name: "Berlin", countryCode: "DE", population: 3644826},
	}
	got := build(defaultPolicy, baseCountries(), cities, nil, nil)

	for _, key := range []string{"america", "asia", "eu"} {
		if code, ok := codeOf(got, kindCity, key); ok {
			t.Errorf("%s: got (%q,true), want barred by city stoplist (region word)", key, code)
		}
	}
	if code, ok := codeOf(got, kindCity, "berlin"); !ok || code != "DE" {
		t.Errorf("berlin: got (%q,%v), want (DE,true) — a real city is unaffected by the stoplist", code, ok)
	}
}

func TestBuildStateLayer(t *testing.T) {
	states := []admin1Row{
		{"TX", "Texas"}, {"NY", "New York"},
		{"CA", "California"}, // ca collides with Canada -> code out, name in
		{"DE", "Delaware"},   // de collides with Germany -> code out, name in
		{"CO", "Colorado"},   // co collides with Colombia -> code out, name in
		{"IN", "Indiana"},    // in collides with India -> code out, name in
		{"OR", "Oregon"},     // or is stoplisted -> code out, name in
		{"GA", "Georgia"},    // ga collides with Gabon (code) AND name is a country -> both out
	}
	got := build(defaultPolicy, baseCountries(), nil, states, nil)

	admittedCodes := map[string]bool{"tx": true, "ny": true}
	for code := range admittedCodes {
		if c, ok := codeOf(got, kindState, code); !ok || c != "US" {
			t.Errorf("state code %q: got (%q,%v), want (US,true)", code, c, ok)
		}
	}
	for _, code := range []string{"ca", "de", "co", "in", "or", "ga"} {
		if c, ok := codeOf(got, kindState, code); ok {
			t.Errorf("state code %q: got (%q,true), want excluded", code, c)
		}
	}
	// Full state names of ISO/stoplist-excluded codes still resolve via the name.
	for _, name := range []string{"california", "delaware", "colorado", "indiana", "oregon", "texas"} {
		if c, ok := codeOf(got, kindState, name); !ok || c != "US" {
			t.Errorf("state name %q: got (%q,%v), want (US,true)", name, c, ok)
		}
	}
	// The subdivision name equal to a country name stays in the country layer.
	if c, ok := codeOf(got, kindState, "georgia"); ok {
		t.Errorf("georgia in state layer: got (%q,true), want excluded (country reading wins)", c)
	}
	if c, ok := codeOf(got, kindCountry, "georgia"); !ok || c != "GE" {
		t.Errorf("georgia in country layer: got (%q,%v), want (GE,true)", c, ok)
	}
}

func TestBuildCrossLayerPrecedence(t *testing.T) {
	// Monaco is both a country and a city; it must appear once, in the country layer.
	cities := []cityRow{{name: "Monaco", countryCode: "MC", population: 38000}}
	got := build(defaultPolicy, baseCountries(), cities, nil, nil)

	count := 0
	for _, e := range got {
		if e.key == "monaco" {
			count++
			if e.kind != kindCountry || e.code != "MC" {
				t.Errorf("monaco entry = %+v, want {country monaco MC}", e)
			}
		}
	}
	if count != 1 {
		t.Errorf("monaco appears %d times, want exactly 1 (country layer suppresses the city)", count)
	}
}

func TestBuildSupplementCityOverridesDominance(t *testing.T) {
	// GeoNames yields "rome" only for the US towns; the curated exonym reassigns
	// it to Italy, and the supplement must win over the raw dominance result.
	cities := []cityRow{{name: "Rome", countryCode: "US", population: 36000}}
	syn := []synonym{{kindCity, "rome", "IT"}}
	got := build(defaultPolicy, baseCountries(), cities, nil, syn)

	if code, ok := codeOf(got, kindCity, "rome"); !ok || code != "IT" {
		t.Errorf("rome: got (%q,%v), want (IT,true) — curated exonym overrides", code, ok)
	}
}

func TestBuildDemonymsAbsent(t *testing.T) {
	// The supplement carries the English name and an endonym but no demonym; the
	// pipeline emits neither "german" nor "germans".
	syn := []synonym{
		{kindCountry, "Germany", "DE"},
		{kindCountry, "Deutschland", "DE"},
	}
	got := build(defaultPolicy, baseCountries(), nil, nil, syn)

	for _, key := range []string{"germany", "deutschland"} {
		if code, ok := codeOf(got, kindCountry, key); !ok || code != "DE" {
			t.Errorf("%s: got (%q,%v), want (DE,true)", key, code, ok)
		}
	}
	for _, e := range got {
		if e.key == "german" || e.key == "germans" {
			t.Errorf("demonym leaked into artifact: %+v", e)
		}
	}
}

func TestBuildDeterministicAndSorted(t *testing.T) {
	countries := baseCountries()
	cities := []cityRow{
		{name: "Berlin", countryCode: "DE", population: 3644826},
		{name: "Paris", countryCode: "FR", population: 2138551},
	}
	states := []admin1Row{{"TX", "Texas"}, {"NY", "New York"}}
	syn := []synonym{{kindCountry, "usa", "US"}, {kindCity, "munich", "DE"}}

	first := build(defaultPolicy, countries, cities, states, syn)
	second := build(defaultPolicy, countries, cities, states, syn)
	if !reflect.DeepEqual(first, second) {
		t.Error("build is not deterministic: two calls on identical input differ")
	}

	for i := 1; i < len(first); i++ {
		prev, cur := first[i-1], first[i]
		if prev.kind > cur.kind || (prev.kind == cur.kind && prev.key > cur.key) {
			t.Errorf("not sorted by (kind,key): %+v precedes %+v", prev, cur)
		}
	}
}
