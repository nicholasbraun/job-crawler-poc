package catalog_test

import (
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestCanonicalURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"http coerced to https", "http://careers.acme.com/jobs", "https://careers.acme.com/jobs"},
		{"https left unchanged", "https://careers.acme.com/jobs", "https://careers.acme.com/jobs"},
		{"root slash stripped", "https://careers.acme.com/", "https://careers.acme.com"},
		{"no path unchanged", "https://careers.acme.com", "https://careers.acme.com"},
		{"trailing slash stripped", "https://careers.acme.com/careers/", "https://careers.acme.com/careers"},
		{"deep trailing slash stripped", "https://careers.acme.com/careers/open/", "https://careers.acme.com/careers/open"},
		{"single-param query stripped", "https://careers.acme.com/careers?p=6774", "https://careers.acme.com/careers"},
		{"multi-param query stripped", "https://malta.acme.com/jobs?country=203&p=6774", "https://malta.acme.com/jobs"},
		{"bare question mark stripped", "https://careers.acme.com/jobs?", "https://careers.acme.com/jobs"},
		{"XSS fuzz query discarded", "https://acme.com/careers?redirect=%3Cscript%3Ealert(1)%3C/script%3E", "https://acme.com/careers"},
		{"SQLi fuzz query discarded", "https://acme.com/jobs?id=1%27%20OR%20%271%27=%271", "https://acme.com/jobs"},
		{"http + query + trailing slash combined", "http://acme.com/careers/?ref=twitter", "https://acme.com/careers"},
		{"greenhouse board root unchanged", "https://job-boards.greenhouse.io/xai", "https://job-boards.greenhouse.io/xai"},
		{"recruitee host root unchanged", "https://acme.recruitee.com", "https://acme.recruitee.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catalog.CanonicalURL(tt.in)
			if got != tt.want {
				t.Errorf("CanonicalURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Idempotent: re-canonicalising the output is a no-op (AC #4, so the
			// Catalog Doctor's second pass over already-canonical rows changes nothing).
			if again := catalog.CanonicalURL(got); again != got {
				t.Errorf("CanonicalURL not idempotent: CanonicalURL(%q) = %q", got, again)
			}
			// No query string ever survives (AC #2).
			if strings.Contains(got, "?") {
				t.Errorf("CanonicalURL(%q) = %q still carries a query string", tt.in, got)
			}
		})
	}
}
