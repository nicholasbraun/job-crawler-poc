package main

import (
	"strings"
	"testing"
)

// cityLine builds a 19-field GeoNames geoname row, placing the three fields the
// parser reads (asciiname=2, countryCode=8, population=14) and leaving the rest
// blank — so the test pins the indices, not an incidental layout.
func cityLine(asciiname, countryCode, population string) string {
	f := make([]string, 19)
	f[2] = asciiname
	f[8] = countryCode
	f[14] = population
	return strings.Join(f, "\t")
}

func TestParseCities5000(t *testing.T) {
	data := strings.Join([]string{
		cityLine("Berlin", "DE", "3644826"),
		cityLine("Springfield", "US", ""), // blank population -> 0
		cityLine("", "FR", "1000"),        // blank asciiname -> dropped
		cityLine("Nowhere", "", "1000"),   // blank country -> dropped
		"# a comment line",
		"",               // blank line
		"too\tfew\tcols", // under-length -> dropped
	}, "\n")

	got := parseCities5000([]byte(data))
	want := []cityRow{
		{name: "Berlin", countryCode: "DE", population: 3644826},
		{name: "Springfield", countryCode: "US", population: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("parseCities5000 returned %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestParseCountryInfo(t *testing.T) {
	data := strings.Join([]string{
		"# ISO\tISO3\t...header...",
		"#",
		"DE\tDEU\t276\tGM\tGermany\tBerlin",
		"GE\tGEO\t268\tGG\tGeorgia\tTbilisi",
		"\tXXX\t000\t\t\tNowhere",                  // blank code -> dropped
		"AN\tANT\t530\tNT\tNetherlands Antilles\t", // withdrawn code still parsed; build drops it
	}, "\n")

	got := parseCountryInfo([]byte(data))
	want := []countryRow{
		{code: "DE", name: "Germany"},
		{code: "GE", name: "Georgia"},
		{code: "AN", name: "Netherlands Antilles"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseCountryInfo returned %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestParseAdmin1US(t *testing.T) {
	data := strings.Join([]string{
		"US.TX\tTexas\tTexas\t4736286",
		"US.NY\tNew York\tNew York\t5128638",
		"DE.BY\tBavaria\tBavaria\t2951839", // non-US -> dropped
		"CA.ON\tOntario\tOntario\t6093822", // non-US -> dropped
		"# comment",
	}, "\n")

	got := parseAdmin1US([]byte(data))
	want := []admin1Row{
		{code: "TX", name: "Texas"},
		{code: "NY", name: "New York"},
	}
	if len(got) != len(want) {
		t.Fatalf("parseAdmin1US returned %d rows, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("row %d = %+v, want %+v", i, got[i], w)
		}
	}
}
