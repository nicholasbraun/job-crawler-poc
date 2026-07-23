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
			name:           "smartrecruiters careers host slugs the tenant (lowercased)",
			url:            "https://careers.smartrecruiters.com/BoschGroup",
			wantKey:        "smartrecruiters:boschgroup",
			wantProvider:   "smartrecruiters",
			wantPoliteness: "careers.smartrecruiters.com",
		},
		{
			name:           "smartrecruiters posting host keys to the same tenant",
			url:            "https://jobs.smartrecruiters.com/BoschGroup/744000138449489--process-development-engineer-em",
			wantKey:        "smartrecruiters:boschgroup",
			wantProvider:   "smartrecruiters",
			wantPoliteness: "jobs.smartrecruiters.com",
		},
		{
			name:           "join.com prefix rule: tenant root",
			url:            "https://join.com/companies/fugro",
			wantKey:        "join:fugro",
			wantProvider:   "join",
			wantPoliteness: "join.com",
		},
		{
			name:           "join.com prefix rule: posting still attributes to tenant",
			url:            "https://join.com/companies/zara/12345-senior-engineer",
			wantKey:        "join:zara",
			wantProvider:   "join",
			wantPoliteness: "join.com",
		},
		{
			name:           "join.com non-tenant path falls back to eTLD+1",
			url:            "https://join.com/blog",
			wantKey:        "join.com",
			wantProvider:   "",
			wantPoliteness: "join.com",
		},
		{
			name:           "bamboohr subdomain tenant",
			url:            "https://acme.bamboohr.com/",
			wantKey:        "bamboohr:acme",
			wantProvider:   "bamboohr",
			wantPoliteness: "acme.bamboohr.com",
		},
		{
			name:           "teamtailor subdomain tenant",
			url:            "https://acme.teamtailor.com/",
			wantKey:        "teamtailor:acme",
			wantProvider:   "teamtailor",
			wantPoliteness: "acme.teamtailor.com",
		},
		{
			name:           "icims subdomain tenant",
			url:            "https://acme.icims.com/",
			wantKey:        "icims:acme",
			wantProvider:   "icims",
			wantPoliteness: "acme.icims.com",
		},
		{
			name:           "indigo subdomain tenant",
			url:            "https://acme.indigo.jobs/",
			wantKey:        "indigo:acme",
			wantProvider:   "indigo",
			wantPoliteness: "acme.indigo.jobs",
		},
		{
			name:           "hibob subdomain tenant (multi-label suffix)",
			url:            "https://acme.careers.hibob.com/",
			wantKey:        "hibob:acme",
			wantProvider:   "hibob",
			wantPoliteness: "acme.careers.hibob.com",
		},
		{
			name:           "haileyhr subdomain tenant (multi-label suffix)",
			url:            "https://acme.careers.haileyhr.app/",
			wantKey:        "haileyhr:acme",
			wantProvider:   "haileyhr",
			wantPoliteness: "acme.careers.haileyhr.app",
		},
		{
			name:           "softgarden tenant slug from subdomain",
			url:            "https://demo.career.softgarden.de/jobs/32701543/Communications-Manager-f-m-d-/",
			wantKey:        "softgarden:demo",
			wantProvider:   "softgarden",
			wantPoliteness: "demo.career.softgarden.de",
		},
		{
			// The .softgarden.io form is not confirmed to serve the feed, so it is not
			// recognized and falls to eTLD+1 with an empty provider.
			name:           "softgarden .io form is not recognized",
			url:            "https://demo.softgarden.io/",
			wantKey:        "softgarden.io",
			wantProvider:   "",
			wantPoliteness: "demo.softgarden.io",
		},
		{
			// Custom-domain CNAME tenants serve the identical feed but aren't
			// host-recognizable, so they fall to eTLD+1 (accepted, like Recruitee).
			name:           "softgarden custom domain falls to eTLD+1",
			url:            "https://career.nuvisan.com/",
			wantKey:        "nuvisan.com",
			wantProvider:   "",
			wantPoliteness: "career.nuvisan.com",
		},
		{
			// The bare suffix carries no tenant label, so it is not slugged.
			name:           "softgarden bare suffix has no tenant",
			url:            "https://career.softgarden.de/",
			wantKey:        "softgarden.de",
			wantProvider:   "",
			wantPoliteness: "career.softgarden.de",
		},
		{
			name:           "manatal subdomain tenant",
			url:            "https://acme.careers-page.com/",
			wantKey:        "manatal:acme",
			wantProvider:   "manatal",
			wantPoliteness: "acme.careers-page.com",
		},
		{
			name:           "manatal legacy path tenant",
			url:            "https://www.careers-page.com/acme",
			wantKey:        "manatal:acme",
			wantProvider:   "manatal",
			wantPoliteness: "www.careers-page.com",
		},
		{
			name:           "manatal posting on subdomain attributes to tenant",
			url:            "https://acme.careers-page.com/jobs/2b1c-uuid",
			wantKey:        "manatal:acme",
			wantProvider:   "manatal",
			wantPoliteness: "acme.careers-page.com",
		},
		{
			// www is the legacy board host (recovered by the pathRule), not a tenant
			// label, so the bare host mints no tenant while the path tenant above resolves.
			name:           "manatal bare www is not a tenant",
			url:            "https://www.careers-page.com/",
			wantKey:        "careers-page.com",
			wantProvider:   "",
			wantPoliteness: "www.careers-page.com",
		},
		{
			name:           "manatal api host is not a tenant",
			url:            "https://api.careers-page.com/open/v1/career-pages/acme/job-posts",
			wantKey:        "careers-page.com",
			wantProvider:   "",
			wantPoliteness: "api.careers-page.com",
		},
		{
			name:           "manatal customer CNAME host is not a tenant",
			url:            "https://customer.careers-page.com/",
			wantKey:        "careers-page.com",
			wantProvider:   "",
			wantPoliteness: "customer.careers-page.com",
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
		{"smartrecruiters careers root is a career page", "https://careers.smartrecruiters.com/BoschGroup", catalog.RoleCareerPage},
		{"smartrecruiters posting is a job listing", "https://jobs.smartrecruiters.com/BoschGroup/744000138449489--process-development-engineer-em", catalog.RoleJobListing},
		{"unrecognized host is unknown", "https://careers.acme.com/jobs", catalog.RoleUnknown},
		{"join.com tenant root is a career page", "https://join.com/companies/fugro", catalog.RoleCareerPage},
		{"join.com posting is a job listing", "https://join.com/companies/zara/12345-senior-engineer", catalog.RoleJobListing},
		{"join.com index without tenant is a job listing", "https://join.com/companies", catalog.RoleJobListing},
		{"join.com non-tenant path is unknown", "https://join.com/blog", catalog.RoleUnknown},
		{"bamboohr root is a career page", "https://acme.bamboohr.com/", catalog.RoleCareerPage},
		{"bamboohr posting is a job listing", "https://acme.bamboohr.com/jobs/123", catalog.RoleJobListing},
		{"teamtailor root is a career page", "https://acme.teamtailor.com/", catalog.RoleCareerPage},
		{"teamtailor posting is a job listing", "https://acme.teamtailor.com/jobs/123", catalog.RoleJobListing},
		{"icims root is a career page", "https://acme.icims.com/", catalog.RoleCareerPage},
		{"icims posting is a job listing", "https://acme.icims.com/jobs/123", catalog.RoleJobListing},
		{"indigo root is a career page", "https://acme.indigo.jobs/", catalog.RoleCareerPage},
		{"indigo posting is a job listing", "https://acme.indigo.jobs/jobs/123", catalog.RoleJobListing},
		{"hibob root is a career page", "https://acme.careers.hibob.com/", catalog.RoleCareerPage},
		{"hibob posting is a job listing", "https://acme.careers.hibob.com/jobs/123", catalog.RoleJobListing},
		{"haileyhr root is a career page", "https://acme.careers.haileyhr.app/", catalog.RoleCareerPage},
		{"haileyhr posting is a job listing", "https://acme.careers.haileyhr.app/jobs/123", catalog.RoleJobListing},
		{"softgarden root is a career page", "https://demo.career.softgarden.de/", catalog.RoleCareerPage},
		{"softgarden posting is a job listing", "https://demo.career.softgarden.de/jobs/32701543/Communications-Manager-f-m-d-/", catalog.RoleJobListing},
		{"manatal subdomain root is a career page", "https://acme.careers-page.com/", catalog.RoleCareerPage},
		{"manatal subdomain posting is a job listing", "https://acme.careers-page.com/jobs/123", catalog.RoleJobListing},
		{"manatal legacy path root is a career page", "https://www.careers-page.com/acme", catalog.RoleCareerPage},
		{"manatal legacy path posting is a job listing", "https://www.careers-page.com/acme/jobs/123", catalog.RoleJobListing},
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
		{"smartrecruiters posting collapses to board root (slug lowercased)", "https://jobs.smartrecruiters.com/BoschGroup/744000138449489--process-development-engineer-em", "https://jobs.smartrecruiters.com/boschgroup", true},
		{"recruitee posting collapses to host root", "https://acme.recruitee.com/o/backend-engineer", "https://acme.recruitee.com", true},
		{"join.com posting collapses to companies/{slug} root", "https://join.com/companies/zara/12345-senior-engineer", "https://join.com/companies/zara", true},
		{"join.com tenant root is already canonical", "https://join.com/companies/zara", "https://join.com/companies/zara", true},
		{"bamboohr posting collapses to host root", "https://acme.bamboohr.com/jobs/999", "https://acme.bamboohr.com", true},
		{"teamtailor posting collapses to host root", "https://acme.teamtailor.com/jobs/999", "https://acme.teamtailor.com", true},
		{"icims posting collapses to host root", "https://acme.icims.com/jobs/999", "https://acme.icims.com", true},
		{"indigo posting collapses to host root", "https://acme.indigo.jobs/jobs/999", "https://acme.indigo.jobs", true},
		{"hibob posting collapses to host root", "https://acme.careers.hibob.com/jobs/999", "https://acme.careers.hibob.com", true},
		{"haileyhr posting collapses to host root", "https://acme.careers.haileyhr.app/jobs/999", "https://acme.careers.haileyhr.app", true},
		{"softgarden posting collapses to host root", "https://demo.career.softgarden.de/jobs/32701543/x/", "https://demo.career.softgarden.de", true},
		{"manatal subdomain posting collapses to host root", "https://acme.careers-page.com/jobs/999", "https://acme.careers-page.com", true},
		{"manatal legacy path posting collapses to path root", "https://www.careers-page.com/acme/jobs/999", "https://www.careers-page.com/acme", true},
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

func TestATSProviderForHost(t *testing.T) {
	tests := []struct {
		name         string
		host         string
		wantProvider string
		wantOK       bool
	}{
		{"greenhouse board host", "boards.greenhouse.io", "greenhouse", true},
		{"greenhouse job-boards host", "job-boards.greenhouse.io", "greenhouse", true},
		{"ashby board host", "jobs.ashbyhq.com", "ashby", true},
		{"lever board host", "jobs.lever.co", "lever", true},
		{"smartrecruiters careers host", "careers.smartrecruiters.com", "smartrecruiters", true},
		{"smartrecruiters posting host", "jobs.smartrecruiters.com", "smartrecruiters", true},
		{"bamboohr tenant subdomain", "acme.bamboohr.com", "bamboohr", true},
		{"personio tenant subdomain (.de)", "globex.jobs.personio.de", "personio", true},
		{"personio tenant subdomain (.com)", "globex.jobs.personio.com", "personio", true},
		{"recruitee tenant subdomain", "acme.recruitee.com", "recruitee", true},
		{"softgarden tenant subdomain", "demo.career.softgarden.de", "softgarden", true},
		// Custom-domain CNAME tenants aren't host-recognizable (pins the limitation).
		{"softgarden custom domain is not an ATS host", "career.nuvisan.com", "", false},
		{"manatal subdomain tenant host", "acme.careers-page.com", "manatal", true},
		// The legacy board host is recognized via the pathRule (it is a board host),
		// even though Identify declines to mint a tenant for the bare host.
		{"manatal legacy path host", "www.careers-page.com", "manatal", true},
		{"manatal api host is not a tenant board host", "api.careers-page.com", "", false},
		{"manatal customer CNAME host is not a tenant board host", "customer.careers-page.com", "", false},
		{"match is case-insensitive", "BOARDS.GREENHOUSE.IO", "greenhouse", true},
		{"a company's own host is not an ATS host", "www.acme.com", "", false},
		{"empty host is not an ATS host", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, ok := catalog.ATSProviderForHost(tt.host)
			if ok != tt.wantOK {
				t.Fatalf("ATSProviderForHost(%q) ok = %v, want %v", tt.host, ok, tt.wantOK)
			}
			if provider != tt.wantProvider {
				t.Errorf("ATSProviderForHost(%q) provider = %q, want %q", tt.host, provider, tt.wantProvider)
			}
		})
	}
}

