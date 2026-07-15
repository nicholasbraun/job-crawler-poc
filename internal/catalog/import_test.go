package catalog_test

import (
	"strings"
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

func ptr[T any](v T) *T {
	return &v
}

func TestDecodeImportRecord(t *testing.T) {
	t.Run("full record decodes all fields", func(t *testing.T) {
		line := `{"companyKey":"greenhouse:acme","atsProvider":"greenhouse","name":"Acme","displayDomain":"acme.com","website":"https://acme.com","firstSeen":"2025-01-02T03:04:05Z","lastSeen":"2025-06-07T08:09:10Z","careerPages":[{"url":"https://boards.greenhouse.io/acme","firstSeen":"2025-01-03T00:00:00Z","lastSeen":"2025-06-06T00:00:00Z"}]}`
		rec, err := catalog.DecodeImportRecord([]byte(line))
		if err != nil {
			t.Fatalf("DecodeImportRecord() error = %v", err)
		}
		if rec.CompanyKey == nil || *rec.CompanyKey != "greenhouse:acme" {
			t.Errorf("CompanyKey = %v, want greenhouse:acme", rec.CompanyKey)
		}
		if rec.ATSProvider == nil || *rec.ATSProvider != "greenhouse" {
			t.Errorf("ATSProvider = %v, want greenhouse", rec.ATSProvider)
		}
		if rec.Name == nil || *rec.Name != "Acme" {
			t.Errorf("Name = %v, want Acme", rec.Name)
		}
		if rec.DisplayDomain == nil || *rec.DisplayDomain != "acme.com" {
			t.Errorf("DisplayDomain = %v, want acme.com", rec.DisplayDomain)
		}
		if rec.Website == nil || *rec.Website != "https://acme.com" {
			t.Errorf("Website = %v, want https://acme.com", rec.Website)
		}
		if rec.FirstSeen == nil || !rec.FirstSeen.Equal(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)) {
			t.Errorf("FirstSeen = %v, want 2025-01-02T03:04:05Z", rec.FirstSeen)
		}
		if rec.LastSeen == nil || !rec.LastSeen.Equal(time.Date(2025, 6, 7, 8, 9, 10, 0, time.UTC)) {
			t.Errorf("LastSeen = %v, want 2025-06-07T08:09:10Z", rec.LastSeen)
		}
		if len(rec.CareerPages) != 1 {
			t.Fatalf("len(CareerPages) = %d, want 1", len(rec.CareerPages))
		}
		page := rec.CareerPages[0]
		if page.URL != "https://boards.greenhouse.io/acme" {
			t.Errorf("page URL = %q, want https://boards.greenhouse.io/acme", page.URL)
		}
		if page.FirstSeen == nil || !page.FirstSeen.Equal(time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("page FirstSeen = %v, want 2025-01-03T00:00:00Z", page.FirstSeen)
		}
		if page.LastSeen == nil || !page.LastSeen.Equal(time.Date(2025, 6, 6, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("page LastSeen = %v, want 2025-06-06T00:00:00Z", page.LastSeen)
		}
	})

	t.Run("absent fields decode to nil pointers", func(t *testing.T) {
		rec, err := catalog.DecodeImportRecord([]byte(`{"name":"Initech"}`))
		if err != nil {
			t.Fatalf("DecodeImportRecord() error = %v", err)
		}
		if rec.Name == nil || *rec.Name != "Initech" {
			t.Errorf("Name = %v, want Initech", rec.Name)
		}
		if rec.CompanyKey != nil || rec.ATSProvider != nil || rec.DisplayDomain != nil || rec.Website != nil {
			t.Errorf("absent string fields not nil: %+v", rec)
		}
		if rec.FirstSeen != nil || rec.LastSeen != nil {
			t.Errorf("absent timestamps not nil: %+v", rec)
		}
		if len(rec.CareerPages) != 0 {
			t.Errorf("len(CareerPages) = %d, want 0", len(rec.CareerPages))
		}
	})

	t.Run("explicit empty string is present, not absent", func(t *testing.T) {
		rec, err := catalog.DecodeImportRecord([]byte(`{"atsProvider":"","website":""}`))
		if err != nil {
			t.Fatalf("DecodeImportRecord() error = %v", err)
		}
		if rec.ATSProvider == nil || *rec.ATSProvider != "" {
			t.Errorf("ATSProvider = %v, want pointer to \"\"", rec.ATSProvider)
		}
		if rec.Website == nil || *rec.Website != "" {
			t.Errorf("Website = %v, want pointer to \"\"", rec.Website)
		}
	})

	t.Run("malformed json errors", func(t *testing.T) {
		_, err := catalog.DecodeImportRecord([]byte(`{"name":`))
		if err == nil || !strings.Contains(err.Error(), "invalid json") {
			t.Errorf("DecodeImportRecord() error = %v, want invalid json", err)
		}
	})

	t.Run("non-object line errors", func(t *testing.T) {
		_, err := catalog.DecodeImportRecord([]byte(`[1,2]`))
		if err == nil || !strings.Contains(err.Error(), "invalid json") {
			t.Errorf("DecodeImportRecord() error = %v, want invalid json", err)
		}
	})

	t.Run("invalid timestamp errors", func(t *testing.T) {
		_, err := catalog.DecodeImportRecord([]byte(`{"firstSeen":"yesterday"}`))
		if err == nil {
			t.Error("DecodeImportRecord() error = nil, want timestamp error")
		}
	})
}

func TestResolveImportRecord_IdentityLadder(t *testing.T) {
	t.Run("explicit companyKey outranks website and pages", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey:  ptr("greenhouse:acme"),
			Website:     ptr("https://other.com"),
			CareerPages: []catalog.ImportPageRecord{{URL: "https://jobs.lever.co/initech"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "greenhouse:acme" {
			t.Errorf("CompanyKey = %q, want greenhouse:acme", got.Company.CompanyKey)
		}
		// File-authoritative: the lever page still lands under the file's key.
		if len(got.Pages) != 1 || got.Pages[0].URL != "https://jobs.lever.co/initech" {
			t.Errorf("Pages = %+v, want the lever page resolved", got.Pages)
		}
	})

	t.Run("empty companyKey treated as absent", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr(""),
			Website:    ptr("https://acme.com"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey = %q, want acme.com", got.Company.CompanyKey)
		}
	})

	t.Run("website derives registrable domain", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			Website: ptr("https://jobs.acme.co.uk/about"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "acme.co.uk" {
			t.Errorf("CompanyKey = %q, want acme.co.uk", got.Company.CompanyKey)
		}
		if got.Company.ATSProvider != "" {
			t.Errorf("ATSProvider = %q, want self-hosted \"\"", got.Company.ATSProvider)
		}
	})

	t.Run("website outranks pages", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			Website:     ptr("https://acme.com"),
			CareerPages: []catalog.ImportPageRecord{{URL: "https://boards.greenhouse.io/acme"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey = %q, want acme.com", got.Company.CompanyKey)
		}
		if len(got.Pages) != 1 || got.Pages[0].PolitenessDomain != "boards.greenhouse.io" {
			t.Errorf("Pages = %+v, want politeness boards.greenhouse.io", got.Pages)
		}
	})

	t.Run("pages derive ats-aware identity", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{{URL: "https://boards.greenhouse.io/acme/jobs/123"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "greenhouse:acme" {
			t.Errorf("CompanyKey = %q, want greenhouse:acme", got.Company.CompanyKey)
		}
		if got.Company.ATSProvider != "greenhouse" || got.Company.ATSProviderPresent {
			t.Errorf("ATSProvider = (%q, %v), want (greenhouse, false)",
				got.Company.ATSProvider, got.Company.ATSProviderPresent)
		}
	})

	t.Run("pages agreeing on one company resolve", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{
				{URL: "https://boards.greenhouse.io/acme"},
				{URL: "https://boards.greenhouse.io/acme/jobs/123"},
			},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "greenhouse:acme" {
			t.Errorf("CompanyKey = %q, want greenhouse:acme", got.Company.CompanyKey)
		}
		// No intra-record dedup: both entries pass through even though they
		// collapse to the same stored URL (ADR-0013 merges converge).
		if len(got.Pages) != 2 {
			t.Fatalf("len(Pages) = %d, want 2", len(got.Pages))
		}
		for _, p := range got.Pages {
			if p.URL != "https://boards.greenhouse.io/acme" {
				t.Errorf("page URL = %q, want https://boards.greenhouse.io/acme", p.URL)
			}
		}
	})

	t.Run("keyless multi-company pages error names sorted keys", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{
				{URL: "https://jobs.lever.co/initech"},
				{URL: "https://boards.greenhouse.io/acme"},
			},
		})
		if err == nil {
			t.Fatal("ResolveImportRecord() error = nil, want multi-company error")
		}
		if !strings.Contains(err.Error(), "greenhouse:acme, lever:initech") {
			t.Errorf("error = %q, want sorted keys greenhouse:acme, lever:initech", err)
		}
		if !strings.Contains(err.Error(), "split the record or set companyKey") {
			t.Errorf("error = %q, want split-the-record hint", err)
		}
	})

	t.Run("record with no identity source errors", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{Name: ptr("x")})
		if err == nil || !strings.Contains(err.Error(), "no identity") {
			t.Errorf("error = %v, want no-identity error", err)
		}
	})

	t.Run("empty careerPages array with no key or website errors", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{},
		})
		if err == nil || !strings.Contains(err.Error(), "no identity") {
			t.Errorf("error = %v, want no-identity error", err)
		}
	})

	t.Run("record with only a companyKey resolves as pageless", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("greenhouse:acme"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "greenhouse:acme" {
			t.Errorf("CompanyKey = %q, want greenhouse:acme", got.Company.CompanyKey)
		}
		if len(got.Pages) != 0 {
			t.Errorf("len(Pages) = %d, want 0 (Pageless Company)", len(got.Pages))
		}
	})
}

