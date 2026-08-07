package crawler

import (
	"encoding/json"
	"strings"
)

// StructuredPosting is what a page's structured data says about the single job
// posting it publishes — the typed judgment fields the crawl lane needs (ADR-0042).
// It is produced only by LonePosting, and only for a page whose structured data is
// unambiguous, so no caller has to ask how many posting nodes the page carried or
// how they were nested.
//
// Every field is absent-as-empty: a page that publishes no title, no address, or no
// working mode yields the zero value for it, never a guess.
type StructuredPosting struct {
	// Title is the posting's declared title, entity-decoded and whitespace-collapsed
	// (2.8% of live posting nodes ship an entity-encoded title, e.g. "Office 365
	// &#038; Azure Engineer"; none ship HTML tags). Empty when the page publishes no
	// usable one — the signal ADR-0042 uses to hand a half-published page to the
	// model instead.
	Title string
	// Description is the posting's declared description EXACTLY as published, still
	// carrying whatever HTML or entity encoding the site ships. Reducing and capping
	// it belongs to PostingBody (ADR-0041), which owns the strip/unescape/strip
	// ordering that reduction depends on; repeating it here would either apply it
	// twice or split one contract across two files. It is the only long field here,
	// and the only one a caller must reduce itself.
	Description string
	// Location is the posting's place composed into the free-text shape the save-time
	// Country Resolver reads (ADR-0029): locality, region and country joined with
	// ", ". Empty when the page publishes no address.
	Location string
	// Country is the same place's addressCountry verbatim — an ISO code ("AU") or a
	// name ("Japan"), whichever the page published — carried separately as the
	// Country Resolver's hint. It must not be resolved out of Location alone: the
	// Resolver has no bare alpha-2 country keys but does key US state abbreviations,
	// so "Perth, WA, AU" resolves to US on the region and never reaches the country.
	// Read from the same Place that produced Location, so the two never disagree.
	// Empty when the page publishes no country.
	Country string
	// WorkArrangement is WorkArrangementRemote when the posting positively declares
	// remote work, and WorkArrangementUnspecified for everything else (ADR-0030) — a
	// page that does not state its mode is never guessed Onsite.
	WorkArrangement WorkArrangement
}

// LonePosting reads the single job posting a page publishes in its structured data,
// reporting ok only when that data is UNAMBIGUOUS: exactly one JobPosting node and no
// openings index (ADR-0042). It is the exact complement of the openings-index shape the
// Extract Gate rejects, and the one reading of a page's structured posting data the
// codebase has — the Extract Gate, the Posting Body derivation, and the Free Extraction
// all resolve through it, so they cannot drift into three answers about one page.
//
// ok is a statement about the page's SHAPE, not about the posting's quality: a lone
// node with no title still reports ok with an empty Title, and the caller decides what
// a title-less posting is worth. On !ok the zero StructuredPosting is returned, so a
// caller that ignores ok reads empty fields rather than a half-page.
//
// A nil page, absent structured data, and an unparseable block all contribute nothing
// and never fail the read (the fail-safe ADR-0016 already applies to the gate's use).
//
// Pure: no model, no network, no database.
func LonePosting(content *Content) (StructuredPosting, bool) {
	scan := scanStructuredData(content)
	if scan.postings != 1 || scan.openingsIndex {
		return StructuredPosting{}, false
	}
	return scan.posting, true
}

// HasOpeningsIndex reports whether a page's structured data publishes an OPENINGS
// INDEX — two or more JobPosting nodes anywhere on the page, or an ItemList wrapping at
// least one. It is the companion of LonePosting over the same scan: a page it fires on
// is a hub to crawl, not a posting to extract.
//
// A lone JobPosting is one Job Listing, not a hub, so it fires nothing; a nil page,
// absent structured data, and an unparseable block likewise. Both polarities of that
// silence are deliberate: on the Discovery Gate the signal only ever ADDS credit, so an
// unreadable block can never turn a single posting into a False-Certain (ADR-0016); on
// the Extract Gate it costs missed savings, never a lost posting.
func HasOpeningsIndex(content *Content) bool {
	scan := scanStructuredData(content)
	return scan.openingsIndex || scan.postings >= 2
}

// structuredScan is the result of one walk over structured data: how many JobPosting
// nodes it holds, whether an ItemList among them wraps at least one, and the typed
// fields of the FIRST posting found. That first posting is meaningful only when
// postings == 1 — with several nodes, which one is "first" follows Go's randomized map
// iteration — which is exactly the condition LonePosting requires before returning it.
type structuredScan struct {
	postings      int
	openingsIndex bool
	posting       StructuredPosting
}

// merge folds a child scan into s: counts add, the openings-index flag is sticky, and
// the first posting's fields win so a later node never overwrites an earlier one.
func (s *structuredScan) merge(child structuredScan) {
	if s.postings == 0 && child.postings > 0 {
		s.posting = child.posting
	}
	s.postings += child.postings
	s.openingsIndex = s.openingsIndex || child.openingsIndex
}

// scanStructuredData walks every structured-data block on the page once — the single
// scan LonePosting and HasOpeningsIndex are both defined over. An unparseable block is
// skipped: a site that ships one broken script must not cost the read of the others.
func scanStructuredData(content *Content) structuredScan {
	var scan structuredScan
	if content == nil {
		return scan
	}
	for _, block := range content.JSONLD {
		var node any
		if err := json.Unmarshal([]byte(block), &node); err != nil {
			continue // an unparseable block contributes nothing (fail-safe)
		}
		scan.merge(scanNode(node))
	}
	return scan
}

