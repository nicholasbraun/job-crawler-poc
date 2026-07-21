package geo

import "strings"

// validCodes is the full set of officially assigned ISO 3166-1 alpha-2 codes —
// the authority for Valid. Reserved and user-assigned codes (EU, UK, SU, ...)
// are deliberately absent: they are not officially assigned.
var validCodes = toSet(`
AD AE AF AG AI AL AM AO AQ AR AS AT AU AW AX AZ
BA BB BD BE BF BG BH BI BJ BL BM BN BO BQ BR BS BT BV BW BY BZ
CA CC CD CF CG CH CI CK CL CM CN CO CR CU CV CW CX CY CZ
DE DJ DK DM DO DZ
EC EE EG EH ER ES ET
FI FJ FK FM FO FR
GA GB GD GE GF GG GH GI GL GM GN GP GQ GR GS GT GU GW GY
HK HM HN HR HT HU
ID IE IL IM IN IO IQ IR IS IT
JE JM JO JP
KE KG KH KI KM KN KP KR KW KY KZ
LA LB LC LI LK LR LS LT LU LV LY
MA MC MD ME MF MG MH MK ML MM MN MO MP MQ MR MS MT MU MV MW MX MY MZ
NA NC NE NF NG NI NL NO NP NR NU NZ
OM
PA PE PF PG PH PK PL PM PN PR PS PT PW PY
QA
RE RO RS RU RW
SA SB SC SD SE SG SH SI SJ SK SL SM SN SO SR SS ST SV SX SY SZ
TC TD TF TG TH TJ TK TL TM TN TO TR TT TV TW TZ
UA UG UM US UY UZ
VA VC VE VG VI VN VU
WF WS
YE YT
ZA ZM ZW`)

// countryByName maps a folded country name, synonym, or demonym to its ISO
// alpha-2 code. Region words (eu, europe, emea, dach, scandinavia) are
// deliberately excluded so a region-only string resolves to the empty Country.
var countryByName = buildNames()

// countryByCity is a small, deliberately partial city safety-net, weighted to
// the DACH region and major EU hubs the Discovery-seeded Catalog actually
// contains. A city outside it resolves to the empty Country and is kept — the
// safe under-filtering direction.
var countryByCity = buildCities()

// maxPhraseWords is the longest key, in words, across both gazetteers — the
// widest n-gram window Resolve needs to try.
var maxPhraseWords = computeMaxPhraseWords()

// toSet turns a whitespace-separated list of codes into a lookup set.
func toSet(codes string) map[string]struct{} {
	fields := strings.Fields(codes)
	m := make(map[string]struct{}, len(fields))
	for _, c := range fields {
		m[c] = struct{}{}
	}
	return m
}

// nameBuilder accumulates folded name -> code entries. Keys are folded on
// insertion so the curated data can be written in natural spelling
// ("Österreich") and still match folded lookups ("osterreich").
type nameBuilder struct{ m map[string]string }

func (b nameBuilder) add(code string, names ...string) {
	for _, n := range names {
		b.m[fold(n)] = code
	}
}

func computeMaxPhraseWords() int {
	max := 1
	for _, table := range []map[string]string{countryByName, countryByCity} {
		for k := range table {
			if w := len(strings.Fields(k)); w > max {
				max = w
			}
		}
	}
	return max
}