func TestResolveImportRecord_Defaults(t *testing.T) {
	t.Run("atsProvider fills from key prefix", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{CompanyKey: ptr("greenhouse:acme")})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.ATSProvider != "greenhouse" || got.Company.ATSProviderPresent {
			t.Errorf("ATSProvider = (%q, %v), want (greenhouse, false)",
				got.Company.ATSProvider, got.Company.ATSProviderPresent)
		}
	})

	t.Run("atsProvider fills self-hosted for prefixless key", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{CompanyKey: ptr("acme.com")})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.ATSProvider != "" || got.Company.ATSProviderPresent {
			t.Errorf("ATSProvider = (%q, %v), want (\"\", false)",
				got.Company.ATSProvider, got.Company.ATSProviderPresent)
		}
	})

	t.Run("explicit empty atsProvider is present", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey:  ptr("greenhouse:acme"),
			ATSProvider: ptr(""),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.ATSProvider != "" || !got.Company.ATSProviderPresent {
			t.Errorf("ATSProvider = (%q, %v), want (\"\", true)",
				got.Company.ATSProvider, got.Company.ATSProviderPresent)
		}
	})

	t.Run("explicit atsProvider passes through unvalidated", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey:  ptr("greenhouse:acme"),
			ATSProvider: ptr("lever"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.ATSProvider != "lever" || !got.Company.ATSProviderPresent {
			t.Errorf("ATSProvider = (%q, %v), want (lever, true)",
				got.Company.ATSProvider, got.Company.ATSProviderPresent)
		}
	})

	t.Run("displayDomain fills from website registrable domain", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("greenhouse:acme"),
			Website:    ptr("https://jobs.acme.com/x"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.DisplayDomain != "acme.com" || got.Company.DisplayDomainPresent {
			t.Errorf("DisplayDomain = (%q, %v), want (acme.com, false)",
				got.Company.DisplayDomain, got.Company.DisplayDomainPresent)
		}
	})

	t.Run("displayDomain fills from self-hosted key", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{CompanyKey: ptr("acme.com")})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.DisplayDomain != "acme.com" || got.Company.DisplayDomainPresent {
			t.Errorf("DisplayDomain = (%q, %v), want (acme.com, false)",
				got.Company.DisplayDomain, got.Company.DisplayDomainPresent)
		}
	})

	t.Run("displayDomain empty for ats key without website", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{CompanyKey: ptr("greenhouse:acme")})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.DisplayDomain != "" || got.Company.DisplayDomainPresent {
			t.Errorf("DisplayDomain = (%q, %v), want (\"\", false)",
				got.Company.DisplayDomain, got.Company.DisplayDomainPresent)
		}
	})

	t.Run("explicit displayDomain passes through", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey:    ptr("greenhouse:acme"),
			DisplayDomain: ptr("corp.example"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.DisplayDomain != "corp.example" || !got.Company.DisplayDomainPresent {
			t.Errorf("DisplayDomain = (%q, %v), want (corp.example, true)",
				got.Company.DisplayDomain, got.Company.DisplayDomainPresent)
		}
	})

	t.Run("name fills from tenant slug", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{CompanyKey: ptr("greenhouse:acme")})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.Name != "acme" || got.Company.NamePresent {
			t.Errorf("Name = (%q, %v), want (acme, false)", got.Company.Name, got.Company.NamePresent)
		}
	})

	t.Run("name fills from self-hosted key", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{CompanyKey: ptr("acme.com")})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.Name != "acme.com" || got.Company.NamePresent {
			t.Errorf("Name = (%q, %v), want (acme.com, false)", got.Company.Name, got.Company.NamePresent)
		}
	})

	t.Run("explicit name passes through", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("greenhouse:acme"),
			Name:       ptr("Acme Corp"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.Name != "Acme Corp" || !got.Company.NamePresent {
			t.Errorf("Name = (%q, %v), want (Acme Corp, true)", got.Company.Name, got.Company.NamePresent)
		}
	})

	t.Run("website passes through verbatim with presence", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			Website: ptr("https://Acme.com/"),
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.Website != "https://Acme.com/" || !got.Company.WebsitePresent {
			t.Errorf("Website = (%q, %v), want the file's bytes with presence",
				got.Company.Website, got.Company.WebsitePresent)
		}
		if got.Company.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey = %q, want acme.com", got.Company.CompanyKey)
		}
	})

	t.Run("company timestamps pass through and absent stay nil", func(t *testing.T) {
		first := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("acme.com"),
			FirstSeen:  &first,
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.FirstSeen == nil || !got.Company.FirstSeen.Equal(first) {
			t.Errorf("FirstSeen = %v, want %v", got.Company.FirstSeen, first)
		}
		if got.Company.LastSeen != nil {
			t.Errorf("LastSeen = %v, want nil (absent)", got.Company.LastSeen)
		}
	})
}

