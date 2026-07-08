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
}
