package crawler

import (
	"errors"
	"net/url"
	"strings"
)

// URL is a parsed crawl target. It is a value type — safe to copy and compare.
// RawURL is always in canonical form (see normalize).
type URL struct {
	Hostname string
	RawURL   string
	// Depth is the number of links followed from a seed URL to reach this URL.
	// Seed URLs have depth 0.
	Depth int
	// Scope and Owner are the provenance keys every URL carries (ADR-0021). Both
	// are set at the seed and inherited unchanged by every link discovered from
	// it — unlike Depth, which increments. Empty for a Discovery Crawl (which
	// roams); populated by a Collection Crawl, whose seeds are resolved from the
	// Catalog. Scope is the seed's URL-derived CompanyKey (the fence key); Owner
	// is the seed's catalog-stored CompanyKey (the attribution key).
	Scope string
	Owner string
}

func NewURL(u string) (URL, error) {
	if u == "" {
		return URL{}, errors.New("url: cannot create empty url")
	}

	parsed, err := url.Parse(u)
	if err != nil {
		return URL{}, err
	}
	normalize(parsed)

	return URL{
		Hostname: parsed.Hostname(),
		RawURL:   parsed.String(),
		Depth:    0,
	}, nil
}

// Parse resolves u against the base URL, returning a new normalized URL that
// inherits the base's Scope and Owner and carries Depth+1. URL is a value type,
// so the base is read, never mutated.
func (base URL) Parse(u string) (URL, error) {
	parsed, err := url.Parse(base.RawURL)
	if err != nil {
		return URL{}, err
	}
	parsed, err = parsed.Parse(u)
	if err != nil {
		return URL{}, err
	}
	normalize(parsed)

	return URL{
		Hostname: parsed.Hostname(),
		RawURL:   parsed.String(),
		Depth:    base.Depth + 1,
		Scope:    base.Scope,
		Owner:    base.Owner,
	}, nil
}

// trackingParams are query parameters that never change which page is served —
// marketing/analytics tags a link carries only to attribute a click. Left in place
// they defeat frontier dedup: the same page arrives under ?utm_source=a and
// ?utm_source=b as two distinct visited entries (observed live: substack cross-promo
// links minting a fresh key per referral). normalize strips them so the variants
// collapse to one canonical URL. Keys are matched exactly except utm_*, stripped by
// prefix. Cache/session/trap params (cHash, tx_*, PHPSESSID) are handled by the
// BlockQueryParams URL filter instead — those mark a page not worth fetching at all,
// so they reject rather than dedup.
var trackingParams = map[string]struct{}{
	"gclid": {}, "gclsrc": {}, "dclid": {}, "fbclid": {}, "msclkid": {},
	"mc_cid": {}, "mc_eid": {}, "igshid": {}, "_hsenc": {}, "_hsmi": {},
	"ref_src": {}, "vero_id": {}, "yclid": {}, "wickedid": {}, "twclid": {},
}

// normalize canonicalizes a URL so that trivially-equivalent variants dedup
// to the same string: lowercases scheme and host, drops the fragment, strips
// tracking query parameters, sorts the rest, and strips a trailing slash from
// non-root paths.
func normalize(u *url.URL) {
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" {
		q := u.Query()
		for key := range q {
			lower := strings.ToLower(key)
			if _, tracked := trackingParams[lower]; tracked || strings.HasPrefix(lower, "utm_") {
				q.Del(key)
			}
		}
		u.RawQuery = q.Encode()
	}
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
	}
}
