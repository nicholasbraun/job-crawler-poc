package crawler

// Content holds the parsed result of downloading and parsing a single web page.
// Used as a pointer type.
type Content struct {
	Title       string
	MainContent string
	URLs        []string
	// JSONLD holds the raw contents of each <script type="application/ld+json">
	// block on the page, for structured-data-aware consumers (e.g. JobPosting
	// extraction). Other pipelines ignore it.
	JSONLD []string
	// Embeds holds every <iframe> and <script> that carries a src, tagged by
	// element kind. It is kept SEPARATE from URLs so a third-party board, tracker,
	// or CDN src is never enqueued as a crawl target. The Gate's ATS-embed signal
	// (ADR-0016) reads these to recognize a Company page that renders an ATS board
	// inline. Other pipelines ignore it.
	Embeds []Embed
	// ElementIDs holds the id attribute of every element on the page that has one.
	// The Gate's ATS-embed signal checks these for a provider's board-container
	// marker (e.g. Greenhouse "grnhse_app"), so a site-wide embed script with no
	// rendered board does not fire. Other pipelines ignore it.
	ElementIDs []string
}

// Embed is a third-party board embed the parser found on a page: an <iframe> or
// a <script> that carries a src. The Gate's ATS-embed signal (ADR-0016) fires on
// an iframe pointing at a known ATS host (an iframed board is page-specific), or
// on a script pointing at a known ATS host when that provider's board-container
// marker is also present.
type Embed struct {
	// Src is the raw src attribute value (may be relative or protocol-relative).
	Src string
	// IsFrame is true for an <iframe>, false for a <script>. It gates the marker
	// check: an iframe embed fires with no marker; a script embed requires the
	// provider's board-container marker to be present too.
	IsFrame bool
}
