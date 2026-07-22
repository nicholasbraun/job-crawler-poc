package main

import (
	"cmp"
	"log"
	"slices"

	"github.com/nicholasbraun/job-crawler-poc/internal/geo"
)

// The three gazetteer layers, in descending precedence. A lookup key is emitted
// in the highest layer it qualifies for and suppressed from every lower one, so
// the resolver reads exactly one country per key.
const (
	kindCountry = "country"
	kindState   = "state"
	kindCity    = "city"
)

// entry is one emitted gazetteer row: a folded lookup key in a layer (kind)
// mapping to an ISO 3166-1 alpha-2 country code.
type entry struct {
	kind string
	key  string
	code string
}

// policy holds the ADR-0031 keep-on-doubt dominance thresholds and the two
// stoplists. Thresholds are integers so the transform is float-free and
// byte-identical across runs.
type policy struct {
	floor    int64               // min top-country max-population for a contested city name
	ratio    int64               // top must beat the runner-up by this factor
	stoplist map[string]struct{} // folded 2-letter English words barred as US state codes
	cityStop map[string]struct{} // folded region/aggregate words barred from the city layer
}

var defaultPolicy = policy{
	floor: 100_000,
	ratio: 8,
	// English words that are also admittable US state codes; barred so a stray
	// "or"/"hi"/"ok"/"oh" token never resolves a listing to the US. Codes that
	// collide with a foreign ISO code (DE, CA, IN, ME, ...) are already excluded
	// by the iso check and need no stoplist entry.
	stoplist: map[string]struct{}{
		"or": {}, "hi": {}, "ok": {}, "oh": {},
	},
	// Region/continent/HR-aggregate words are not places: an obscure GeoNames
	// town happens to carry the name ("america"->AR, "eu"->FR, "apac"->UG), so
	// without this bar the city layer would resolve a region-only location to a
	// single country and — via the Country Constraint — false-drop the rest of
	// the region, inverting keep-on-doubt (ADR-0028/0029). Barring them here
	// mirrors how region words are already kept out of the country layer, so a
	// region-only string lands at the empty Country. Also covers the common
	// English words ("man", "to") that shadow obscure towns.
	cityStop: map[string]struct{}{
		"america": {}, "americas": {}, "asia": {}, "apac": {},
		"eu": {}, "europe": {}, "africa": {}, "oceania": {}, "antarctica": {},
		"emea": {}, "emeia": {}, "mena": {}, "latam": {},
		"benelux": {}, "nordics": {}, "nordic": {}, "scandinavia": {},
		"dach": {}, "anz": {}, "man": {}, "to": {},
		// "union" shadows a US town, so without this bar "European Union" and a
		// bare "Union" resolve to US (the city layer's rightmost match) and the
		// Country Constraint then false-drops EU-wide listings. Barring the bare
		// key keeps the region reading; compound keys ("union city") are unaffected.
		"union": {},
	},
}

// unassigned are codes GeoNames countryInfo carries that ISO 3166-1 no longer
// (or never) officially assigns, so geo.Valid rejects them; they are dropped so
// every emitted code is Valid. AN = Netherlands Antilles (withdrawn 2010),
// CS = Serbia and Montenegro (withdrawn 2006), XK = Kosovo (user-assigned).
var unassigned = map[string]struct{}{"AN": {}, "CS": {}, "XK": {}}

