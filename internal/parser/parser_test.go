package parser_test

import (
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

func TestParser(t *testing.T) {
	t.Run("parse simple html", func(t *testing.T) {
		html := `
<html>
	<head>
		<title>Hello World</title>
	</head>
	<body>
		<header>
			<nav>
				<a href="/home">home</a>
			</nav>
		</header>
		<main>
			<h1>This is the h1 headline</h1>
			
			<p>Text with an <a href="https://example.com">absolute link</a> and a <a href="/blog">relative link</a></p>
		</main>
	</body>
</html>
`
		parser := parser.NewHTMLParser()

		content, err := parser.Parse([]byte(html))
		if err != nil {
			t.Fatalf("error parsing content: %v", err)
		}

		if content.Title != "Hello World" {
			t.Errorf("expected title to be 'Hello World', got: %v", content.Title)
		}

		if !strings.Contains(content.MainContent, "This is the h1 headline") {
			t.Errorf("main content should include the h1 headline, got: %s", content.MainContent)
		}

		if !strings.Contains(content.MainContent, "Text with an") {
			t.Errorf("main content should include the text snippet")
		}

		wantURLLength := 3
		gotURLLength := len(content.URLs)
		if gotURLLength != wantURLLength {
			t.Errorf("expected %d URLs, got: %d", wantURLLength, gotURLLength)
		}

		wantURL0 := "/home"
		gotURL0 := content.URLs[0]
		if gotURL0 != wantURL0 {
			t.Errorf("expected first URL to be %s, got: %s", wantURL0, gotURL0)
		}

		wantURL1 := "https://example.com"
		gotURL1 := content.URLs[1]
		if gotURL1 != wantURL1 {
			t.Errorf("expected first URL to be %s, got: %s", wantURL1, gotURL1)
		}

		wantURL2 := "/blog"
		gotURL2 := content.URLs[2]
		if gotURL2 != wantURL2 {
			t.Errorf("expected first URL to be %s, got: %s", wantURL2, gotURL2)
		}
	})

	t.Run("strips non-content tags and collapses whitespace", func(t *testing.T) {
		html := `
<html>
	<body>
		<main>
			<style>.headline { color: red; }</style>
			<h1>Senior   Go
Engineer</h1>
			<script>var tracking = 1;</script>
			<p>Build   crawlers.</p>
			<noscript>Enable JavaScript</noscript>
		</main>
	</body>
</html>
`
		content, err := parser.NewHTMLParser().Parse([]byte(html))
		if err != nil {
			t.Fatalf("error parsing content: %v", err)
		}

		if strings.Contains(content.MainContent, "color: red") {
			t.Errorf("css leaked into main content: %q", content.MainContent)
		}
		if strings.Contains(content.MainContent, "var tracking") {
			t.Errorf("js leaked into main content: %q", content.MainContent)
		}
		if strings.Contains(content.MainContent, "Enable JavaScript") {
			t.Errorf("noscript leaked into main content: %q", content.MainContent)
		}

		want := "Senior Go Engineer Build crawlers."
		if content.MainContent != want {
			t.Errorf("expected collapsed content %q, got: %q", want, content.MainContent)
		}
	})

	t.Run("ld+json survives a script inside the main region", func(t *testing.T) {
		html := `
<html>
	<body>
		<main>
			<h1>Careers</h1>
			<script type="application/ld+json">{"@type":"JobPosting","title":"Go Dev"}</script>
			<p>Open roles</p>
		</main>
	</body>
</html>
`
		content, err := parser.NewHTMLParser().Parse([]byte(html))
		if err != nil {
			t.Fatalf("error parsing content: %v", err)
		}

		// The script is stripped from the LLM-facing text...
		if strings.Contains(content.MainContent, "JobPosting") {
			t.Errorf("ld+json script leaked into main content: %q", content.MainContent)
		}
		if content.MainContent != "Careers Open roles" {
			t.Errorf("unexpected main content: %q", content.MainContent)
		}

		// ...but is still extracted into JSONLD (clone did not clobber the doc).
		if len(content.JSONLD) != 1 {
			t.Fatalf("want 1 ld+json block, got %d: %v", len(content.JSONLD), content.JSONLD)
		}
		if !strings.Contains(content.JSONLD[0], "JobPosting") {
			t.Errorf("expected JobPosting ld+json, got: %s", content.JSONLD[0])
		}
	})

	t.Run("extracts ld+json blocks into JSONLD", func(t *testing.T) {
		html := `
<html>
	<head>
		<title>Careers</title>
		<script type="application/ld+json">{"@type":"JobPosting","title":"Go Dev"}</script>
		<script type="application/json">{"not":"ld"}</script>
		<script type="application/ld+json">{"@type":"Organization","name":"Acme"}</script>
	</head>
	<body><main>content</main></body>
</html>
`
		content, err := parser.NewHTMLParser().Parse([]byte(html))
		if err != nil {
			t.Fatalf("error parsing content: %v", err)
		}

		if len(content.JSONLD) != 2 {
			t.Fatalf("want 2 ld+json blocks (plain application/json excluded), got %d: %v",
				len(content.JSONLD), content.JSONLD)
		}
		if !strings.Contains(content.JSONLD[0], "JobPosting") {
			t.Errorf("first block should be the JobPosting script, got: %s", content.JSONLD[0])
		}
	})
}