// buildNames curates country names, endonyms, common synonyms, and demonyms.
// Weighted to the DACH region and the EU the Catalog is seeded toward, with the
// major world economies covered for their names and synonyms.
func buildNames() map[string]string {
	b := nameBuilder{m: map[string]string{}}

	// --- DACH ---
	b.add("DE", "germany", "deutschland", "german", "germans")
	b.add("AT", "austria", "österreich", "oesterreich", "austrian")
	b.add("CH", "switzerland", "schweiz", "suisse", "svizzera", "swiss")

	// --- Western & Southern Europe ---
	b.add("FR", "france", "french")
	b.add("ES", "spain", "españa", "espana", "spanish")
	b.add("IT", "italy", "italia", "italian")
	b.add("PT", "portugal", "portuguese")
	b.add("NL", "netherlands", "holland", "nederland", "dutch")
	b.add("BE", "belgium", "belgië", "belgie", "belgique", "belgian")
	b.add("LU", "luxembourg", "luxemburg", "luxembourgish")
	b.add("IE", "ireland", "éire", "eire", "irish")
	b.add("GB", "united kingdom", "uk", "britain", "great britain", "british", "england", "scotland", "wales")
	b.add("GR", "greece", "hellas", "greek")
	b.add("MT", "malta", "maltese")
	b.add("CY", "cyprus", "cypriot")

	// --- Northern Europe ---
	b.add("SE", "sweden", "sverige", "swedish")
	b.add("NO", "norway", "norge", "norwegian")
	b.add("DK", "denmark", "danmark", "danish")
	b.add("FI", "finland", "suomi", "finnish")
	b.add("IS", "iceland", "icelandic") // endonym "ísland" omitted: folds to the English word "island"
	b.add("EE", "estonia", "eesti", "estonian")
	b.add("LV", "latvia", "latvija", "latvian")
	b.add("LT", "lithuania", "lietuva", "lithuanian")

	// --- Central & Eastern Europe ---
	b.add("PL", "poland", "polska", "polish")
	b.add("CZ", "czechia", "czech republic", "česko", "cesko", "czech")
	b.add("SK", "slovakia", "slovensko", "slovak")
	b.add("HU", "hungary", "magyarország", "magyarorszag", "hungarian")
	b.add("RO", "romania", "românia", "romanian")
	b.add("BG", "bulgaria", "bulgarian")
	b.add("SI", "slovenia", "slovenija", "slovenian")
	b.add("HR", "croatia", "hrvatska", "croatian")
	b.add("RS", "serbia", "srbija", "serbian")
	b.add("UA", "ukraine", "ukrainian")
	b.add("RU", "russia", "russian federation", "russian")

	// --- Americas ---
	// Bare "us"/"america"/"american" are deliberately omitted: "us" is the
	// ubiquitous English pronoun ("join us") and "america" also names the
	// continents ("Latin America"), so as country synonyms they mis-resolve
	// unrelated locations to US and — via the Country Constraint — false-drop
	// real non-US listings, inverting the keep-on-doubt invariant (ADR-0028).
	b.add("US", "united states", "united states of america", "usa")
	b.add("CA", "canada", "canadian")
	b.add("MX", "mexico", "méxico", "mexican")
	b.add("BR", "brazil", "brasil", "brazilian")
	b.add("AR", "argentina", "argentinian", "argentine")
	b.add("CL", "chile", "chilean")
	b.add("CO", "colombia", "colombian")
	b.add("PE", "peru", "perú", "peruvian")

	// --- Asia-Pacific ---
	b.add("CN", "china", "chinese")
	b.add("JP", "japan", "nippon", "japanese")
	b.add("KR", "south korea", "korea", "republic of korea", "korean")
	b.add("KP", "north korea") // two-word phrase wins over bare "korea" -> KR
	b.add("IN", "india", "indian")
	b.add("SG", "singapore", "singaporean")
	b.add("HK", "hong kong")
	b.add("TW", "taiwan", "taiwanese")
	b.add("ID", "indonesia", "indonesian")
	b.add("MY", "malaysia", "malaysian")
	b.add("TH", "thailand", "thai")
	b.add("PH", "philippines", "filipino")
	b.add("VN", "vietnam", "viet nam", "vietnamese")
	b.add("AU", "australia", "australian")
	b.add("NZ", "new zealand", "aotearoa")

	// --- Middle East & Africa ---
	b.add("AE", "united arab emirates", "uae", "emirati")
	b.add("SA", "saudi arabia", "saudi")
	b.add("IL", "israel", "israeli")
	b.add("TR", "turkey", "türkiye", "turkiye", "turkish")
	b.add("EG", "egypt", "egyptian")
	b.add("ZA", "south africa", "south african")
	b.add("NG", "nigeria", "nigerian")
	b.add("KE", "kenya", "kenyan")

	// --- The ambiguity trap: Georgia the country resolves to GE. A bare
	// "Georgia" is the country; "Atlanta, Georgia, USA" prefers the rightmost
	// country token (USA -> US), the US-state reading. ---
	b.add("GE", "georgia", "georgian")

	return b.m
}

// buildCities curates the city safety-net. Deliberately small and biased toward
// the DACH region and major EU hubs; a city outside it resolves to the empty
// Country and is kept. Alt-spellings that folding will not produce (English
// exonyms, ASCII transliterations) are listed explicitly.
func buildCities() map[string]string {
	b := nameBuilder{m: map[string]string{}}

	// --- Germany ---
	b.add("DE", "berlin", "münchen", "munich", "muenchen", "hamburg",
		"frankfurt", "köln", "koeln", "cologne", "stuttgart", "düsseldorf",
		"duesseldorf", "leipzig")

	// --- Austria ---
	b.add("AT", "wien", "vienna", "graz", "salzburg", "linz", "innsbruck")

	// --- Switzerland ---
	b.add("CH", "zürich", "zuerich", "zurich", "genève", "geneve", "geneva",
		"bern", "basel", "lausanne")

	// --- Rest of EU / major hubs ---
	b.add("FR", "paris", "lyon", "marseille", "toulouse")
	b.add("NL", "amsterdam", "rotterdam", "the hague", "den haag", "utrecht")
	b.add("ES", "madrid", "barcelona", "valencia")
	b.add("IT", "rome", "roma", "milan", "milano", "turin", "torino")
	b.add("GB", "london", "manchester", "edinburgh")
	b.add("IE", "dublin")
	b.add("BE", "brussels", "brussel", "bruxelles", "antwerp", "antwerpen")
	b.add("SE", "stockholm", "gothenburg", "göteborg", "goteborg")
	b.add("DK", "copenhagen", "københavn", "kobenhavn")
	b.add("PT", "lisbon", "lisboa", "porto")
	b.add("PL", "warsaw", "warszawa", "kraków", "krakow", "cracow")
	b.add("CZ", "prague", "praha")
	b.add("FI", "helsinki")
	b.add("NO", "oslo")

	return b.m
}
