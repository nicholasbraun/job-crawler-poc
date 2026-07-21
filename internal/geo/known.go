package geo

import "sort"

// Country pairs an ISO 3166-1 alpha-2 code with its canonical English display name.
type Country struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// knownCountries is the curated set the Resolver can place by name — deliberately
// NOT the full ISO validation set (validCodes), so the dashboard offers only
// Countries a Country Constraint can actually match, each with a display name. Every
// entry's Code must be Valid and its Name must Resolve back to that Code; the
// TestKnownCountries sync-check enforces this against the gazetteer (ADR-0028/0029).
var knownCountries = []Country{
	// DACH.
	{"DE", "Germany"},
	{"AT", "Austria"},
	{"CH", "Switzerland"},

	// Western & Southern Europe.
	{"FR", "France"},
	{"ES", "Spain"},
	{"IT", "Italy"},
	{"PT", "Portugal"},
	{"NL", "Netherlands"},
	{"BE", "Belgium"},
	{"LU", "Luxembourg"},
	{"IE", "Ireland"},
	{"GB", "United Kingdom"},
	{"GR", "Greece"},
	{"MT", "Malta"},
	{"CY", "Cyprus"},

	// Northern Europe.
	{"SE", "Sweden"},
	{"NO", "Norway"},
	{"DK", "Denmark"},
	{"FI", "Finland"},
	{"IS", "Iceland"},
	{"EE", "Estonia"},
	{"LV", "Latvia"},
	{"LT", "Lithuania"},

	// Central & Eastern Europe.
	{"PL", "Poland"},
	{"CZ", "Czechia"},
	{"SK", "Slovakia"},
	{"HU", "Hungary"},
	{"RO", "Romania"},
	{"BG", "Bulgaria"},
	{"SI", "Slovenia"},
	{"HR", "Croatia"},
	{"RS", "Serbia"},
	{"UA", "Ukraine"},
	{"RU", "Russia"},

	// Americas.
	{"US", "United States"},
	{"CA", "Canada"},
	{"MX", "Mexico"},
	{"BR", "Brazil"},
	{"AR", "Argentina"},
	{"CL", "Chile"},
	{"CO", "Colombia"},
	{"PE", "Peru"},

	// Asia-Pacific.
	{"CN", "China"},
	{"JP", "Japan"},
	{"KR", "South Korea"},
	{"IN", "India"},
	{"SG", "Singapore"},
	{"HK", "Hong Kong"},
	{"TW", "Taiwan"},
	{"ID", "Indonesia"},
	{"MY", "Malaysia"},
	{"TH", "Thailand"},
	{"PH", "Philippines"},
	{"VN", "Vietnam"},
	{"AU", "Australia"},
	{"NZ", "New Zealand"},

	// Middle East & Africa.
	{"AE", "United Arab Emirates"},
	{"SA", "Saudi Arabia"},
	{"IL", "Israel"},
	{"TR", "Turkey"},
	{"EG", "Egypt"},
	{"ZA", "South Africa"},
	{"NG", "Nigeria"},
	{"KE", "Kenya"},

	// Georgia the country (GE), resolvable as a standalone token (ADR-0029).
	{"GE", "Georgia"},
}

// KnownCountries returns the curated Countries the Resolver can place by name,
// sorted by Name, backing the definition-defaults known-country set so the
// dashboard multi-select isn't hardcoded (ADR-0028/0029). The returned slice is a
// fresh copy the caller may sort or mutate without affecting the package data.
func KnownCountries() []Country {
	out := make([]Country, len(knownCountries))
	copy(out, knownCountries)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
