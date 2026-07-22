package main

import (
	"strconv"
	"strings"
)

// The reference files are the GeoNames tab-separated dumps plus the ISO 3166
// countryInfo table. Field indices below are fixed by those published layouts;
// parse_test.go guards the easy-to-get-wrong ones against synthetic rows.

// cityRow is one populated place from GeoNames cities5000: its ASCII name, its
// UTF-8 primary name (used only to recover an umlaut alias key — see build's
// cityKeys), the ISO 3166-1 alpha-2 country it sits in, and its population
// (0 when unknown).
type cityRow struct {
	name        string
	altName     string
	countryCode string
	population  int64
}

// countryRow is one ISO 3166-1 country: alpha-2 code and English short name.
type countryRow struct {
	code string
	name string
}

// admin1Row is one US state (a GeoNames US.* admin1 division): the two-letter
// state code and its English name.
type admin1Row struct {
	code string
	name string
}

// parseCities5000 reads the GeoNames "geoname" table layout (19 tab-separated
// fields, no header). It reads asciiname (field 2) as the primary key source
// because asciiname is guaranteed ASCII, sidestepping non-Latin scripts, and
// also the UTF-8 name (field 1) as altName: build folds the latter into an alias
// key so umlaut endonyms whose asciiname spells "ue" ("Duesseldorf") still match
// the runtime fold that maps ü->u ("dusseldorf"). Rows with a blank asciiname or
// country code are dropped; a blank population parses to 0.
func parseCities5000(data []byte) []cityRow {
	const (
		fieldName        = 1
		fieldAsciiName   = 2
		fieldCountryCode = 8
		fieldPopulation  = 14
		minFields        = 15
	)
	rows := []cityRow{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < minFields {
			continue
		}
		name := strings.TrimSpace(f[fieldAsciiName])
		altName := strings.TrimSpace(f[fieldName])
		cc := strings.TrimSpace(f[fieldCountryCode])
		if name == "" || cc == "" {
			continue
		}
		pop, _ := strconv.ParseInt(strings.TrimSpace(f[fieldPopulation]), 10, 64)
		rows = append(rows, cityRow{name: name, altName: altName, countryCode: cc, population: pop})
	}
	return rows
}

// parseCountryInfo reads the ISO 3166 countryInfo table: alpha-2 code (field 0)
// and English country name (field 4). Its ~50 leading '#' comment lines and any
// blank lines are skipped. All rows are returned, including codes GeoNames
// carries that ISO no longer assigns (AN, CS, XK); build applies the unassigned
// blocklist so only officially assigned codes reach the artifact.
func parseCountryInfo(data []byte) []countryRow {
	const (
		fieldCode = 0
		fieldName = 4
		minFields = 5
	)
	rows := []countryRow{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < minFields {
			continue
		}
		code := strings.TrimSpace(f[fieldCode])
		name := strings.TrimSpace(f[fieldName])
		if code == "" || name == "" {
			continue
		}
		rows = append(rows, countryRow{code: code, name: name})
	}
	return rows
}

// parseAdmin1US reads GeoNames admin1CodesASCII and keeps only US divisions:
// rows whose field-0 key is "US.<CC>". The state code is the part after the dot
// (e.g. "US.TX" -> "TX"); the name is field 1. Non-US rows are ignored.
func parseAdmin1US(data []byte) []admin1Row {
	const (
		fieldKey  = 0
		fieldName = 1
		minFields = 2
		prefix    = "US."
	)
	rows := []admin1Row{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < minFields {
			continue
		}
		key := strings.TrimSpace(f[fieldKey])
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		code := strings.TrimSpace(strings.TrimPrefix(key, prefix))
		name := strings.TrimSpace(f[fieldName])
		if code == "" || name == "" {
			continue
		}
		rows = append(rows, admin1Row{code: code, name: name})
	}
	return rows
}
