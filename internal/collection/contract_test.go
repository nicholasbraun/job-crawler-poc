package collection_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// recordingTransport captures the host of the first request it sees and replies with
// an empty 200 body, so a fetcher's URL construction can be asserted without a real
// network call. The reply need not decode — the host is recorded before it is
// returned, so a later decode error is irrelevant to the assertion.
type recordingTransport struct{ host string }

func (rt *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.host = req.URL.Host
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     make(http.Header),
	}, nil
}

// TestSubdomainProviderHostContract pins the catalog↔fetcher contract for the
// subdomain-host ATS providers: the tenant slug RouteSeeds hands a fetcher (the
// leftmost catalog label, trimmed from the "provider:slug" CompanyKey) MUST
// reconstruct the tenant's real board host when the fetcher templates its default
// base URL. Every fetcher's own unit tests pass the full host, so only this
// end-to-end check catches a default base that drops the provider suffix — e.g. a
// bare "https://{tenant}" builds the unresolvable host "https://demo/…" for
// softgarden:demo, silently breaking every board fetch while the green unit suite
// masks it. Manatal/Greenhouse/Lever are excluded on purpose: their slug is a path
// segment, not a host label, so the host is fixed regardless of tenant.
func TestSubdomainProviderHostContract(t *testing.T) {
	// build wires each provider's fetcher with its DEFAULT base URL and a recording
	// client, so the assertion exercises the real production host construction.
	builders := map[string]func(*http.Client) ats.BoardFetcher{
		ats.ProviderSoftgarden: func(c *http.Client) ats.BoardFetcher {
			return ats.NewSoftgardenFetcher(ats.WithSoftgardenHTTPClient(c))
		},
		ats.ProviderTeamtailor: func(c *http.Client) ats.BoardFetcher {
			return ats.NewTeamtailorFetcher(ats.WithTeamtailorHTTPClient(c))
		},
		ats.ProviderRecruitee: func(c *http.Client) ats.BoardFetcher {
			return ats.NewRecruiteeFetcher(ats.WithRecruiteeHTTPClient(c))
		},
	}

	// hasFetcher mirrors the ATS Fetch lane's real registry predicate.
	reg := ats.NewDefaultRegistry()
	hasFetcher := func(provider string) bool {
		_, ok := reg.Fetcher(provider)
		return ok
	}

	cases := []struct {
		name     string
		seedURL  string // a tenant board URL for a subdomain-host provider
		wantHost string // the board host the fetcher must request from the routed slug
	}{
		{"softgarden", "https://demo.career.softgarden.de/jobs", "demo.career.softgarden.de"},
		{"teamtailor", "https://acme.teamtailor.com/jobs", "acme.teamtailor.com"},
		{"recruitee", "https://acme.recruitee.com/careers", "acme.recruitee.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// RouteSeeds resolves the seed via catalog.Identify and produces the slug
			// exactly as the production lane does — the value under contract.
			_, tasks, _ := collection.RouteSeeds([]crawler.CollectionSeed{
				{URL: tc.seedURL, CompanyKey: "owner.example"},
			}, hasFetcher)
			if len(tasks) != 1 {
				t.Fatalf("RouteSeeds produced %d tasks, want 1 (a recognized subdomain tenant with a fetcher)", len(tasks))
			}
			task := tasks[0]

			build, ok := builders[task.Provider]
			if !ok {
				t.Fatalf("no fetcher builder for provider %q", task.Provider)
			}
			rt := &recordingTransport{}
			fetcher := build(&http.Client{Transport: rt})

			// The empty reply body may fail to decode; only the requested host matters.
			_, _ = fetcher.Fetch(t.Context(), task.TenantSlug)

			if rt.host != tc.wantHost {
				t.Errorf("provider %s: fetcher requested host %q from routed slug %q, want %q — the tenant slug must reconstruct the real board host",
					task.Provider, rt.host, task.TenantSlug, tc.wantHost)
			}
		})
	}
}