func TestATSBoardContainerMarker(t *testing.T) {
	tests := []struct {
		name       string
		provider   string
		wantMarker string
		wantOK     bool
	}{
		{"greenhouse renders into grnhse_app", "greenhouse", "grnhse_app", true},
		{"ashby renders into ashby_embed", "ashby", "ashby_embed", true},
		{"bamboohr renders into BambooHR", "bamboohr", "BambooHR", true},
		{"lever has no curated marker (hosted-board classify)", "lever", "", false},
		{"personio has no marker (iframe-based)", "personio", "", false},
		{"unknown provider has no marker", "unknown", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			marker, ok := catalog.ATSBoardContainerMarker(tt.provider)
			if ok != tt.wantOK {
				t.Fatalf("ATSBoardContainerMarker(%q) ok = %v, want %v", tt.provider, ok, tt.wantOK)
			}
			if marker != tt.wantMarker {
				t.Errorf("ATSBoardContainerMarker(%q) marker = %q, want %q", tt.provider, marker, tt.wantMarker)
			}
		})
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
		{"hv capital portfolio board on its .capital gTLD", "https://www.hv.capital/portfolio", true},
		{"xing professional network", "https://www.xing.com/pages/acme", true},
		{"linkedin professional network", "https://www.linkedin.com/company/acme/jobs", true},
		{"linkedin country subdomain folds in via eTLD+1", "https://de.linkedin.com/jobs/view/123", true},
		{"indeed aggregator", "https://de.indeed.com/jobs?q=go", true},
		{"stepstone job board", "https://www.stepstone.de/jobs/acme", true},
		{"crunchboard job board", "https://www.crunchboard.com/jobs/123", true},
		// #46 audit additions -- one per added denylist host.
		{"eu-startups directory", "https://www.eu-startups.com/directory/", true},
		{"schuelerkarriere student board", "https://schuelerkarriere.de/jobs", true},
		{"musicbusinessworldwide job board", "https://www.musicbusinessworldwide.com/jobs/", true},
		{"ausbildung apprenticeship board", "https://www.ausbildung.de/stellen/", true},
		{"deutsche-startups directory", "https://www.deutsche-startups.de/jobs/", true},
		{"gruenderszene directory", "https://www.gruenderszene.de/jobboard", true},
		{"startupbrett directory", "https://startupbrett.de/", true},
		{"dealroom company database", "https://dealroom.co/companies", true},
		{"crunchbase company database", "https://www.crunchbase.com/hub/x", true},
		{"f6s company database", "https://www.f6s.com/companies", true},
		{"bitkom member directory", "https://www.bitkom.org/mitglieder", true},
		{"startupverband member directory", "https://startupverband.de/mitglieder", true},
		{"balderton portfolio board", "https://balderton.com/portfolio", true},
		{"earlybird portfolio board", "https://earlybird.com/portfolio", true},
		{"pointnine portfolio board", "https://pointnine.com/portfolio", true},
		{"cherry.vc portfolio board on its .vc ccTLD", "https://www.cherry.vc/portfolio", true},
		{"holtzbrinck ventures portfolio board", "https://www.holtzbrinck-ventures.com/portfolio", true},
		{"lakestar portfolio board", "https://lakestar.com/portfolio", true},
		{"techstars portfolio board", "https://www.techstars.com/portfolio", true},
		{"ycombinator portfolio folds in news.ycombinator.com subdomain", "https://www.ycombinator.com/companies", true},
		{"match is case-insensitive", "https://BuiltIn.com/jobs", true},
		// A per-tenant ATS or recruiting-platform board root is a legitimate hub,
		// not an aggregator -- its only defect is identity attribution (#46).
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