// scanNode walks one decoded JSON-LD value — arrays, objects, and any @graph /
// itemListElement / item nesting — counting JobPosting nodes, recognizing an ItemList
// that wraps at least one, and reading the first posting's typed fields.
func scanNode(v any) structuredScan {
	var scan structuredScan
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			scan.merge(scanNode(item))
		}
	case map[string]any:
		if ldHasToken(node["@type"], "jobposting") {
			scan.postings = 1
			scan.posting = postingFields(node)
		}
		// Recurse into every value so nested JobPostings (an ItemList's
		// itemListElement, a ListItem's item, an @graph) are all counted once.
		var descendants structuredScan
		for _, val := range node {
			descendants.merge(scanNode(val))
		}
		if ldHasToken(node["@type"], "itemlist") && descendants.postings > 0 {
			descendants.openingsIndex = true
		}
		scan.merge(descendants)
	}
	return scan
}

// postingFields reads one JobPosting node into typed fields. Everything schema-shaped
// — a value published as a string, as a named node, or as an array of either — is
// resolved here so no caller ever touches decoded JSON.
func postingFields(node map[string]any) StructuredPosting {
	location, country := structuredLocation(node["jobLocation"])
	posting := StructuredPosting{
		Title:           ldText(node["title"]),
		Location:        location,
		Country:         country,
		WorkArrangement: WorkArrangementUnspecified,
	}
	// Read as a raw string, deliberately unlike every other field: the body reduction
	// belongs to PostingBody (see StructuredPosting.Description). A non-string
	// description yields "", which sends PostingBody to the page's main content.
	if description, ok := node["description"].(string); ok {
		posting.Description = description
	}
	// TELECOMMUTE is schema.org's remote-work marker and the only value live pages use
	// in practice (283 of 285 observed jobLocationType values; the outliers are one
	// "Hybrid" and a handful of nulls). Anything else stays Unspecified — a posting
	// that does not positively state its mode is never guessed (ADR-0030).
	if ldHasToken(node["jobLocationType"], "telecommute") {
		posting.WorkArrangement = WorkArrangementRemote
	}
	return posting
}

// structuredLocation composes the free-text Location the save-time Country Resolver
// reads (ADR-0029) from a posting's jobLocation, and returns that Place's declared
// country alongside it as the Resolver's hint (see StructuredPosting.Country).
// jobLocation is a Place or an ARRAY of them (19% of live posting nodes carry an
// array, up to 21 offices); the first Place that composes to a non-empty string wins,
// and the country returned is that same Place's. Joining them all was rejected: it
// would hand the Resolver several countries at once and read as noise wherever the
// listing is displayed.
func structuredLocation(v any) (location, country string) {
	switch node := v.(type) {
	case []any:
		for _, item := range node {
			if location, country = structuredLocation(item); location != "" {
				return location, country
			}
		}
	case map[string]any:
		return addressText(node["address"])
	}
	return "", ""
}

// addressText composes one postal address into "locality, region, country", skipping
// the parts the page leaves empty, and takes an address published as a bare string
// ("Edmonton, AB", 8 of 1167 live posting nodes) verbatim. It returns the
// addressCountry separately as well, so the save-time Country Resolver can be given
// the country the page actually declared instead of having to find it inside the
// composed string (see StructuredPosting.Country). A bare-string address declares no
// country and yields "".
//
// Redundant parts are deliberately NOT de-duplicated: real addresses compose to
// "Berlin, Berlin, DE" and "Hamburg, Germany, Germany", and ADR-0042 pins that those
// resolve correctly through the real Country Resolver, so no normalization is
// introduced here. This is where it parts company with ats.softgardenLocation, which
// dedupes one provider's feed for display.
func addressText(v any) (text, country string) {
	switch address := v.(type) {
	case []any:
		for _, item := range address {
			if text, country = addressText(item); text != "" {
				return text, country
			}
		}
	case map[string]any:
		parts := []string{}
		for _, key := range []string{"addressLocality", "addressRegion", "addressCountry"} {
			if part := ldText(address[key]); part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, ", "), ldText(address["addressCountry"])
	case string:
		return htmlToText(address), ""
	}
	return "", ""
}

// ldText reads a short display value out of a JSON-LD field, whichever of the shapes
// schema.org allows the site chose: a plain string, a named node
// ({"@type":"Country","name":"Japan"}), or an array of either — all three observed live
// on addressCountry alone. Anything else (a number, a bool, a node with no name) yields
// "", so a malformed field degrades to absent rather than to junk.
//
// The value is reduced with the same htmlToText the Posting Body uses, because sites
// ship entity-encoded titles ("Vendeur.se &quot;Ville&quot;"). A doubly-encoded value
// unescapes once, matching the body reduction; that shape is 1 of 1167 live titles and
// is not worth a second pass.
func ldText(v any) string {
	switch value := v.(type) {
	case string:
		return htmlToText(value)
	case map[string]any:
		return ldText(value["name"])
	case []any:
		for _, item := range value {
			if text := ldText(item); text != "" {
				return text
			}
		}
	}
	return ""
}

// ldHasToken reports whether a JSON-LD value that may be a string or an array of
// strings carries want as a case-insensitive substring. It serves @type — so a bare
// "JobPosting" and a "https://schema.org/JobPosting" both hit — and jobLocationType,
// whose remote marker varies only in case. Mirrors isOrganizationType in the
// career_page_processor derive logic.
func ldHasToken(v any, want string) bool {
	switch value := v.(type) {
	case string:
		return strings.Contains(strings.ToLower(value), want)
	case []any:
		for _, item := range value {
			if s, ok := item.(string); ok && strings.Contains(strings.ToLower(s), want) {
				return true
			}
		}
	}
	return false
}
