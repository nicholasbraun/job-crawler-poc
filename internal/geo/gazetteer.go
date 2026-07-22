package geo

import (
	_ "embed"
	"strings"
)

// gazetteerData is the generated lookup table (internal/geo/gen), embedded so the
// resolver carries no runtime data dependency. Its keys are already folded and
// normalized by the generator; the parser stores them verbatim (ADR-0029/0031).
//
//go:embed gazetteer.tsv
var gazetteerData []byte

// gaz is the parsed gazetteer, built once at init from the embedded artifact.
var gaz = parseGazetteer(gazetteerData)

// tables are the three precedence layers parsed from the embedded gazetteer, in
// descending precedence (country > state > city). Each maps a folded lookup key
// to an ISO 3166-1 alpha-2 code; a key is globally unique across layers, so the
// generator has already resolved precedence into the data. maxWords is the widest
// key in words across all layers — the n-gram window Resolve must try.
type tables struct {
	country  map[string]string
	state    map[string]string
	city     map[string]string
	maxWords int
}

// parseGazetteer reads the embedded "kind\tkey\tcode" rows into the three layers.
// It skips the '#'-comment header and blank lines, and defensively skips any row
// that is not exactly three fields or names an unknown kind. Keys are stored as
// written — the generator folded and normalized them, so re-folding here would
// risk drift with the lookup path.
func parseGazetteer(data []byte) tables {
	t := tables{
		country:  map[string]string{},
		state:    map[string]string{},
		city:     map[string]string{},
		maxWords: 1,
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		kind, key, code := fields[0], fields[1], fields[2]
		var layer map[string]string
		switch kind {
		case "country":
			layer = t.country
		case "state":
			layer = t.state
		case "city":
			layer = t.city
		default:
			continue
		}
		layer[key] = code
		if w := len(strings.Fields(key)); w > t.maxWords {
			t.maxWords = w
		}
	}
	return t
}

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

// toSet turns a whitespace-separated list of codes into a lookup set.
func toSet(codes string) map[string]struct{} {
	fields := strings.Fields(codes)
	m := make(map[string]struct{}, len(fields))
	for _, c := range fields {
		m[c] = struct{}{}
	}
	return m
}
