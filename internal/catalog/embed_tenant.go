// ATS Embed tenant resolution: the embed-side counterpart to Identify. It reads
// an ATS board-embed src and recovers the provider family and tenant slug it
// points at, reusing the same host rules as the crawl-side identity logic so ATS
// host knowledge stays in one place (ADR-0001).
package catalog

import (
	"net/url"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// atsEmbedTenantParams maps an ATS provider whose board-embed src carries the
// tenant in a query parameter (not the host or path) to that parameter name.
// Greenhouse's board-embed src is
// boards.greenhouse.io/embed/job_board/js?for={tenant}: the path is fixed embed
// boilerplate, so the tenant lives only in ?for=. Providers absent here carry the
// tenant where a crawlable board URL would — a per-tenant subdomain label or the
// first path segment — recovered by matchATS.
var atsEmbedTenantParams = map[string]string{
	"greenhouse": "for",
}

// ATSEmbedTenant resolves an ATS Embed src (an <iframe>/<script> src pointing at a
// known ATS host) to its provider family and tenant slug — the embed-side
// counterpart to Identify, which reads a crawlable board URL. A query-param
// provider (Greenhouse ?for=) carries the tenant in a parameter; every other
// provider carries it where Identify/matchATS already find it (subdomain label or
// first path segment). ok is false when the host is not a recognized ATS host or
// no tenant can be recovered (a bare board-host embed with no tenant). Pure; never
// touches the network. ADR-0022: v1 uses this only to trigger an ATS Fetch, never
// to derive a crawlable URL for a clientless provider.
func ATSEmbedTenant(src string) (provider, tenant string, ok bool) {
	parsed, err := url.Parse(src)
	if err != nil {
		return "", "", false
	}

	host := strings.ToLower(parsed.Hostname())
	provider, ok = ATSProviderForHost(host)
	if !ok {
		return "", "", false
	}

	if param, isQuery := atsEmbedTenantParams[provider]; isQuery {
		slug := strings.ToLower(strings.TrimSpace(parsed.Query().Get(param)))
		if slug == "" {
			return "", "", false
		}
		return provider, slug, true
	}

	// A non-query provider carries the tenant on the host/path exactly where a
	// crawlable board URL would, so the crawl-side resolver recovers it. Its
	// provider equals the host-resolved one by construction (both key off the same
	// host rules); keep the host-resolved provider.
	if _, slug := matchATS(crawler.URL{Hostname: host, RawURL: src}); slug != "" {
		return provider, slug, true
	}
	return "", "", false
}
