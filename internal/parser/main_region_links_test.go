package parser_test

import (
	"slices"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// TestMainRegionURLs pins rule 1 of the Career Surface (ADR-0051): which links the
// parser reports as being inside the page's main region. It asserts on the harvested
// SET only -- never on how the region was selected -- so the as-is carve-out below
// stays re-decidable without rewriting these cases.
func TestMainRegionURLs(t *testing.T) {
	// A page with a semantic container: the site's <nav>/<header>/<footer> are
	// SIBLINGS of <main>, so taking the container already excludes the global menu --
	// the whole reason the as-is carve-out limits the risk rather than the benefit.
	// The in-<main> <nav> is the breadcrumb/in-content-sidebar class that survives on
	// purpose: stripping it can empty a compact Career Page whose roles sit in a
	// sidebar (ADR-0051, and #270 before it).
	const semantic = `
<html>
	<body>
		<header><a href="/about">about</a></header>
		<nav><a href="/solutions">solutions</a></nav>
		<main>
			<a href="/jobs/senior-go-engineer">Senior Go Engineer</a>
			<nav aria-label="Pagination"><a href="/jobs?page=2">2</a></nav>
		</main>
		<footer><a href="/imprint">imprint</a></footer>
	</body>
</html>
`
	// No semantic container, so mainRegion falls back to the body with the chrome
	// dropped (#270): the harvest is the same region, minus the same furniture.
	const noContainer = `
<html>
	<body>
		<nav><a href="/solutions">solutions</a></nav>
		<div><a href="/jobs/senior-go-engineer">Senior Go Engineer</a></div>
	</body>
</html>
`
	// The withoutChrome fallback: the page's ONLY text lives in its furniture, so the
	// unstripped body is kept rather than dropping the page to nothing. The link
	// harvest follows that region, which means the whole-document set.
	const chromeOnly = `
<html>
	<body>
		<nav><a href="/karriere">Karriere</a> Offene Stellen</nav>
	</body>
</html>
`

	tests := []struct {
		name string
		html string
		want []string
	}{
		{
			name: "a semantic container excludes its sibling chrome and keeps the chrome inside it",
			html: semantic,
			want: []string{"/jobs/senior-go-engineer", "/jobs?page=2"},
		},
		{
			name: "no semantic container harvests the body with the chrome dropped",
			html: noContainer,
			want: []string{"/jobs/senior-go-engineer"},
		},
		{
			name: "a page whose only text is chrome keeps the whole document",
			html: chromeOnly,
			want: []string{"/karriere"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := parser.NewHTMLParser(parser.WithMainRegionLinks(true)).Parse([]byte(tt.html))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if !slices.Equal(content.MainRegionURLs, tt.want) {
				t.Errorf("MainRegionURLs = %v, want %v", content.MainRegionURLs, tt.want)
			}
			// Content.URLs is never narrowed: two Gate rungs read it and every Gold Set
			// fixture was captured under the wide harvest (ADR-0051). The region set is a
			// subset of it, spelled identically, which is what lets the walk test
			// membership by string.
			for _, href := range content.MainRegionURLs {
				if !slices.Contains(content.URLs, href) {
					t.Errorf("MainRegionURLs entry %q is missing from the whole-document URLs %v", href, content.URLs)
				}
			}
		})
	}

	t.Run("the harvest is off by default", func(t *testing.T) {
		content, err := parser.NewHTMLParser().Parse([]byte(semantic))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if len(content.MainRegionURLs) != 0 {
			t.Errorf("MainRegionURLs = %v, want empty with the option off", content.MainRegionURLs)
		}
		// Off must change nothing else: the Discovery Crawl reads this parser.
		want := []string{"/about", "/solutions", "/jobs/senior-go-engineer", "/jobs?page=2", "/imprint"}
		if !slices.Equal(content.URLs, want) {
			t.Errorf("URLs = %v, want the unchanged whole-document harvest %v", content.URLs, want)
		}
	})
}