// build applies the ADR-0031 keep-on-doubt policy to the reference rows and the
// hand supplement, returning gazetteer entries sorted by (kind, key). It is
// deterministic: integer-only comparisons and a total order over globally unique
// keys. Layer precedence country > state > city is baked into the data here, so
// the emitted table needs no runtime precedence logic. It is otherwise
// side-effect free, but aborts via log.Fatalf if the reference data is internally
// inconsistent — a single country or city-synonym key claimed by two different
// codes — since such data cannot yield a table the resolver can trust.
func build(p policy, countries []countryRow, cities []cityRow, states []admin1Row, syn []synonym) []entry {
	// --- Country layer: ISO names plus the supplement's country synonyms. ---
	countryMap := map[string]string{}
	addCountry := func(key, code string) {
		if key == "" {
			return
		}
		if prev, ok := countryMap[key]; ok && prev != code {
			// One country key claimed by two codes means ambiguous data; fail
			// loudly rather than emit a key the resolver cannot trust.
			log.Fatalf("gen: country key %q claimed by both %s and %s", key, prev, code)
		}
		countryMap[key] = code
	}
	validCountry := map[string]struct{}{}
	for _, c := range countries {
		if _, bad := unassigned[c.code]; bad {
			continue
		}
		validCountry[c.code] = struct{}{}
		addCountry(geo.NormalizeKey(c.name), c.code)
	}
	for _, s := range syn {
		if s.kind == kindCountry {
			addCountry(geo.NormalizeKey(s.key), s.code)
		}
	}

	// isoCodes is the foreign-collision set for state codes: every ISO alpha-2
	// code (folded), so a US state code equal to one (DE, CA, CO, IN, GA, ...)
	// is barred and resolves via city or full state name instead. Built from the
	// data, not a hardcoded ISO list.
	isoCodes := map[string]struct{}{}
	for _, c := range countries {
		isoCodes[geo.Fold(c.code)] = struct{}{}
	}

	// --- State layer: US state codes and names, minus country-key collisions. ---
	stateMap := map[string]string{}
	for _, st := range states {
		if cc := geo.Fold(st.code); isTwoLetter(cc) {
			_, iso := isoCodes[cc]
			_, stop := p.stoplist[cc]
			_, country := countryMap[cc]
			if !iso && !stop && !country {
				stateMap[cc] = "US"
			}
		}
		// A subdivision name equal to a country name/synonym ("georgia") stays in
		// the country layer; only distinct names enter the state layer.
		if nk := geo.NormalizeKey(st.name); nk != "" {
			if _, country := countryMap[nk]; !country {
				stateMap[nk] = "US"
			}
		}
	}

	// --- City layer: population dominance, minus higher-layer collisions. ---
	// Group each name's per-country max population, over valid-country cities and
	// only for keys not already owned by the country or state layer.
	groups := map[string]map[string]int64{}
	for _, city := range cities {
		if _, ok := validCountry[city.countryCode]; !ok {
			continue
		}
		for _, key := range cityKeys(city) {
			if key == "" {
				continue
			}
			if _, stop := p.cityStop[key]; stop {
				continue
			}
			if _, country := countryMap[key]; country {
				continue
			}
			if _, state := stateMap[key]; state {
				continue
			}
			g := groups[key]
			if g == nil {
				g = map[string]int64{}
				groups[key] = g
			}
			if city.population > g[city.countryCode] {
				g[city.countryCode] = city.population
			}
		}
	}
	cityMap := map[string]string{}
	for key, g := range groups {
		if code, ok := dominant(p, g); ok {
			cityMap[key] = code
		}
	}
	// Supplement city exonyms take precedence over the raw dominance result
	// (curated intent wins among cities) but never over a country or state key.
	// synCity tracks keys claimed BY the supplement so two supplement entries
	// disagreeing on a key fail loudly (symmetric with addCountry), while the
	// intended override of a dominance-derived code stays silent.
	synCity := map[string]string{}
	for _, s := range syn {
		if s.kind != kindCity {
			continue
		}
		key := geo.NormalizeKey(s.key)
		if key == "" {
			continue
		}
		if _, stop := p.cityStop[key]; stop {
			continue
		}
		if _, country := countryMap[key]; country {
			continue
		}
		if _, state := stateMap[key]; state {
			continue
		}
		if prev, ok := synCity[key]; ok && prev != s.code {
			log.Fatalf("gen: city synonym key %q claimed by both %s and %s", key, prev, s.code)
		}
		synCity[key] = s.code
		cityMap[key] = s.code
	}

	// --- Emit, sorted by (kind, key). Keys are globally unique across layers. ---
	entries := make([]entry, 0, len(countryMap)+len(stateMap)+len(cityMap))
	for k, c := range countryMap {
		entries = append(entries, entry{kindCountry, k, c})
	}
	for k, c := range stateMap {
		entries = append(entries, entry{kindState, k, c})
	}
	for k, c := range cityMap {
		entries = append(entries, entry{kindCity, k, c})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.key, b.key))
	})
	return entries
}

// dominant applies the keep-on-doubt population rule to one name's per-country
// max populations. A name in exactly one country is assigned to it with no floor
// (recovering the long-tail of small European towns). A name in several is
// assigned to the top country only when the runner-up population is known and
// positive AND the top clears the floor AND beats the runner-up by the ratio;
// otherwise the name is dropped to the empty Country. The runner-up positivity
// guard matters: with a runner-up of 0 (unknown population) the ratio test
// "top >= ratio*0" is vacuously true, so the name would be assigned on the floor
// alone — the one path that can place it in the wrong country. The
// (population desc, code asc) ordering makes every tie deterministic; an exactly
// equal top pair fails the ratio (ratio > 1) and is dropped.
func dominant(p policy, pop map[string]int64) (string, bool) {
	type countryPop struct {
		code string
		pop  int64
	}
	ranked := make([]countryPop, 0, len(pop))
	for code, n := range pop {
		ranked = append(ranked, countryPop{code, n})
	}
	if len(ranked) == 0 {
		return "", false
	}
	slices.SortFunc(ranked, func(a, b countryPop) int {
		return cmp.Or(cmp.Compare(b.pop, a.pop), cmp.Compare(a.code, b.code))
	})
	if len(ranked) == 1 {
		return ranked[0].code, true
	}
	top, runner := ranked[0], ranked[1]
	if runner.pop > 0 && top.pop >= p.floor && top.pop >= p.ratio*runner.pop {
		return top.code, true
	}
	return "", false
}

// cityKeys returns the distinct lookup keys a city contributes: its asciiname
// key always, plus an alias derived from the UTF-8 primary name when the two
// differ. The alias closes the umlaut recall gap — GeoNames' asciiname spells ü
// as "ue" ("Duesseldorf") while the runtime fold maps ü->u ("dusseldorf"), so
// without the primary-name alias the endonym input never matches the stored key.
// The alias is an ordinary city key: build feeds it through the same stoplist,
// higher-layer suppression, and keep-on-doubt dominance, so an ambiguous umlaut
// name shared across countries still drops to the empty Country.
func cityKeys(c cityRow) []string {
	ascii := geo.NormalizeKey(c.name)
	alias := geo.NormalizeKey(c.altName)
	if alias == "" || alias == ascii {
		return []string{ascii}
	}
	return []string{ascii, alias}
}

// isTwoLetter reports whether s is exactly two ASCII lowercase letters — the
// shape of a folded US state code eligible for the abbreviation layer.
func isTwoLetter(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}
