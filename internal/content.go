package crawler

// Content holds the parsed result of downloading and parsing a single web page.
// Used as a pointer type.
type Content struct {
	Title       string
	MainContent string
	URLs        []string
}
