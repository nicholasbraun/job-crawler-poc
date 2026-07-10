package catalog_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func mustURL(t *testing.T, raw string) crawler.URL {
	t.Helper()
	u, err := crawler.NewURL(raw)
	if err != nil {
		t.Fatalf("error building url %q: %v", raw, err)
	}
	return u
}

func TestIdentify(t *testing.T) {
	tests := []struct {
		name           string
		url            string
		wantKey        string
		wantProvider   string
		wantPoliteness string
	}{
		{
			name:           "greenhouse board tenant slug from path",
			url:            "https://boards.greenhouse.io/acme/jobs/123",
			wantKey:        "greenhouse:acme",
			wantProvider:   "greenhouse",
			wantPoliteness: "boards.greenhouse.io",
		},
		{
			name:           "greenhouse job-boards host",
			url:            "https://job-boards.greenhouse.io/globex",
			wantKey:        "greenhouse:globex",
			wantProvider:   "greenhouse",
			wantPoliteness: "job-boards.greenhouse.io",
		},
		{
			name:           "lever tenant slug from path",
			url:            "https://jobs.lever.co/initech/posting",
			wantKey:        "lever:initech",
			wantProvider:   "lever",
			wantPoliteness: "jobs.lever.co",
		},
		{
			name:           "ashby tenant slug from path",
			url:            "https://jobs.ashbyhq.com/umbrella",
			wantKey:        "ashby:umbrella",
			wantProvider:   "ashby",
			wantPoliteness: "jobs.ashbyhq.com",
		},
		{
			name:           "recruitee tenant slug from subdomain",
			url:            "https://acme.recruitee.com/o/backend-engineer",
			wantKey:        "recruitee:acme",
			wantProvider:   "recruitee",
			wantPoliteness: "acme.recruitee.com",
		},
		{
			name:           "personio tenant slug from subdomain (.de)",
			url:            "https://acme.jobs.personio.de/job/42",
			wantKey:        "personio:acme",
			wantProvider:   "personio",
			wantPoliteness: "acme.jobs.personio.de",
		},
		{
			name:           "personio tenant slug from subdomain (.com)",
			url:            "https://globex.jobs.personio.com/",
			wantKey:        "personio:globex",
			wantProvider:   "personio",
			wantPoliteness: "globex.jobs.personio.com",
		},
		{
			name:           "workable path board",
			url:            "https://apply.workable.com/acme/j/ABC123",
			wantKey:        "workable:acme",
			wantProvider:   "workable",
			wantPoliteness: "apply.workable.com",
		},
		{
			name:           "workable subdomain tenant",
			url:            "https://globex.workable.com/jobs",
			wantKey:        "workable:globex",
			wantProvider:   "workable",
			wantPoliteness: "globex.workable.com",
		},
		{
			name:           "self-hosted falls back to eTLD+1",
			url:            "https://careers.acme.com/jobs/senior-go",
			wantKey:        "acme.com",
			wantProvider:   "",
			wantPoliteness: "careers.acme.com",
		},
		{
			name:           "self-hosted multi-level TLD (.co.uk)",
			url:            "https://jobs.acme.co.uk/vacancies",
			wantKey:        "acme.co.uk",
			wantProvider:   "",
			wantPoliteness: "jobs.acme.co.uk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := catalog.Identify(mustURL(t, tt.url))
			if got.CompanyKey != tt.wantKey {
				t.Errorf("CompanyKey: want %q, got %q", tt.wantKey, got.CompanyKey)
			}
			if got.ATSProvider != tt.wantProvider {
				t.Errorf("ATSProvider: want %q, got %q", tt.wantProvider, got.ATSProvider)
			}
			if got.PolitenessDomain != tt.wantPoliteness {
				t.Errorf("PolitenessDomain: want %q, got %q", tt.wantPoliteness, got.PolitenessDomain)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want catalog.Role
	}{
		{"greenhouse board root is a career page", "https://job-boards.greenhouse.io/xai", catalog.RoleCareerPage},
		{"greenhouse posting is a job listing", "https://job-boards.greenhouse.io/xai/jobs/123", catalog.RoleJobListing},
		{"lever board root is a career page", "https://jobs.lever.co/initech", catalog.RoleCareerPage},
		{"lever posting is a job listing", "https://jobs.lever.co/initech/2b1c-uuid", catalog.RoleJobListing},
		{"recruitee root is a career page", "https://acme.recruitee.com/", catalog.RoleCareerPage},
		{"recruitee posting is a job listing", "https://acme.recruitee.com/o/backend-engineer", catalog.RoleJobListing},
		{"personio posting is a job listing", "https://acme.jobs.personio.de/job/42", catalog.RoleJobListing},
		{"unrecognized host is unknown", "https://careers.acme.com/jobs", catalog.RoleUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalog.Classify(mustURL(t, tt.url)); got != tt.want {
				t.Errorf("Classify(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestCareerPageURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		want   string
		wantOK bool
	}{
		{"greenhouse posting collapses to board root", "https://job-boards.greenhouse.io/xai/jobs/123", "https://job-boards.greenhouse.io/xai", true},
		{"greenhouse pagination collapses to board root", "https://job-boards.greenhouse.io/acme?page=2", "https://job-boards.greenhouse.io/acme", true},
		{"lever posting collapses to board root", "https://jobs.lever.co/initech/2b1c-uuid", "https://jobs.lever.co/initech", true},
		{"recruitee posting collapses to host root", "https://acme.recruitee.com/o/backend-engineer", "https://acme.recruitee.com", true},
		{"self-hosted has no canonical ATS url", "https://careers.acme.com/jobs", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := catalog.CareerPageURL(mustURL(t, tt.url))
			if ok != tt.wantOK {
				t.Fatalf("CareerPageURL(%q) ok = %v, want %v", tt.url, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("CareerPageURL(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

// TestIdentifyDistinctTenantsSameHost is the ADR-0001 invariant: two tenants on
// one shared ATS host get distinct CompanyKeys but the same Politeness Domain.
func TestIdentifyDistinctTenantsSameHost(t *testing.T) {
	acme := catalog.Identify(mustURL(t, "https://boards.greenhouse.io/acme/jobs/1"))
	globex := catalog.Identify(mustURL(t, "https://boards.greenhouse.io/globex/jobs/2"))

	if acme.CompanyKey == globex.CompanyKey {
		t.Errorf("distinct tenants must not collapse: both keyed %q", acme.CompanyKey)
	}
	if acme.PolitenessDomain != globex.PolitenessDomain {
		t.Errorf("tenants on the same host must share politeness domain: %q vs %q",
			acme.PolitenessDomain, globex.PolitenessDomain)
	}
}

func TestIsAggregatorHost(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"builtin job board", "https://builtin.com/jobs", true},
		{"builtin city sibling", "https://builtinnyc.com/company/acme", true},
		{"getro portfolio board on a subdomain", "https://jobsinvc.getro.com/companies/acme", true},
		{"speedinvest portfolio board", "https://careers.speedinvest.com/companies/bitpanda", true},
		{"xing professional network", "https://www.xing.com/pages/acme", true},
		{"crunchboard job board", "https://www.crunchboard.com/jobs/123", true},
		{"match is case-insensitive", "https://BuiltIn.com/jobs", true},
		// A recognized single-tenant ATS board root is a legitimate hub, not an
		// aggregator -- its only defect is identity attribution (#46).
		{"smartrecruiters tenant is not an aggregator", "https://jobs.smartrecruiters.com/ScalableGmbH", false},
		{"join.com company board is not an aggregator", "https://join.com/companies/fugro", false},
		{"a company's own site is not an aggregator", "https://careers.acme.com/jobs", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := catalog.IsAggregatorHost(mustURL(t, tt.url)); got != tt.want {
				t.Errorf("IsAggregatorHost(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}
