package openrouter_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
)

// TestPromptFormNarrowsThenCaps pins the one property both prompts depend on: the
// link targets are gone before the cap runs, so a cut can never leave a dangling
// "](" in the prompt, and the cap counts runes rather than bytes (ADR-0046).
func TestPromptFormNarrowsThenCaps(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		maxChars int
		want     string
	}{
		{
			// Flattened Text carries no markers, so the narrowing is the identity: the
			// prompt with PARSE_STRUCTURAL_RENDERING off is byte-identical to the one
			// that predates the renderer.
			name:     "flattened text passes through untouched",
			in:       "plain page words with no markers at all",
			maxChars: 100,
			want:     "plain page words with no markers at all",
		},
		{
			name:     "link target dropped, brackets kept",
			in:       "-\t[Apply now](/apply)\nTASKS",
			maxChars: 100,
			want:     "-\t[Apply now]\nTASKS",
		},
		{
			// The whole reason the two steps live in one function: capping first would
			// cut inside "(/a/very/long/target)".
			name:     "narrowing happens before the cap",
			in:       "[Apply](/a/very/long/target)X",
			maxChars: 8,
			want:     "[Apply]X",
		},
		{
			name:     "cap counts runes, not bytes",
			in:       strings.Repeat("ü", 10),
			maxChars: 5,
			want:     strings.Repeat("ü", 5),
		},
		{
			// Only links are narrowed. A form control is exactly what the model needs to
			// read a role picker as a picker, so it must survive untouched.
			name:     "form-control markers survive the narrowing",
			in:       "Position [input checkbox: role] Sales Manager",
			maxChars: 100,
			want:     "Position [input checkbox: role] Sales Manager",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := openrouter.PromptForm(tt.in, tt.maxChars)
			if got != tt.want {
				t.Errorf("PromptForm(%q, %d) = %q, want %q", tt.in, tt.maxChars, got, tt.want)
			}
			if strings.Contains(got, "](") {
				t.Errorf("PromptForm(%q, %d) = %q, which still carries a link target", tt.in, tt.maxChars, got)
			}
			if n := utf8.RuneCountInString(got); n > tt.maxChars {
				t.Errorf("PromptForm(%q, %d) returned %d runes, over the cap", tt.in, tt.maxChars, n)
			}
		})
	}
}
