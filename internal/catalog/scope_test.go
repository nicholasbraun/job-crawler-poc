package catalog_test

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func TestInScope(t *testing.T) {
	tests := []struct {
		name  string
		scope string
		child string
		want  bool
	}{
		{
			name:  "same registrable domain passes",
			scope: "acme.com",
			child: "https://acme.com/jobs/123",
			want:  true,
		},
		{
			name:  "subdomain passes",
			scope: "acme.com",
			child: "https://careers.acme.com/jobs",
			want:  true,
		},
		{
			name:  "ATS same tenant passes",
			scope: "greenhouse:acme",
			child: "https://boards.greenhouse.io/acme/jobs/5",
			want:  true,
		},
		{
			name:  "sibling ATS tenant rejected",
			scope: "greenhouse:acme",
			child: "https://boards.greenhouse.io/globex/jobs/5",
			want:  false,
		},
		{
			name:  "different registrable domain rejected",
			scope: "acme.com",
			child: "https://evilcorp.com/jobs",
			want:  false,
		},
		{
			name:  "off-catalog host rejected",
			scope: "acme.com",
			child: "https://talish.dev/portfolio",
			want:  false,
		},
		{
			// ADR-0021: the fence is stricter than a blanket ATS-host allowlist. A
			// self-hosted seed keyed on its registrable domain does not follow a link
			// onto a known ATS host, because Identify keys that host to its own
			// provider:tenant ("greenhouse:acme"), never to "acme.com".
			name:  "self-hosted scope does not follow onto a known ATS host",
			scope: "acme.com",
			child: "https://boards.greenhouse.io/acme",
			want:  false,
		},
		{
			// #202: a shared-hosting seed is keyed on its full host, so a different
			// path on the same tenant subdomain stays in scope.
			name:  "shared-host same tenant different path passes",
			scope: "acme.substack.com",
			child: "https://acme.substack.com/archive",
			want:  true,
		},
		{
			// The core #202 fix: a sibling newsletter on the same platform is a
			// different tenant, so it is now OUT of scope (previously the seed keyed
			// on "substack.com" and swept every tenant in).
			name:  "shared-host sibling tenant rejected",
			scope: "acme.substack.com",
			child: "https://bob.substack.com/p/x",
			want:  false,
		},
		{
			// The legacy whole-platform key no longer matches a full-host tenant key,
			// so a stored "substack.com" scope confines to nothing on the platform.
			name:  "legacy whole-platform key no longer matches a tenant",
			scope: "substack.com",
			child: "https://acme.substack.com/p/x",
			want:  false,
		},
		{
			// Guards the 22.4% case: a real company is not a shared host, so a sibling
			// subdomain of its eTLD+1 stays in scope.
			name:  "real-company sibling subdomain stays in scope",
			scope: "acme.com",
			child: "https://jobs.acme.com/openings",
			want:  true,
		},
		{
			name:  "empty scope roams",
			scope: "",
			child: "https://anything.dev/x",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catalog.InScope(tt.scope, mustURL(t, tt.child))
			if got != tt.want {
				t.Errorf("InScope(%q, %q) = %v, want %v", tt.scope, tt.child, got, tt.want)
			}
		})
	}
}
