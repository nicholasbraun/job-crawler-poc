package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

// TestStructuralRenderingRoundTrip asserts ADR-0046's invariant over every committed
// classifier fixture: stripping a Structural Rendering reproduces today's Flattened
// Text byte for byte.
//
//	crawler.FlattenedText(StructuralRendering(html)) == the golden artefact
//
// This is the whole safety argument for the change, not a nicety. SourceHash is
// persisted on ~46k job_listing rows and a different value there silently
// re-extracts the entire Corpus, so a failure here is a real break in the
// derivation: fix the renderer or the strip, never the golden artefact. If it cannot
// be held at all, stop and reopen the two-field design ADR-0046 names, with the
// failing fixtures as the evidence (#279, spec #275).
func TestStructuralRenderingRoundTrip(t *testing.T) {
	golden := readGoldenRows(t)
	rendering := parser.NewHTMLParser(parser.WithStructuralRendering(true))

	for _, name := range goldenFixtureNames(t) {
		want, ok := golden[name]
		if !ok {
			continue // TestFlattenedTextGolden reports the missing row
		}
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(goldFixturesDir, name))
			if err != nil {
				t.Fatalf("read fixture %s: %v", name, err)
			}
			content, err := rendering.Parse(b)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}

			got := crawler.FlattenedText(content.MainContent)
			if got == want {
				return
			}
			offset, gotWindow, wantWindow := firstDiff(got, want)
			t.Errorf("the round-trip does not hold for %s: flattening the Structural Rendering gives %d bytes, the golden artefact has %d, first difference at byte %d\n got: %s\nwant: %s\n"+
				"the rendering may not reach SourceHash as different bytes than the flattened parser produced (ADR-0046)",
				name, len(got), len(want), offset, gotWindow, wantWindow)
		})
	}
}
