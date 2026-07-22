package main

// synonym is one hand-owned supplement entry: an alias GeoNames + ISO 3166 will
// not themselves yield, mapped to an ISO 3166-1 alpha-2 country code. kind is
// kindCountry (an endonym, abbreviation, or common English synonym) or kindCity
// (an English city exonym the ASCII name won't produce). Keys are written in
// natural spelling; build runs each through normalizeKey before storing.
type synonym struct {
	kind string
	key  string
	code string
}

// supplement is the small hand table that complements the generated data. It
// carries only aliases the reference files lack — country endonyms and
// abbreviations (GeoNames already supplies the English names), and English city
// exonyms whose ASCII name differs (GeoNames yields "Muenchen"/"Roma"/"Praha",
// so "munich"/"rome"/"prague" are added here). It ports the current hand
// tables' non-demonym entries so #173's swap does not regress the resolver
// tests.
//
// No demonyms appear here by construction: nothing in the pipeline emits them,
// which is what keeps "german" and the like out of the artifact. Bare "us",
// "america", and "american" are also deliberately omitted (ADR-0028) — "us" is
// the English pronoun and "america" names the continents, so as country
// synonyms they would false-drop non-US listings; bare "US" is matched
// separately by #173's case- and delimiter-aware pass.
var supplement = []synonym{
	// --- Country endonyms, abbreviations, and English synonyms (kindCountry) ---
	// DACH.
	{kindCountry, "germany", "DE"}, {kindCountry, "deutschland", "DE"},
	{kindCountry, "austria", "AT"}, {kindCountry, "österreich", "AT"}, {kindCountry, "oesterreich", "AT"},
	{kindCountry, "switzerland", "CH"}, {kindCountry, "schweiz", "CH"}, {kindCountry, "suisse", "CH"}, {kindCountry, "svizzera", "CH"},

	// Western & Southern Europe.
	{kindCountry, "france", "FR"},
	{kindCountry, "spain", "ES"}, {kindCountry, "españa", "ES"}, {kindCountry, "espana", "ES"},
	{kindCountry, "italy", "IT"}, {kindCountry, "italia", "IT"},
	{kindCountry, "portugal", "PT"},
	{kindCountry, "netherlands", "NL"}, {kindCountry, "holland", "NL"}, {kindCountry, "nederland", "NL"},
	{kindCountry, "belgium", "BE"}, {kindCountry, "belgië", "BE"}, {kindCountry, "belgie", "BE"}, {kindCountry, "belgique", "BE"},
	{kindCountry, "luxembourg", "LU"}, {kindCountry, "luxemburg", "LU"},
	{kindCountry, "ireland", "IE"}, {kindCountry, "éire", "IE"}, {kindCountry, "eire", "IE"},
	{kindCountry, "united kingdom", "GB"}, {kindCountry, "uk", "GB"}, {kindCountry, "britain", "GB"},
	{kindCountry, "great britain", "GB"}, {kindCountry, "england", "GB"}, {kindCountry, "scotland", "GB"}, {kindCountry, "wales", "GB"},
	{kindCountry, "greece", "GR"}, {kindCountry, "hellas", "GR"},
	{kindCountry, "malta", "MT"},
	{kindCountry, "cyprus", "CY"},

	// Northern Europe.
	{kindCountry, "sweden", "SE"}, {kindCountry, "sverige", "SE"},
	{kindCountry, "norway", "NO"}, {kindCountry, "norge", "NO"},
	{kindCountry, "denmark", "DK"}, {kindCountry, "danmark", "DK"},
	{kindCountry, "finland", "FI"}, {kindCountry, "suomi", "FI"},
	{kindCountry, "iceland", "IS"},
	{kindCountry, "estonia", "EE"}, {kindCountry, "eesti", "EE"},
	{kindCountry, "latvia", "LV"}, {kindCountry, "latvija", "LV"},
	{kindCountry, "lithuania", "LT"}, {kindCountry, "lietuva", "LT"},

	// Central & Eastern Europe.
	{kindCountry, "poland", "PL"}, {kindCountry, "polska", "PL"},
	{kindCountry, "czechia", "CZ"}, {kindCountry, "czech republic", "CZ"}, {kindCountry, "česko", "CZ"}, {kindCountry, "cesko", "CZ"},
	{kindCountry, "slovakia", "SK"}, {kindCountry, "slovensko", "SK"},
	{kindCountry, "hungary", "HU"}, {kindCountry, "magyarország", "HU"}, {kindCountry, "magyarorszag", "HU"},
	{kindCountry, "romania", "RO"}, {kindCountry, "românia", "RO"},
	{kindCountry, "bulgaria", "BG"},
	{kindCountry, "slovenia", "SI"}, {kindCountry, "slovenija", "SI"},
	{kindCountry, "croatia", "HR"}, {kindCountry, "hrvatska", "HR"},
	{kindCountry, "serbia", "RS"}, {kindCountry, "srbija", "RS"},
	{kindCountry, "ukraine", "UA"},
	{kindCountry, "russia", "RU"}, {kindCountry, "russian federation", "RU"},

	// Americas. Bare "us"/"america"/"american" deliberately omitted (see above).
	{kindCountry, "united states", "US"}, {kindCountry, "united states of america", "US"}, {kindCountry, "usa", "US"},
	{kindCountry, "canada", "CA"},
	{kindCountry, "mexico", "MX"}, {kindCountry, "méxico", "MX"},
	{kindCountry, "brazil", "BR"}, {kindCountry, "brasil", "BR"},
	{kindCountry, "argentina", "AR"},
	{kindCountry, "chile", "CL"},
	{kindCountry, "colombia", "CO"},
	{kindCountry, "peru", "PE"}, {kindCountry, "perú", "PE"},

	// Asia-Pacific.
	{kindCountry, "china", "CN"},
	{kindCountry, "japan", "JP"}, {kindCountry, "nippon", "JP"},
	{kindCountry, "south korea", "KR"}, {kindCountry, "korea", "KR"}, {kindCountry, "republic of korea", "KR"},
	{kindCountry, "north korea", "KP"},
	{kindCountry, "india", "IN"},
	{kindCountry, "singapore", "SG"},
	{kindCountry, "hong kong", "HK"},
	{kindCountry, "taiwan", "TW"},
	{kindCountry, "indonesia", "ID"},
	{kindCountry, "malaysia", "MY"},
	{kindCountry, "thailand", "TH"},
	{kindCountry, "philippines", "PH"},
	{kindCountry, "vietnam", "VN"}, {kindCountry, "viet nam", "VN"},
	{kindCountry, "australia", "AU"},
	{kindCountry, "new zealand", "NZ"}, {kindCountry, "aotearoa", "NZ"},

	// Middle East & Africa.
	{kindCountry, "united arab emirates", "AE"}, {kindCountry, "uae", "AE"},
	{kindCountry, "saudi arabia", "SA"},
	{kindCountry, "israel", "IL"},
	{kindCountry, "turkey", "TR"}, {kindCountry, "türkiye", "TR"}, {kindCountry, "turkiye", "TR"},
	{kindCountry, "egypt", "EG"},
	{kindCountry, "south africa", "ZA"},
	{kindCountry, "nigeria", "NG"},
	{kindCountry, "kenya", "KE"},

	// --- English city exonyms (kindCity) whose GeoNames ASCII name differs ---
	// GeoNames yields the endonym ASCII form (Muenchen, Koeln, Wien, Roma,
	// Praha, Warszawa, ...); these curated exonyms complement it and, where the
	// raw dominance would place the exonym elsewhere (e.g. "rome" the US towns),
	// take precedence in the city layer.
	{kindCity, "munich", "DE"}, {kindCity, "cologne", "DE"},
	{kindCity, "vienna", "AT"},
	{kindCity, "geneva", "CH"},
	{kindCity, "rome", "IT"}, {kindCity, "milan", "IT"}, {kindCity, "turin", "IT"},
	{kindCity, "prague", "CZ"},
	{kindCity, "warsaw", "PL"}, {kindCity, "cracow", "PL"},
	{kindCity, "lisbon", "PT"},
	{kindCity, "gothenburg", "SE"},
	{kindCity, "copenhagen", "DK"},
	{kindCity, "brussels", "BE"},
	{kindCity, "the hague", "NL"},
}
