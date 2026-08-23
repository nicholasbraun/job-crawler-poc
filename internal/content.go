package crawler

// Content holds the parsed result of downloading and parsing a single web page.
// The parser returns it as *Content and the filter/gate pipeline passes it by
// pointer; the Raw* candidate structs (RawJobListing, RawCareerPage) embed it by
// value.
type Content struct {
	Title string
	// MainContent is the page's content as the parser produced it: a Structural
	// Rendering when PARSE_STRUCTURAL_RENDERING is on, today's Flattened Text when it
	// is off. A consumer that wants the plain run of words derives it with FlattenedText
	// rather than reading this field raw (ADR-0046), which is what keeps the Posting
	// Body, the extraction-cache key and the Gate's phrase marks reading exactly what
	// they read today; the two LLM prompts derive their own variant with
	// WithoutLinkTargets. Only the extract-capture tap stores it verbatim, because a
	// captured row is meant to record what the parser produced.
	MainContent string
	URLs        []string
	// MainRegionURLs holds the raw hrefs found inside the page's main region — the
	// same region MainContent is read from — as a SUBSET of URLs, never a narrowing
	// of it. It is rule 1 of the Career Surface (ADR-0051): the Collection Crawl's
	// walk follows a link off this set, or one of the two rules it cannot cover
	// (career-shaped, same-path variant), and leaves the rest of the host's Site
	// Chrome out of the Frontier.
	//
	// URLs stays wide because two Gate rungs read it — the Career Page Confidence
	// Score's job-link weight (ADR-0016) and the Extract Gate's job-link saturation
	// (ADR-0019) — and every Gold Set fixture was captured under the wide harvest
	// (ADR-0043); narrowing it in place would silently move both rungs. Keeping the
	// sets separate is what makes ADR-0051 not a Gate change.
	//
	// Nil unless the parser was built with WithMainRegionLinks: only the Collection
	// lane reads it, so the Discovery Crawl does not pay a second walk over the
	// region on every page it fetches.
	//
	// omitempty — the only tag on this struct — for the reason goldRow.Renderer
	// carries one (#281): the committed Extract Gold Set stores a Content per row and
	// a round trip through its decoder must be the IDENTITY, or every goldset-* verb
	// rewrites the whole substrate. An absent key therefore says no more than "this row
	// records no main-region harvest": a row drawn before ADR-0051 and a page whose
	// main region simply held no links encode identically, since the harvest of a
	// link-free region is an EMPTY slice and omitempty drops that too. Nothing reads
	// the difference — both decode to nil, and the walk treats nil and empty as the
	// same membership set.
	MainRegionURLs []string `json:"MainRegionURLs,omitempty"`
	// JSONLD holds the raw contents of each <script type="application/ld+json">
	// block on the page, for structured-data-aware consumers (e.g. JobPosting
	// extraction). Other pipelines ignore it.
	JSONLD []string
	// SiteName is the page's og:site_name meta value, or "" when absent. The Name
	// Ladder's metadata rung (ADR-0025) reads it for a self-hosted Company. Only the
	// parser populates it; other pipelines ignore it.
	SiteName string
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
