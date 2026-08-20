package parser_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

const (
	// fingerprintVersion is the parser.RendererStructural value the fingerprint
	// below was taken at. The two move together: a grammar change moves the hash,
	// and the version has to move with it, in the same commit.
	fingerprintVersion = "structural-v2"

	// structuralRenderingFingerprint is sha256 over every committed classifier
	// fixture's Structural Rendering, in sorted fixture order.
	structuralRenderingFingerprint = "a27db469bcfcfe4119dbcf23d035a42e01fd2df4e03c4fce090dcdd8ab0a8a47"
)

// TestRendererIDNamesTheRendererInUse asserts a parser states which renderer it
// writes Content.MainContent with, so a captured page can be attributed to one
// (#281). The two identifiers must differ: a record written with the kill switch
// off is otherwise indistinguishable from one written with it on, and the two
// renderings then mix inside a single gold-set drawing (ADR-0046).
func TestRendererIDNamesTheRendererInUse(t *testing.T) {
	if parser.RendererFlattened == "" || parser.RendererStructural == "" {
		t.Fatalf("renderer identifiers must be non-empty, got %q and %q", parser.RendererFlattened, parser.RendererStructural)
	}
	if parser.RendererFlattened == parser.RendererStructural {
		t.Fatalf("both renderers are called %q; the two populations must be distinguishable", parser.RendererFlattened)
	}

	if got := parser.NewHTMLParser().RendererID(); got != parser.RendererFlattened {
		t.Errorf("default parser reports %q, want %q -- the shipped default is the Flattened Text", got, parser.RendererFlattened)
	}
	if got := parser.NewHTMLParser(parser.WithStructuralRendering(true)).RendererID(); got != parser.RendererStructural {
		t.Errorf("structural parser reports %q, want %q", got, parser.RendererStructural)
	}
	if got := parser.NewHTMLParser(parser.WithStructuralRendering(false)).RendererID(); got != parser.RendererFlattened {
		t.Errorf("parser with the switch explicitly off reports %q, want %q", got, parser.RendererFlattened)
	}
}

// TestStructuralRendererFingerprint makes the version bump mechanical rather than
// remembered (#281). It hashes the Structural Rendering of all committed classifier
// fixtures and fails the build the moment that output moves, so a grammar change
// cannot ship under a version that claims the old output.
//
// This is NOT a golden artefact to regenerate: the hash records a fact about a
// renderer whose identifier is stamped on gold-set rows, and rewriting it alone
// makes those rows name a renderer that never produced them. There is deliberately
// no -update flag.
func TestStructuralRendererFingerprint(t *testing.T) {
	if parser.RendererStructural != fingerprintVersion {
		t.Fatalf("parser.RendererStructural is %q but this fingerprint was taken at %q -- update both together, never one alone (ADR-0046, #281)",
			parser.RendererStructural, fingerprintVersion)
	}

	p := parser.NewHTMLParser(parser.WithStructuralRendering(true))
	h := sha256.New()
	names := goldenFixtureNames(t)
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(goldFixturesDir, name))
		if err != nil {
			t.Fatalf("read fixture %s: %v", name, err)
		}
		content, err := p.Parse(b)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(content.MainContent))
		h.Write([]byte{0})
	}

	got := hex.EncodeToString(h.Sum(nil))
	if got != structuralRenderingFingerprint {
		t.Fatalf("the renderer's output over the %d committed fixtures moved: got %s, recorded %s\n"+
			"if the FIXTURE SET changed, update structuralRenderingFingerprint alone.\n"+
			"if the RENDERER's rules changed, bump parser.RendererStructural and fingerprintVersion in the same commit, then update the hash -- "+
			"a stamped gold-set row has to name the renderer that really produced it (ADR-0046, #281)",
			len(names), got, structuralRenderingFingerprint)
	}
}
