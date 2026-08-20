package parser_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

const (
	// linkedFixturesMin is a non-vacuity tripwire for TestPromptVariantDropsLinkTargets:
	// 74 of the 90 committed fixtures carry at least one link marker today, so a renderer
	// that quietly stopped emitting links would satisfy "no target survives" trivially.
	linkedFixturesMin = 45

	// promptVariantMaxRatio bounds what the prompt variant costs against the Flattened
	// Text it replaces. Today it measures 1.081 (569008 bytes against 526159; the
	// targets-carrying form is 1.253, and ADR-0046 measured 1.10 / 1.43 over 36 live
	// pages). This is a tripwire, not an expectation: LLM_EXTRACT_MAX_CHARS is a fixed
	// 8000, so a rendering that grew past it would silently start showing the model less
	// page rather than failing anything.
	promptVariantMaxRatio = 1.5
)

// TestPromptVariantIsIdentityOnFlattenedText asserts #280's kill-switch criterion over
// all 90 committed classifier fixtures: with PARSE_STRUCTURAL_RENDERING off, what the
// extractor and the classifier prompts carry is byte for byte what they carried before
// the renderer existed.
//
// That is what makes the switch a real kill switch rather than a hope. It holds because
// every link marker carries "](" and no fixture's page text does; a failure here means
// page text carrying that pair reached the narrowing, and the fix is in the narrowing or
// the renderer, never in the golden artefact (ADR-0046).
func TestPromptVariantIsIdentityOnFlattenedText(t *testing.T) {
	golden := readGoldenRows(t)
	flattening := parser.NewHTMLParser() // the shipped default: no rendering

	for _, name := range goldenFixtureNames(t) {
		want, ok := golden[name]
		if !ok {
			continue // TestFlattenedTextGolden reports the missing row
		}
		t.Run(name, func(t *testing.T) {
			content, err := flattening.Parse(readFixture(t, name))
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}

			got := crawler.WithoutLinkTargets(content.MainContent)
			if got == want {
				return
			}
			offset, gotWindow, wantWindow := firstDiff(got, want)
			t.Errorf("the prompt variant is not the identity on %s with the rendering off: got %d bytes, the golden artefact has %d, first difference at byte %d\n got: %s\nwant: %s\n"+
				"with the kill switch off both prompts must carry exactly what they carry today (#280)",
				name, len(got), len(want), offset, gotWindow, wantWindow)
		})
	}
}

// TestPromptVariantDropsLinkTargets asserts the other half: with the rendering on, the
// prompts carry the page's structure and none of its link targets, at a size the
// prompt cap can still afford.
//
// The targets go because they are what makes the rendering expensive -- a link's text
// is signal, its href is a URL the model was never asked about -- while the brackets
// stay, so a rail of "similar jobs" links is still distinguishable from the posting's
// own prose (ADR-0046).
func TestPromptVariantDropsLinkTargets(t *testing.T) {
	golden := readGoldenRows(t)
	rendering := parser.NewHTMLParser(parser.WithStructuralRendering(true))

	scored, linked, narrowedBytes, fullBytes, flatBytes := 0, 0, 0, 0, 0
	for _, name := range goldenFixtureNames(t) {
		want, ok := golden[name]
		if !ok {
			continue // TestFlattenedTextGolden reports the missing row
		}
		content, err := rendering.Parse(readFixture(t, name))
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scored++
		if strings.Contains(content.MainContent, "](") {
			linked++
		}

		narrowed := crawler.WithoutLinkTargets(content.MainContent)
		if i := strings.Index(narrowed, "]("); i >= 0 {
			t.Errorf("a link target survived the narrowing in %s at byte %d: %s", name, i, window(narrowed, i))
		}
		narrowedBytes += len(narrowed)
		fullBytes += len(content.MainContent)
		flatBytes += len(want)
	}

	if linked < linkedFixturesMin {
		t.Errorf("only %d fixtures render a link marker, below the tripwire of %d (74 of 90 do today) -- the assertion above would pass vacuously",
			linked, linkedFixturesMin)
	}

	if flatBytes == 0 {
		t.Fatalf("the golden artefact records no bytes at all")
	}
	narrowedRatio := float64(narrowedBytes) / float64(flatBytes)
	t.Logf("over %d fixtures (%d of them carrying a link marker): Flattened Text %d bytes, rendering with targets %d (%.3fx), prompt variant %d (%.3fx)",
		scored, linked, flatBytes, fullBytes, float64(fullBytes)/float64(flatBytes), narrowedBytes, narrowedRatio)
	if narrowedRatio > promptVariantMaxRatio {
		t.Errorf("the prompt variant costs %.3fx the Flattened Text, past the tripwire of %.2f (it measured 1.081 when #280 shipped) -- at a fixed prompt cap that shows the model less page",
			narrowedRatio, promptVariantMaxRatio)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(goldFixturesDir, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return b
}
