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

		if !strings.Contains(content.MainContent, "<h1>This is the h1 headline</h1>") {
			t.Errorf("main content should include the h1 headline, got: %s", content.MainContent)
		}

		if !strings.Contains(content.MainContent, "<p>Text with an") {
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
}
