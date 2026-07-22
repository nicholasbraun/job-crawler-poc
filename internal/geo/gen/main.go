// Command gen generates internal/geo/gazetteer.tsv, the Country Resolver's
// lookup table (ADR-0029, ADR-0031), from a pinned GeoNames + ISO 3166 snapshot
// (CC BY 4.0) plus a small hand synonym supplement. It is offline and
// deterministic: the reference files are embedded, the keep-on-doubt dominance
// policy is applied in build, and the output is sorted so regeneration yields a
// byte-identical diff for review.
package main

import (
	_ "embed"
	"flag"
	"log"
	"os"
)

//go:generate go run . -out ../gazetteer.tsv

//go:embed data/cities5000.txt
var cities5000Data []byte

//go:embed data/countryInfo.txt
var countryInfoData []byte

//go:embed data/admin1CodesASCII.txt
var admin1Data []byte

func main() {
	out := flag.String("out", "../gazetteer.tsv", "output artifact path (relative to the gen dir)")
	flag.Parse()

	entries := build(defaultPolicy,
		parseCountryInfo(countryInfoData),
		parseCities5000(cities5000Data),
		parseAdmin1US(admin1Data),
		supplement)

	f, err := os.Create(*out)
	if err != nil {
		log.Fatalf("gen: create %s: %v", *out, err)
	}
	defer func() { _ = f.Close() }()

	if err := writeTSV(f, entries); err != nil {
		log.Fatalf("gen: write %s: %v", *out, err)
	}
	if err := f.Close(); err != nil {
		log.Fatalf("gen: close %s: %v", *out, err)
	}
	log.Printf("gen: wrote %d entries to %s", len(entries), *out)
}
