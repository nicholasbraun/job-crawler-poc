package bench_test

import (
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

func TestSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"greenhouse", "https://boards.greenhouse.io/acme", "boards-greenhouse-io-acme"},
		{"www-stripped", "https://www.linkedin.com/jobs/acme", "linkedin-com-jobs-acme"},
		{"path-with-hyphens", "https://acme.example/careers/senior-go-engineer", "acme-example-careers-senior-go-engineer"},
		{"root-path", "https://example.com/", "example-com"},
		{"empty", "", "page"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bench.Slug(tt.in); got != tt.want {
				t.Errorf("Slug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}

	t.Run("truncation", func(t *testing.T) {
		got := bench.Slug("https://ex.com/" + strings.Repeat("a", 100))
		if len(got) > 60 {
			t.Errorf("len(Slug) = %d, want <= 60", len(got))
		}
		if !strings.HasPrefix(got, "ex-com-a") {
			t.Errorf("Slug = %q, want prefix %q", got, "ex-com-a")
		}
	})
}

func TestNextIndex(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want int
	}{
		{"empty", []string{}, 0},
		{"two-sequential", []string{"0000-a.html", "0001-b.html"}, 2},
		{"ignores-unnumbered", []string{"greenhouse_acme.html", "0003-x.html"}, 4},
		{"unordered", []string{"0007-a.html", "0002-b.html"}, 8},
		{"non-numeric-prefix", []string{"abc-x.html"}, 0},
		{"with-dir-prefix", []string{"pages/0005-x.html"}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bench.NextIndex(tt.in); got != tt.want {
				t.Errorf("NextIndex(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestFixtureName(t *testing.T) {
	tests := []struct {
		name  string
		index int
		slug  string
		want  string
	}{
		{"zero", 0, "acme", "0000-acme.html"},
		{"forty-two", 42, "x-y", "0042-x-y.html"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bench.FixtureName(tt.index, tt.slug); got != tt.want {
				t.Errorf("FixtureName(%d, %q) = %q, want %q", tt.index, tt.slug, got, tt.want)
			}
		})
	}
}
