package main

import (
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// TestGoldRenderDocumentKeepsTheStructure is what #288 exists for: the rendering the
// labeller reads has to be STRUCTURE on screen -- headings, list items, table rows,
// links with their targets, form controls -- because the captured text alone is one
// run of words a human cannot resolve an openings index from.
func TestGoldRenderDocumentKeepsTheStructure(t *testing.T) {
	tests := []struct {
		name      string
		rendering string
		want      []string
		absent    []string
	}{
		{
			name:      "a heading is a heading",
			rendering: "#\vUnternehmen\n###\vStandorte",
			want:      []string{"<h1>Unternehmen</h1>", "<h3>Standorte</h3>"},
		},
		{
			name:      "consecutive items are one list",
			rendering: "-\vGo\n-\vKubernetes",
			want:      []string{"<ul><li>Go</li><li>Kubernetes</li></ul>"},
		},
		{
			name:      "consecutive rows are one table, one cell per cell",
			rendering: "|\vBackend Engineer\tBerlin\n|\vDesigner\tRemote",
			want: []string{
				"<table><tr><td>Backend Engineer</td><td>Berlin</td></tr>" +
					"<tr><td>Designer</td><td>Remote</td></tr></table>",
			},
		},
		{
			name:      "a paragraph is a paragraph",
			rendering: "Wir suchen Verstaerkung.",
			want:      []string{"<p>Wir suchen Verstaerkung.</p>"},
		},
		{
			// ADR-0046's neckarfilsjobs case: "/organization/..." against "/jobs/..." is
			// what settles an employer directory, so the target is a click AND is visible.
			name:      "a link keeps its target, as a click and on screen",
			rendering: "-\v[Firma Alpha](/organization/1)",
			want:      []string{`<a href="/organization/1"`, ">Firma Alpha</a>", "(/organization/1)"},
		},
		{
			name:      "a button reads as a button",
			rendering: "[button: Senden]",
			want:      []string{`<span class="button">Senden</span>`},
		},
		{
			// The advertorial.de case: a picker of roles must not read as an index of
			// them, and the control marker is the whole difference.
			name:      "a form control reads as an inert control carrying no page words",
			rendering: "[input checkbox: role]\n[select: Position]",
			want: []string{
				`<span class="control">input checkbox: role</span>`,
				`<span class="control">select: Position</span>`,
			},
		},
		{
			name:      "a target that may not become a click is still shown",
			rendering: "[Apply](javascript:alert(1%29)",
			want:      []string{`<span class="link">Apply</span>`, "javascript:alert(1%29"},
			// The only href in the document is the injected <base>: nothing on a crawled
			// page's own say-so ever becomes one.
			absent: []string{"<a href", `href="javascript`},
		},
		{
			name:      "page text that looks like markup is escaped",
			rendering: "<script>alert(1)</script> & <b>bold</b>",
			want:      []string{"&lt;script&gt;alert(1)&lt;/script&gt; &amp; &lt;b&gt;bold&lt;/b&gt;"},
		},
		{
			// The JS-shell case (ADR-0047): an empty frame with no sentence in it reads
			// as a broken tool rather than as what the crawler actually saw.
			name:      "a page with no text says so",
			rendering: "",
			want:      []string{"carried no text"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := string(goldRenderDocument(tt.rendering, "https://site.test/jobs/1"))
			for _, want := range tt.want {
				if !strings.Contains(doc, want) {
					t.Errorf("the document does not carry %q:\n%s", want, doc)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(doc, absent) {
					t.Errorf("the document carries %q, which it must not:\n%s", absent, doc)
				}
			}
			// No script, ever, in any case: the document is generated, so there is nothing
			// in it to execute, and a page's own text must never become one.
			if strings.Contains(strings.ToLower(doc), "<script") {
				t.Errorf("the rendering document carries a script tag:\n%s", doc)
			}
		})
	}
}

// TestGoldRenderDocumentInjectsABaseReference pins the reference a relative target
// resolves against. Without it "/organization/123" would resolve against this tool
// rather than against the site the page came from.
func TestGoldRenderDocumentInjectsABaseReference(t *testing.T) {
	tests := []struct {
		pageURL string
		want    string
	}{
		{"https://site.test/jobs/1", `<base href="https://site.test/jobs/1" target="_blank">`},
		{"http://site.test/jobs/1", `<base href="http://site.test/jobs/1" target="_blank">`},
		{"mailto:x@y.test", ""},
		{"javascript:alert(1)", ""},
		{"/jobs/1", ""},
		{"", ""},
	}
	for _, tt := range tests {
		t.Run(tt.pageURL, func(t *testing.T) {
			doc := string(goldRenderDocument("-\v[A](/a)", tt.pageURL))
			hasBase := strings.Contains(doc, "<base ")
			if tt.want == "" {
				if hasBase {
					t.Errorf("a base reference was injected for %q, which a browser cannot resolve against:\n%s", tt.pageURL, doc)
				}
				return
			}
			if !strings.Contains(doc, tt.want) {
				t.Errorf("the document does not carry %q:\n%s", tt.want, doc)
			}
		})
	}
}

// TestGoldRenderHrefRefusesWhatIsNotAClickTarget pins the same rule the labelling
// page's own safeURL applies to a row URL: a target off a crawled page is the page's
// text, and only some of it may become a destination.
func TestGoldRenderHrefRefusesWhatIsNotAClickTarget(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"/organization/1", "/organization/1"},
		{"//other.test/x", "//other.test/x"},
		{"https://site.test/jobs/1", "https://site.test/jobs/1"},
		{"http://site.test/jobs/1", "http://site.test/jobs/1"},
		{"jobs/1", "jobs/1"},
		{"  /spaced  ", "/spaced"},
		{"javascript:alert(1)", ""},
		{"JavaScript:alert(1)", ""},
		{"data:text/html,<b>x", ""},
		{"mailto:x@y.test", ""},
		{"#apply", ""},
		{"", ""},
		{"://not a url", ""},
	}
	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			if got := goldRenderHref(tt.target); got != tt.want {
				t.Errorf("goldRenderHref(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// TestGoldRenderRefusalIsHTML pins the shape a refusal takes: the consumer is an
// iframe, and a JSON body in a frame is a download prompt rather than a sentence.
func TestGoldRenderRefusalIsHTML(t *testing.T) {
	doc := string(goldRenderRefusal("the live page is no longer the page that was captured"))
	for _, want := range []string{"<!doctype html>", "the live page is no longer the page that was captured"} {
		if !strings.Contains(doc, want) {
			t.Errorf("the refusal does not carry %q:\n%s", want, doc)
		}
	}
	if strings.Contains(strings.ToLower(doc), "<script") {
		t.Errorf("the refusal carries a script tag:\n%s", doc)
	}
	if strings.Contains(doc, "<base ") {
		t.Errorf("the refusal injects a base reference; there is no page behind it:\n%s", doc)
	}
}

// TestGoldSetUIPageSandboxesTheRendering reads the embedded page itself. The crawler
// executes no JavaScript either, so a script-free rendering reproduces what the Extract
// Gate actually saw rather than a richer page -- and a frame that could run scripts on
// the tool's own origin would be a frame that can see the session writing committed
// ground truth.
func TestGoldSetUIPageSandboxesTheRendering(t *testing.T) {
	page := string(goldSetUIPage)
	if !strings.Contains(page, `sandbox="allow-popups allow-popups-to-escape-sandbox"`) {
		t.Error("the rendering frame is not sandboxed with exactly the two popup allowances")
	}
	for _, forbidden := range []string{"allow-scripts", "allow-same-origin", "allow-forms", "allow-top-navigation"} {
		if strings.Contains(page, forbidden) {
			t.Errorf("the rendering frame allows %q; the site's page is third-party text and gets none of it", forbidden)
		}
	}
	// The only thing the frame is ever pointed at is this tool's own route: the origin
	// is never framed directly.
	if !strings.Contains(page, `frame.src = "/render/"`) {
		t.Error("the page does not set the frame from /render/; the site must never be framed from its own origin")
	}
	if strings.Contains(page, "frame.src = q.url") || strings.Contains(page, "renderframe\" src=") {
		t.Error("the page points the frame at a row URL; the origin is never framed")
	}
}

// TestGoldStructuralRendererPrefix keeps the prefix the tool matches on tied to the
// stamp the parser writes. A renamed stamp fails HERE, rather than silently sending
// every structural row down the re-fetch path it does not need.
func TestGoldStructuralRendererPrefix(t *testing.T) {
	if !strings.HasPrefix(parser.RendererStructural, goldStructuralRendererPrefix) {
		t.Errorf("parser.RendererStructural is %q, which does not start with %q (ADR-0046)", parser.RendererStructural, goldStructuralRendererPrefix)
	}
	if goldRowCarriesRendering(goldRow{Renderer: parser.RendererFlattened}) {
		t.Errorf("a %q row was read as carrying its own Structural Rendering", parser.RendererFlattened)
	}
	if goldRowCarriesRendering(goldRow{}) {
		t.Error("a row with no renderer stamp was read as carrying its own Structural Rendering; all 457 committed rows are that row (ADR-0047)")
	}
	if !goldRowCarriesRendering(goldRow{Renderer: parser.RendererStructural}) {
		t.Errorf("a %q row was not read as carrying its own Structural Rendering", parser.RendererStructural)
	}
	// The prefix, not one version: a v3 row stays renderable the day the grammar moves.
	if !goldRowCarriesRendering(goldRow{Renderer: goldStructuralRendererPrefix + "v99"}) {
		t.Error("a future structural renderer version was not read as carrying its own rendering")
	}
}
