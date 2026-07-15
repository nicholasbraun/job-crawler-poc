// ATS Embed Structural Signal for the Gate's final-rung Confidence Score
// (ADR-0016). It reads the embed srcs and element ids the parser extracted into
// Content, resolving embed hosts to ATS providers through the catalog identity
// module (the single source of ATS host truth). It never touches the network or
// the frontier.
package pagegate

import (
	"net/url"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// atsEmbed reports whether content carries an ATS Embed: a Company page rendering
// a third-party ATS board inline. It fires on an <iframe> whose src points at a
// known ATS host (an iframed board is page-specific, so no marker is required), or
// on a <script> whose src points at a known ATS host WHEN that provider's
// board-container marker is also present among the page's element ids (so a
// site-wide embed script in a shared template does not fire on every page). An
// unrecognized embed host, or a script embed whose provider marker is absent,
// fires nothing — the ADR-0016 fail-safe that keeps a curated-list gap a missed
// LLM saving, never a Leak or False-Certain.
func atsEmbed(content *crawler.Content) bool {
	for _, e := range content.Embeds {
		provider, ok := catalog.ATSProviderForHost(embedHost(e.Src))
		if !ok {
			continue
		}
		if e.IsFrame {
			return true
		}
		if marker, ok := catalog.ATSBoardContainerMarker(provider); ok && hasElementID(content, marker) {
			return true
		}
	}
	return false
}

// embedHost returns the host of an embed src, or "" when the src is relative (no
// host) or unparseable — neither of which is an ATS embed. catalog.ATSProviderForHost
// lowercases, so no case folding is done here.
func embedHost(src string) string {
	u, err := url.Parse(src)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// hasElementID reports whether id appears among content's element ids. Element
// ids are case-sensitive in HTML, so the match is exact (the BambooHR marker is
// literally "BambooHR").
func hasElementID(content *crawler.Content, id string) bool {
	for _, got := range content.ElementIDs {
		if got == id {
			return true
		}
	}
	return false
}
