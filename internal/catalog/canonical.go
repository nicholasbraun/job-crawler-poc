package catalog

import (
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// CanonicalURL returns the canonical storage form of a Career Page URL so that
// trivially-equivalent variants collapse to a single Catalog row under the
// UNIQUE(company_id, url) constraint. It forces the scheme to https, strips the
// entire query string (discarding pagination and XSS/SQLi fuzzer params alike),
// and strips any trailing slash, including the bare root slash. It is pure and
// idempotent and is reused by the Catalog Doctor to re-canonicalise stored rows.
// A rawURL that fails to parse is returned unchanged.
//
// This is intentionally distinct from the crawler's frontier normalize, which
// preserves the query string (query-paginated boards must stay crawlable) and
// keeps the root slash.
func CanonicalURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	parsed.Scheme = "https"
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawPath = ""
	return parsed.String()
}

// StoredCareerPageURL returns the exact string form under which a Career Page
// URL is persisted in the Catalog: the ATS board-root collapse (CareerPageURL)
// when the host is a recognized ATS, then CanonicalURL. Discovery and Catalog
// Import both store through this one helper, so imported rows are
// byte-identical to discovered ones (ADR-0013).
func StoredCareerPageURL(u crawler.URL) string {
	raw := u.RawURL
	if canonical, ok := CareerPageURL(u); ok {
		raw = canonical
	}
	return CanonicalURL(raw)
}
