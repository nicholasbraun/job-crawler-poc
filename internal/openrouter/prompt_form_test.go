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
		// keepsParens marks the cases where a "](" legitimately survives: a
		// non-link marker glued to page text that opens with a parenthesis. The
		// pair is only a link target when a link rule claims it (#289).
		keepsParens bool
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
			in:       "-\v[Apply now](/apply)\nTASKS",
			maxChars: 100,
			want:     "-\v[Apply now]\nTASKS",
		},
		{
			// #289: every marker ends in "]", so a link rule on its own claimed an
			// image or a control glued to a page parenthetical and deleted the
			// page's own words. "(m/w/d)" is exactly the token that marks a German
			// role title, so this was a real loss in the prompt the change exists
			// to improve.
			name:        "a parenthetical after an image marker is page text, not a target",
			in:          "#\vSales Manager[img: Acme logo](m/w/d)\nBerlin",
			maxChars:    200,
			want:        "#\vSales Manager[img: Acme logo](m/w/d)\nBerlin",
			keepsParens: true,
		},
		{
			name:        "a parenthetical after a control marker survives too",
			in:          "[input text: Vorname](optional) Nachname",
			maxChars:    200,
			want:        "[input text: Vorname](optional) Nachname",
			keepsParens: true,
		},
		{
			name:        "a parenthetical after a button survives too",
			in:          "[button: Senden](freiwillig)",
			maxChars:    200,
			want:        "[button: Senden](freiwillig)",
			keepsParens: true,
		},
		{
			// ...while a real link still loses its target, including one whose text
			// holds a bracketed run.
			name:     "a genuine link still drops its target",
			in:       "[Fixed Term [12 Months]](/careers/468) (m/w/d)",
			maxChars: 200,
			want:     "[Fixed Term [12 Months]] (m/w/d)",
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
			if !tt.keepsParens && strings.Contains(got, "](") {
				t.Errorf("PromptForm(%q, %d) = %q, which still carries a link target", tt.in, tt.maxChars, got)
			}
			if n := utf8.RuneCountInString(got); n > tt.maxChars {
				t.Errorf("PromptForm(%q, %d) returned %d runes, over the cap", tt.in, tt.maxChars, n)
			}
		})
	}
}