func TestResolveImportRecord_Pages(t *testing.T) {
	t.Run("page url canonicalised like discovery", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey:  ptr("acme.com"),
			CareerPages: []catalog.ImportPageRecord{{URL: "http://acme.com/jobs/?utm=x"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if len(got.Pages) != 1 || got.Pages[0].URL != "https://acme.com/jobs" {
			t.Errorf("Pages = %+v, want URL https://acme.com/jobs", got.Pages)
		}
	})

	t.Run("ats page collapses to board root", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{{URL: "https://boards.greenhouse.io/acme/jobs/123?gh_src=x"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if len(got.Pages) != 1 || got.Pages[0].URL != "https://boards.greenhouse.io/acme" {
			t.Errorf("Pages = %+v, want URL https://boards.greenhouse.io/acme", got.Pages)
		}
	})

	t.Run("uppercase host normalised", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{{URL: "HTTPS://BOARDS.GREENHOUSE.IO/Acme"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if len(got.Pages) != 1 {
			t.Fatalf("len(Pages) = %d, want 1", len(got.Pages))
		}
		if got.Pages[0].URL != "https://boards.greenhouse.io/acme" {
			t.Errorf("URL = %q, want https://boards.greenhouse.io/acme", got.Pages[0].URL)
		}
		if got.Pages[0].PolitenessDomain != "boards.greenhouse.io" {
			t.Errorf("PolitenessDomain = %q, want boards.greenhouse.io", got.Pages[0].PolitenessDomain)
		}
	})

	t.Run("politeness domain is the full url host, not etld+1", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{{URL: "https://careers.acme.com/jobs"}},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if got.Company.CompanyKey != "acme.com" {
			t.Errorf("CompanyKey = %q, want acme.com", got.Company.CompanyKey)
		}
		if len(got.Pages) != 1 || got.Pages[0].PolitenessDomain != "careers.acme.com" {
			t.Errorf("Pages = %+v, want politeness careers.acme.com", got.Pages)
		}
	})

	t.Run("page timestamps pass through", func(t *testing.T) {
		first := time.Date(2025, 1, 3, 0, 0, 0, 0, time.UTC)
		last := time.Date(2025, 6, 6, 0, 0, 0, 0, time.UTC)
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("acme.com"),
			CareerPages: []catalog.ImportPageRecord{
				{URL: "https://acme.com/jobs", FirstSeen: &first, LastSeen: &last},
			},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if len(got.Pages) != 1 {
			t.Fatalf("len(Pages) = %d, want 1", len(got.Pages))
		}
		if got.Pages[0].FirstSeen == nil || !got.Pages[0].FirstSeen.Equal(first) {
			t.Errorf("page FirstSeen = %v, want %v", got.Pages[0].FirstSeen, first)
		}
		if got.Pages[0].LastSeen == nil || !got.Pages[0].LastSeen.Equal(last) {
			t.Errorf("page LastSeen = %v, want %v", got.Pages[0].LastSeen, last)
		}
	})
}

func TestResolveImportRecord_Errors(t *testing.T) {
	t.Run("non-absolute and non-http page urls error precisely", func(t *testing.T) {
		tests := []struct {
			name string
			url  string
		}{
			{"relative page url", "/jobs"},
			{"non-http scheme", "ftp://acme.com/jobs"},
			{"protocol-relative", "//acme.com/jobs"},
			{"empty page url", ""},
			{"bare hostname", "acme.com"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
					CompanyKey:  ptr("acme.com"),
					CareerPages: []catalog.ImportPageRecord{{URL: tt.url}},
				})
				if err != nil {
					t.Fatalf("ResolveImportRecord() error = %v, want page error in PageErrors", err)
				}
				if len(got.PageErrors) != 1 {
					t.Fatalf("len(PageErrors) = %d, want 1", len(got.PageErrors))
				}
				msg := got.PageErrors[0].Error()
				if !strings.Contains(msg, `"`+tt.url+`"`) {
					t.Errorf("page error = %q, want the offending value %q named", msg, tt.url)
				}
				if !strings.Contains(msg, "must be an absolute http(s) url") {
					t.Errorf("page error = %q, want absolute-http(s) message", msg)
				}
			})
		}
	})

	t.Run("unparseable website errors", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			Website: ptr("https://acme com/"),
		})
		if err == nil || !strings.HasPrefix(err.Error(), `website "https://acme com/"`) {
			t.Errorf("error = %v, want it to start with the website field and value", err)
		}
	})

	t.Run("relative website errors", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			Website: ptr("acme.com"),
		})
		if err == nil || !strings.Contains(err.Error(), "must be an absolute http(s) url") {
			t.Errorf("error = %v, want absolute-http(s) message", err)
		}
	})

	t.Run("invalid website fails whole record even when keyed", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("greenhouse:acme"),
			Website:    ptr("acme.com"),
		})
		if err == nil {
			t.Fatal("ResolveImportRecord() error = nil, want website error")
		}
		if got.Company.CompanyKey != "" || len(got.Pages) != 0 {
			t.Errorf("got = %+v, want the zero record on a whole-record error", got)
		}
	})

	t.Run("keyed record with an invalid page applies sub-line best-effort", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CompanyKey: ptr("greenhouse:acme"),
			CareerPages: []catalog.ImportPageRecord{
				{URL: "https://boards.greenhouse.io/acme"},
				{URL: "/jobs"},
			},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if len(got.Pages) != 1 || got.Pages[0].URL != "https://boards.greenhouse.io/acme" {
			t.Errorf("Pages = %+v, want the valid page only", got.Pages)
		}
		if len(got.PageErrors) != 1 || !strings.Contains(got.PageErrors[0].Error(), `"/jobs"`) {
			t.Errorf("PageErrors = %v, want one error naming \"/jobs\"", got.PageErrors)
		}
	})

	t.Run("website-identified record with an invalid page applies sub-line best-effort", func(t *testing.T) {
		got, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			Website: ptr("https://acme.com"),
			CareerPages: []catalog.ImportPageRecord{
				{URL: "https://acme.com/careers"},
				{URL: "/jobs"},
			},
		})
		if err != nil {
			t.Fatalf("ResolveImportRecord() error = %v", err)
		}
		if len(got.Pages) != 1 || got.Pages[0].URL != "https://acme.com/careers" {
			t.Errorf("Pages = %+v, want the valid page only", got.Pages)
		}
		if len(got.PageErrors) != 1 || !strings.Contains(got.PageErrors[0].Error(), `"/jobs"`) {
			t.Errorf("PageErrors = %v, want one error naming \"/jobs\"", got.PageErrors)
		}
	})

	t.Run("keyless record with an invalid page fails whole-line", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{
				{URL: "https://boards.greenhouse.io/acme"},
				{URL: "/jobs"},
			},
		})
		if err == nil || !strings.Contains(err.Error(), `"/jobs"`) {
			t.Errorf("error = %v, want the page error promoted to whole-line", err)
		}
	})

	t.Run("keyless record with all pages invalid fails whole-line", func(t *testing.T) {
		_, err := catalog.ResolveImportRecord(catalog.ImportRecord{
			CareerPages: []catalog.ImportPageRecord{
				{URL: "/jobs"},
				{URL: "ftp://acme.com/jobs"},
			},
		})
		if err == nil || !strings.Contains(err.Error(), `"/jobs"`) {
			t.Errorf("error = %v, want the first page error promoted to whole-line", err)
		}
	})
}
