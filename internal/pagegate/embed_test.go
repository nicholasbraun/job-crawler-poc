package pagegate_test

import (
	"reflect"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
)

func TestATSEmbedTenants(t *testing.T) {
	tests := []struct {
		name    string
		content *crawler.Content
		want    []pagegate.ATSEmbedRef
	}{
		{
			// A Greenhouse script embed fires only alongside its board-container
			// marker; when present, its ?for tenant resolves.
			name: "greenhouse script with its board container resolves the tenant",
			content: &crawler.Content{
				Embeds:     []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
				ElementIDs: []string{"grnhse_app"},
			},
			want: []pagegate.ATSEmbedRef{{Provider: "greenhouse", Tenant: "acme"}},
		},
		{
			// The same script without the marker does not fire (a site-wide embed
			// script in a shared template), so nothing is listed.
			name: "greenhouse script without its board container fires nothing",
			content: &crawler.Content{
				Embeds: []crawler.Embed{{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"}},
			},
			want: []pagegate.ATSEmbedRef{},
		},
		{
			// An iframe to a known ATS host fires with no marker; the subdomain label
			// is the tenant.
			name: "personio iframe resolves the subdomain tenant",
			content: &crawler.Content{
				Embeds: []crawler.Embed{{Src: "https://acme.jobs.personio.de/search", IsFrame: true}},
			},
			want: []pagegate.ATSEmbedRef{{Provider: "personio", Tenant: "acme"}},
		},
		{
			name: "unrecognized-host iframe fires nothing",
			content: &crawler.Content{
				Embeds: []crawler.Embed{{Src: "https://www.youtube.com/embed/xyz", IsFrame: true}},
			},
			want: []pagegate.ATSEmbedRef{},
		},
		{
			// The fail-safe: an unrecognized host earns no credit even when a real
			// provider marker happens to be present on the page.
			name: "unrecognized-host script fires nothing even with a marker present",
			content: &crawler.Content{
				Embeds:     []crawler.Embed{{Src: "https://cdn.example.com/widget.js"}},
				ElementIDs: []string{"grnhse_app"},
			},
			want: []pagegate.ATSEmbedRef{},
		},
		{
			name: "two firing embeds resolve both tenants",
			content: &crawler.Content{
				Embeds: []crawler.Embed{
					{Src: "https://boards.greenhouse.io/embed/job_board/js?for=acme"},
					{Src: "https://globex.jobs.personio.de/search", IsFrame: true},
				},
				ElementIDs: []string{"grnhse_app"},
			},
			want: []pagegate.ATSEmbedRef{
				{Provider: "greenhouse", Tenant: "acme"},
				{Provider: "personio", Tenant: "globex"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pagegate.ATSEmbedTenants(tt.content)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ATSEmbedTenants() = %v, want %v", got, tt.want)
			}
		})
	}
}
