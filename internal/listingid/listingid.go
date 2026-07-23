// Package listingid derives the stable Corpus identity of a Job Listing — the
// value the Corpus is keyed and deduplicated on (ADR-0034), stored in
// job_listing.canonical_url. Identity is lane-specific: an ATS-Fetch posting
// keys on the provider's stable posting id (so a title re-slug never forges a
// new posting), a crawled posting on its canonicalized source URL. Every
// ambiguous case biases toward keep-distinct: a false-merge silently loses a
// real posting (unrecoverable), a false-new is a visible, recoverable blip.
package listingid

import (
	"net/url"
	"strings"
)

// FromATS returns the ATS-Fetch lane identity, provider:tenant:sourceID, built
// from the provider's stable posting id rather than the URL (ADR-0034 — several
// providers append a title slug that re-slugs on edit). provider and tenant are
// lowercased (host-case-insensitive); sourceID is used verbatim (bias
// keep-distinct — never fold two provider ids together). Callers MUST pass a
// non-empty sourceID; an empty one collapses a whole tenant to one key, so the
// ATS processor falls back to FromURL instead.
func FromATS(provider, tenant, sourceID string) string {
	return strings.ToLower(provider) + ":" + strings.ToLower(tenant) + ":" + sourceID
}

// FromURL returns the crawl-lane identity: the canonicalized posting URL. It
// forces https, lowercases the host and strips a leading "www.", drops the
// fragment, removes only KNOWN tracking params (keeping every unknown param —
// some boards carry the posting id in ?jobId=, so a blanket query-strip would
// false-merge), sorts the remaining params, and strips a trailing slash from a
// non-root path. A rawURL that fails to parse is returned unchanged (kept,
// never merged). Deliberately distinct from catalog.CanonicalURL (which strips
// the whole query) and the frontier normalize (which keeps www / http).
func FromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Scheme = "https"
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Fragment = ""
	u.RawFragment = ""
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			if _, isTracker := trackingParams[strings.ToLower(k)]; isTracker {
				q.Del(k)
			}
		}
		u.RawQuery = q.Encode() // Encode sorts by key
	}
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawPath = ""
	}
	return u.String()
}

// trackingParams is the CONSERVATIVE tracker denylist (ADR-0034 keep-distinct):
// only unambiguous trackers, because stripping a meaningful param false-merges
// (unrecoverable) while leaving an unknown tracker on only false-news
// (recoverable). Anything not listed here is kept.
var trackingParams = map[string]struct{}{
	"utm_source": {}, "utm_medium": {}, "utm_campaign": {},
	"utm_term": {}, "utm_content": {}, "utm_id": {},
	"gclid": {}, "fbclid": {}, "msclkid": {}, "yclid": {}, "dclid": {},
	"mc_cid": {}, "mc_eid": {}, "_ga": {}, "_gl": {},
}
