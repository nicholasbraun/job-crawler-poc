package parser_test

import (
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// renderHTML parses a page with the Structural Rendering on.
func renderHTML(t *testing.T, page string) string {
	t.Helper()

	content, err := parser.NewHTMLParser(parser.WithStructuralRendering(true)).Parse([]byte(page))
	if err != nil {
		t.Fatalf("parse with the rendering on: %v", err)
	}
	return content.MainContent
}

// flatHTML parses a page with the shipped default, i.e. the Flattened Text the
// parser has always produced.
func flatHTML(t *testing.T, page string) string {
	t.Helper()

	content, err := parser.NewHTMLParser().Parse([]byte(page))
	if err != nil {
		t.Fatalf("parse with the rendering off: %v", err)
	}
	return content.MainContent
}

// assertRoundTrip is ADR-0046's invariant on one hand-written page: stripping the
// Structural Rendering must reproduce the flattened parser's output byte for byte.
// Every subtest below calls it, so each page is a round-trip case as well as a
// structural one -- the fixtures prove it at scale, these prove it per rule.
func assertRoundTrip(t *testing.T, page string) {
	t.Helper()

	rendering := renderHTML(t, page)
	want := flatHTML(t, page)
	if got := crawler.FlattenedText(rendering); got != want {
		t.Errorf("the round-trip does not hold:\nrendering: %q\n      got: %q\n     want: %q", rendering, got, want)
	}
}

func TestStructuralRendering(t *testing.T) {
	t.Run("headings keep their level", func(t *testing.T) {
		page := `<html><body><main>
			<h1>Open positions</h1>
			<p>We are hiring.</p>
			<h2>Engineering</h2>
		</main></body></html>`

		rendering := renderHTML(t, page)
		for _, want := range []string{"#\vOpen positions", "##\vEngineering"} {
			if !strings.Contains(rendering, want) {
				t.Errorf("rendering should carry %q, got: %q", want, rendering)
			}
		}
		assertRoundTrip(t, page)
	})

	t.Run("list items are one per line", func(t *testing.T) {
		page := `<html><body><main><ul>
			<li>Go</li>
			<li>Postgres</li>
		</ul></main></body></html>`

		rendering := renderHTML(t, page)
		for _, want := range []string{"-\vGo", "-\vPostgres"} {
			if !strings.Contains(rendering, want) {
				t.Errorf("rendering should carry %q, got: %q", want, rendering)
			}
		}
		if lines := strings.Count(rendering, "-\v"); lines != 2 {
			t.Errorf("want one line per list item, got %d in %q", lines, rendering)
		}
		assertRoundTrip(t, page)
	})

	t.Run("a link keeps its text and its target", func(t *testing.T) {
		// The target is what tells an employer directory (/organization/...) from a
		// job index (/jobs/...) at a glance -- ADR-0046's neckarfilsjobs.de case.
		page := `<html><body><main><p>Interested? <a href="/apply">Apply now</a></p></main></body></html>`

		rendering := renderHTML(t, page)
		if !strings.Contains(rendering, "[Apply now](/apply)") {
			t.Errorf("rendering should carry the link text and its target, got: %q", rendering)
		}
		if got := crawler.FlattenedText(rendering); !strings.Contains(got, "Apply now") || strings.Contains(got, "/apply") {
			t.Errorf("Flattened Text should keep the link text and drop the target, got: %q", got)
		}
		assertRoundTrip(t, page)
	})

	t.Run("a table row is one line", func(t *testing.T) {
		page := `<html><body><main><table>
			<tr>
				<td>Backend Engineer</td>
				<td>Berlin</td>
			</tr>
			<tr>
				<td>Data Engineer</td>
				<td>Munich</td>
			</tr>
		</table></main></body></html>`

		// The cell separator carries what the page had between the cells: a tab here,
		// nothing at all in a minified row whose cells the page itself ran together.
		rendering := renderHTML(t, page)
		for _, want := range []string{"|\vBackend Engineer\tBerlin", "|\vData Engineer\tMunich"} {
			if !strings.Contains(rendering, want) {
				t.Errorf("rendering should carry the row %q, got: %q", want, rendering)
			}
		}
		assertRoundTrip(t, page)
	})

	t.Run("form controls are marked as controls", func(t *testing.T) {
		page := `<html><body><main><form>
			<label>Vorname</label><input type="text" name="Vorname">
			<select name="Position">
				<option>Sales Manager (m/w/d)</option>
				<option>Teamleiter Sales (m/w/d)</option>
				<option>Junior Online-Redakteur (m/w/d)</option>
			</select>
			<textarea name="message">Bitte kurz beschreiben</textarea>
			<button>Senden</button>
		</form></main></body></html>`

		rendering := renderHTML(t, page)
		for _, want := range []string{
			"[input text: Vorname]",
			"[select: Position]",
			"[textarea: message]",
			"[button: Senden]",
		} {
			if !strings.Contains(rendering, want) {
				t.Errorf("rendering should carry %q, got: %q", want, rendering)
			}
		}

		// The controls' own text is page text and must survive the strip: an option
		// list is exactly what tells ONE application form from an index of roles.
		flat := crawler.FlattenedText(rendering)
		for _, want := range []string{
			"Sales Manager (m/w/d)",
			"Teamleiter Sales (m/w/d)",
			"Bitte kurz beschreiben",
			"Senden",
		} {
			if !strings.Contains(flat, want) {
				t.Errorf("Flattened Text should keep %q, got: %q", want, flat)
			}
		}
		assertRoundTrip(t, page)
	})

	t.Run("a button contributes its own text, a submit input does not", func(t *testing.T) {
		// The trap: a <button>'s words are page text and have always been in the
		// flattened output, while an <input type=submit> caption is an attribute and
		// has never been. Same caption, opposite obligations.
		page := `<html><body><main><form>
			<button>Senden</button>
			<input type="submit" value="Absenden">
		</form></main></body></html>`

		rendering := renderHTML(t, page)
		if !strings.Contains(rendering, "[input submit: Absenden]") {
			t.Errorf("rendering should mark the submit input, got: %q", rendering)
		}

		flat := crawler.FlattenedText(rendering)
		if !strings.Contains(flat, "Senden") {
			t.Errorf("the button's own text must survive, got: %q", flat)
		}
		if strings.Contains(flat, "Absenden") {
			t.Errorf("the submit input's value is not page text and must not survive, got: %q", flat)
		}
		assertRoundTrip(t, page)
	})

	t.Run("image alternative text never survives flattening", func(t *testing.T) {
		page := `<html><body><main><p>Logo: <img alt="1A SMS GmbH" src="/logo.png"></p></main></body></html>`

		rendering := renderHTML(t, page)
		if !strings.Contains(rendering, "[img: 1A SMS GmbH]") {
			t.Errorf("rendering should mark the image, got: %q", rendering)
		}
		if flat := crawler.FlattenedText(rendering); strings.Contains(flat, "1A SMS") {
			t.Errorf("alt text is not in today's output and must not survive, got: %q", flat)
		}
		assertRoundTrip(t, page)
	})

	t.Run("scripts, styles, noscript, svg and template are excluded", func(t *testing.T) {
		page := `<html><body><main>
			<style>.headline { color: red; }</style>
			<h1>Senior Go Engineer</h1>
			<script>var tracking = 1;</script>
			<noscript>Enable JavaScript</noscript>
			<svg><title>icon</title></svg>
			<template><p>Templated</p></template>
			<p>Build crawlers.</p>
		</main></body></html>`

		rendering := renderHTML(t, page)
		for _, unwanted := range []string{"color: red", "var tracking", "Enable JavaScript", "icon", "Templated"} {
			if strings.Contains(rendering, unwanted) {
				t.Errorf("%q leaked into the rendering: %q", unwanted, rendering)
			}
		}
		assertRoundTrip(t, page)
	})

	t.Run("the region is the same one the parser picks today", func(t *testing.T) {
		// Rendering must never change WHICH part of a page becomes content, so each
		// of these mirrors a committed off-mode case (#279 changes no region rule).
		t.Run("chrome outside a semantic container stays out", func(t *testing.T) {
			page := `<html><body>
				<header>Gratisversand ab 60 Euro</header>
				<nav><a href="/stellen">Stellenangebote</a></nav>
				<div>Service und Kontakt: Telefon 08861 12345.</div>
				<footer>Impressum</footer>
			</body></html>`

			rendering := renderHTML(t, page)
			for _, chrome := range []string{"Stellenangebote", "Gratisversand", "Impressum"} {
				if strings.Contains(rendering, chrome) {
					t.Errorf("chrome %q leaked into the rendering: %q", chrome, rendering)
				}
			}
			assertRoundTrip(t, page)
		})

		t.Run("chrome inside a semantic container is kept", func(t *testing.T) {
			page := `<html><body><main>
				<nav><a href="/jobs">Open positions</a></nav>
				<h1>Career at Acme</h1>
			</main></body></html>`

			if rendering := renderHTML(t, page); !strings.Contains(rendering, "Open positions") {
				t.Errorf("nav inside <main> should be kept, got: %q", rendering)
			}
			assertRoundTrip(t, page)
		})

		t.Run("a page whose only text is chrome keeps it rather than going empty", func(t *testing.T) {
			page := `<html><body><header>
				<h1>Senior Go Engineer</h1>
				<p>Build crawlers.</p>
			</header></body></html>`

			rendering := renderHTML(t, page)
			if !strings.Contains(rendering, "Senior Go Engineer") {
				t.Errorf("the chrome-only text should survive, got: %q", rendering)
			}
			assertRoundTrip(t, page)
		})
	})

	t.Run("a page that runs its blocks together keeps them together", func(t *testing.T) {
		// 58 of the 90 committed fixtures have this shape: no whitespace between the
		// tags, so goquery's Text() -- and therefore SourceHash -- reads "AB", not
		// "A B". The rendering makes the boundary visible with the JOIN, and the
		// strip deletes it rather than folding it to a space.
		page := `<html><body><div>Über Redcare</div><div>Newsroom</div></body></html>`

		rendering := renderHTML(t, page)
		if !strings.Contains(rendering, "Über Redcare\t\nNewsroom") {
			t.Errorf("the rendering should carry the JOIN at a boundary the page ran together, got: %q", rendering)
		}

		want := flatHTML(t, page)
		if want != "Über RedcareNewsroom" {
			t.Fatalf("precondition: today's parser produces %q", want)
		}
		if got := crawler.FlattenedText(rendering); got != want {
			t.Errorf("stripping the JOIN must not insert whitespace: got %q, want %q", got, want)
		}
	})

	// The #289 family. Each of these round-trips through page shapes no committed
	// fixture happens to contain, which is exactly why they are hand-written: the
	// 90-fixture round-trip was green while the invariant was false. A failure here
	// means the rendering reaches SourceHash as different bytes than the flattening
	// parser produced, and the response is to fix the grammar, never the case.
	t.Run("a page word that is exactly a line prefix survives", func(t *testing.T) {
		// Minified, so every block boundary is a JOIN -- whose first byte is the
		// tab the prefix rule used to key on.
		for _, page := range []string{
			`<html><body><main><div>Salary</div><div>-</div><div>Berlin</div></main></body></html>`,
			`<html><body><main><div>|</div><div>Berlin</div></main></body></html>`,
			`<html><body><main><div>#</div><div>1 in Germany</div></main></body></html>`,
			`<html><body><main><div>######</div><div>x</div></main></body></html>`,
			`<html><body><main><table><tr><td><div>Salary</div></td><td>-</td> <td>Remote</td></tr></table></main></body></html>`,
		} {
			assertRoundTrip(t, page)
		}
	})

	t.Run("a wrapping marker declines rather than corrupt its text", func(t *testing.T) {
		// Page text that would make the marker unreadable: an unbalanced "]" closes
		// it early, and text shaped like a control marker gets claimed by the strip
		// as a form control. Both used to leave the raw marker -- and a link's href
		// -- in the Posting Body, the search index and SourceHash.
		for _, page := range []string{
			`<html><body><main><p>x</p><a href="/x">See ] more</a></main></body></html>`,
			`<html><body><main><p>x</p><a href="/x">m/w/d [DE</a></main></body></html>`,
			`<html><body><main><p>x</p><a href="/x">[[a]]</a></main></body></html>`,
			`<html><body><main><p>x</p><a href="/x">input your details</a></main></body></html>`,
			`<html><body><main><p>x</p><a href="/x">button: Senden</a></main></body></html>`,
			`<html><body><main><p>x</p><button>a]b</button></main></body></html>`,
			`<html><body><main><p>x</p><button>Apply [now</button></main></body></html>`,
		} {
			assertRoundTrip(t, page)
		}

		// Declining costs the marking, never the words.
		page := `<html><body><main><a href="/x">See ] more</a></main></body></html>`
		if rendering := renderHTML(t, page); strings.Contains(rendering, "/x") {
			t.Errorf("a declined link must not leave its target behind, got: %q", rendering)
		}
	})

	t.Run("the switch is off by default", func(t *testing.T) {
		page := `<html><body><main>
			<h1>Open positions</h1>
			<ul><li>Go</li></ul>
			<p><a href="/apply">Apply now</a></p>
			<img alt="Acme" src="/logo.png">
		</main></body></html>`

		flat := flatHTML(t, page)
		if strings.ContainsAny(flat, "[\t\n\v") {
			t.Errorf("the default parser must emit no rendered structure at all, got: %q", flat)
		}
		if want := "Open positions Go Apply now"; flat != want {
			t.Errorf("default output = %q, want %q", flat, want)
		}
	})
}
