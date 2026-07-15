package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
)

// exportBody issues GET /api/catalog/export against srv and returns the
// recorder for assertions.
func exportBody(t *testing.T, srv http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/catalog/export", nil))
	return rec
}

func TestCatalogExportHeaders(t *testing.T) {
	companies := &fakeCompanyRepo{companies: []*crawler.Company{
		{ID: uuid.New(), CompanyKey: "acme.com"},
	}}
	srv := newHandler(api.Config{Companies: companies})

	// Capture the date on both sides of the request so a midnight rollover
	// mid-test cannot flake the filename assertion.
	before := time.Now().UTC().Format("2006-01-02")
	rec := exportBody(t, srv)
	after := time.Now().UTC().Format("2006-01-02")

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type: got %q, want application/x-ndjson", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control: got %q, want no-store", cc)
	}
	cd := rec.Header().Get("Content-Disposition")
	wantBefore := `attachment; filename="catalog-` + before + `.ndjson"`
	wantAfter := `attachment; filename="catalog-` + after + `.ndjson"`
	if cd != wantBefore && cd != wantAfter {
		t.Errorf("Content-Disposition: got %q, want %q", cd, wantBefore)
	}
}

func TestCatalogExportIsDeterministicallyOrdered(t *testing.T) {
	cet := time.FixedZone("CET", 3600)
	// 10:00 CET == 09:00 UTC, so the expected lines below can spell the Z
	// instants directly.
	at := func(day int) time.Time { return time.Date(2026, 1, day, 10, 0, 0, 0, cet) }

	acmeSelf := &crawler.Company{
		ID:         uuid.New(),
		CompanyKey: "acme.com",
		// Name and DisplayDomain deliberately empty: "not known" must be
		// omitted from the line, not serialized as "".
		FirstSeen: at(1),
		LastSeen:  at(5),
	}
	acmeGH := &crawler.Company{
		ID:            uuid.New(),
		CompanyKey:    "greenhouse:acme",
		ATSProvider:   "greenhouse",
		Name:          "Acme",
		DisplayDomain: "acme.com",
		// Sub-second precision must survive the export for a lossless
		// import round trip.
		FirstSeen: time.Date(2026, 1, 2, 10, 0, 0, 123456789, cet),
		LastSeen:  at(6),
	}
	zeta := &crawler.Company{
		ID:            uuid.New(),
		CompanyKey:    "zeta.com",
		Name:          "Zeta",
		DisplayDomain: "zeta.com",
		FirstSeen:     at(3),
		LastSeen:      at(7),
	}

	// Repository order is last_seen DESC — deliberately not key order.
	companies := &fakeCompanyRepo{companies: []*crawler.Company{zeta, acmeGH, acmeSelf}}
	// Pages arrive shuffled relative to URL order; every page carries the
	// internal fields the export must strip.
	pages := &fakeCareerPageRepo{pages: []*crawler.CareerPage{
		{ID: uuid.New(), CompanyID: acmeGH.ID, URL: "https://boards.greenhouse.io/acme?dept=eng&x=1", PolitenessDomain: "boards.greenhouse.io", FirstSeen: at(2), LastSeen: at(6)},
		{ID: uuid.New(), CompanyID: acmeSelf.ID, URL: "https://acme.com/careers", PolitenessDomain: "acme.com", FirstSeen: at(1), LastSeen: at(5)},
		{ID: uuid.New(), CompanyID: acmeGH.ID, URL: "https://boards.greenhouse.io/acme", PolitenessDomain: "boards.greenhouse.io", FirstSeen: at(2), LastSeen: at(6)},
		{ID: uuid.New(), CompanyID: acmeGH.ID, URL: "https://boards.greenhouse.io/acme/jobs", PolitenessDomain: "boards.greenhouse.io", FirstSeen: at(4), LastSeen: at(6)},
	}}
	srv := newHandler(api.Config{Companies: companies, CareerPages: pages})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	want := `{"companyKey":"acme.com","atsProvider":"","firstSeen":"2026-01-01T09:00:00Z","lastSeen":"2026-01-05T09:00:00Z","careerPages":[{"url":"https://acme.com/careers","firstSeen":"2026-01-01T09:00:00Z","lastSeen":"2026-01-05T09:00:00Z"}]}` + "\n" +
		`{"companyKey":"greenhouse:acme","atsProvider":"greenhouse","name":"Acme","displayDomain":"acme.com","firstSeen":"2026-01-02T09:00:00.123456789Z","lastSeen":"2026-01-06T09:00:00Z","careerPages":[{"url":"https://boards.greenhouse.io/acme","firstSeen":"2026-01-02T09:00:00Z","lastSeen":"2026-01-06T09:00:00Z"},{"url":"https://boards.greenhouse.io/acme/jobs","firstSeen":"2026-01-04T09:00:00Z","lastSeen":"2026-01-06T09:00:00Z"},{"url":"https://boards.greenhouse.io/acme?dept=eng&x=1","firstSeen":"2026-01-02T09:00:00Z","lastSeen":"2026-01-06T09:00:00Z"}]}` + "\n" +
		`{"companyKey":"zeta.com","atsProvider":"","name":"Zeta","displayDomain":"zeta.com","firstSeen":"2026-01-03T09:00:00Z","lastSeen":"2026-01-07T09:00:00Z","careerPages":[]}` + "\n"
	if got := rec.Body.String(); got != want {
		t.Errorf("export body:\ngot:\n%s\nwant:\n%s", got, want)
	}

	// Byte-stable across repeated exports of the same Catalog.
	rec2 := exportBody(t, srv)
	if rec.Body.String() != rec2.Body.String() {
		t.Errorf("repeated exports differ:\nfirst:\n%s\nsecond:\n%s", rec.Body, rec2.Body)
	}
}

// TestCatalogExportEmitsWebsite pins the website field's byte-level contract: a
// company with a website emits it at the promised key position (between
// displayDomain and firstSeen), and a company without one omits the key entirely
// (omitempty) so a re-import never reads a presence-wins blanking write.
func TestCatalogExportEmitsWebsite(t *testing.T) {
	at := func(day int) time.Time { return time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC) }
	acme := &crawler.Company{
		ID:            uuid.New(),
		CompanyKey:    "acme.com",
		Name:          "Acme",
		DisplayDomain: "acme.com",
		Website:       "https://acme.com",
		FirstSeen:     at(1),
		LastSeen:      at(5),
	}
	zeta := &crawler.Company{
		ID:            uuid.New(),
		CompanyKey:    "zeta.com",
		Name:          "Zeta",
		DisplayDomain: "zeta.com",
		FirstSeen:     at(3),
		LastSeen:      at(7),
	}
	companies := &fakeCompanyRepo{companies: []*crawler.Company{acme, zeta}}
	srv := newHandler(api.Config{Companies: companies})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	var acmeLine, zetaLine string
	for _, line := range strings.Split(strings.TrimSuffix(rec.Body.String(), "\n"), "\n") {
		switch {
		case strings.Contains(line, `"companyKey":"acme.com"`):
			acmeLine = line
		case strings.Contains(line, `"companyKey":"zeta.com"`):
			zetaLine = line
		}
	}
	// Value and key position: website sits between displayDomain and firstSeen.
	if !strings.Contains(acmeLine, `"displayDomain":"acme.com","website":"https://acme.com","firstSeen"`) {
		t.Errorf("acme line missing website at the promised position; got %s", acmeLine)
	}
	// omitempty: a website-less company must not emit the key, so a re-import
	// cannot read it as a presence-wins blank.
	if strings.Contains(zetaLine, `"website"`) {
		t.Errorf("website-less company must omit the website key; got %s", zetaLine)
	}
}

func TestCatalogExportIncludesPagelessCompanies(t *testing.T) {
	companies := &fakeCompanyRepo{companies: []*crawler.Company{
		{ID: uuid.New(), CompanyKey: "pageless.example", Name: "Pageless"},
	}}
	srv := newHandler(api.Config{Companies: companies})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	var line map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &line); err != nil {
		t.Fatalf("error decoding line %s: %v", rec.Body, err)
	}
	cp, ok := line["careerPages"].([]any)
	if !ok {
		t.Fatalf("careerPages should be a present, non-null array; got %v (%T)", line["careerPages"], line["careerPages"])
	}
	if len(cp) != 0 {
		t.Errorf("pageless company should export an empty careerPages array, got %v", cp)
	}
}

func TestCatalogExportNeverLeaksInternalFields(t *testing.T) {
	company := &crawler.Company{
		ID:            uuid.New(),
		CompanyKey:    "greenhouse:acme",
		ATSProvider:   "greenhouse",
		Name:          "Acme",
		DisplayDomain: "acme.com",
		FirstSeen:     time.Now(),
		LastSeen:      time.Now(),
	}
	companies := &fakeCompanyRepo{companies: []*crawler.Company{company}}
	pages := &fakeCareerPageRepo{pages: []*crawler.CareerPage{
		{ID: uuid.New(), CompanyID: company.ID, URL: "https://boards.greenhouse.io/acme", PolitenessDomain: "boards.greenhouse.io", FirstSeen: time.Now(), LastSeen: time.Now()},
	}}
	srv := newHandler(api.Config{Companies: companies, CareerPages: pages})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	body := rec.Body.String()
	for _, leak := range []string{`"id"`, `"companyId"`, `"politenessDomain"`} {
		if strings.Contains(body, leak) {
			t.Errorf("export must not contain %s; body=%s", leak, body)
		}
	}

	var line map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &line); err != nil {
		t.Fatalf("error decoding line %s: %v", rec.Body, err)
	}
	var pageObjs []map[string]json.RawMessage
	if err := json.Unmarshal(line["careerPages"], &pageObjs); err != nil {
		t.Fatalf("error decoding careerPages: %v", err)
	}

	wantCompanyKeys := []string{"companyKey", "atsProvider", "name", "displayDomain", "firstSeen", "lastSeen", "careerPages"}
	if len(line) != len(wantCompanyKeys) {
		t.Errorf("company line has %d keys, want %d: %v", len(line), len(wantCompanyKeys), line)
	}
	for _, k := range wantCompanyKeys {
		if _, ok := line[k]; !ok {
			t.Errorf("company line missing key %q", k)
		}
	}

	if len(pageObjs) != 1 {
		t.Fatalf("want 1 page, got %d", len(pageObjs))
	}
	wantPageKeys := []string{"url", "firstSeen", "lastSeen"}
	if len(pageObjs[0]) != len(wantPageKeys) {
		t.Errorf("page object has %d keys, want %d: %v", len(pageObjs[0]), len(wantPageKeys), pageObjs[0])
	}
	for _, k := range wantPageKeys {
		if _, ok := pageObjs[0][k]; !ok {
			t.Errorf("page object missing key %q", k)
		}
	}
}

// TestCatalogExportSnapshotRaceYieldsPagelessCompany pins the ADR-0015 race
// contract through observable output: a company catalogued between the export
// handler's two repository reads must appear as a Pageless line — if the
// handler read companies before pages, the late company would be missing
// entirely.
func TestCatalogExportSnapshotRaceYieldsPagelessCompany(t *testing.T) {
	existing := &crawler.Company{ID: uuid.New(), CompanyKey: "acme.com"}
	companies := &fakeCompanyRepo{companies: []*crawler.Company{existing}}
	pages := &fakeCareerPageRepo{pages: []*crawler.CareerPage{
		{ID: uuid.New(), CompanyID: existing.ID, URL: "https://acme.com/careers"},
	}}
	pages.onList = func() {
		companies.companies = append(companies.companies, &crawler.Company{
			ID: uuid.New(), CompanyKey: "latecomer.example",
		})
	}
	srv := newHandler(api.Config{Companies: companies, CareerPages: pages})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}

	body := rec.Body.String()
	lateLine := ""
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if strings.Contains(line, `"companyKey":"latecomer.example"`) {
			lateLine = line
		}
	}
	if lateLine == "" {
		t.Fatalf("late-created company should still be exported; body=%s", body)
	}
	if !strings.Contains(lateLine, `"careerPages":[]`) {
		t.Errorf("late-created company should export as pageless, got line %s", lateLine)
	}
	if !strings.Contains(body, "https://acme.com/careers") {
		t.Errorf("existing pages must not be dropped by the race; body=%s", body)
	}
}

func TestCatalogExportSkipsOrphanCareerPages(t *testing.T) {
	company := &crawler.Company{ID: uuid.New(), CompanyKey: "acme.com"}
	companies := &fakeCompanyRepo{companies: []*crawler.Company{company}}
	pages := &fakeCareerPageRepo{pages: []*crawler.CareerPage{
		{ID: uuid.New(), CompanyID: company.ID, URL: "https://acme.com/careers"},
		// Orphan: its company vanished between the two reads (e.g. removed by
		// the Catalog Doctor). The format cannot represent a dangling page.
		{ID: uuid.New(), CompanyID: uuid.New(), URL: "https://orphan.example/jobs"},
	}}
	srv := newHandler(api.Config{Companies: companies, CareerPages: pages})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "orphan.example") {
		t.Errorf("orphan page must be dropped from the export; body=%s", body)
	}
	if !strings.Contains(body, "https://acme.com/careers") {
		t.Errorf("attributed page missing from export; body=%s", body)
	}
}

func TestCatalogExportEmptyCatalog(t *testing.T) {
	srv := newHandler(api.Config{})

	rec := exportBody(t, srv)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/x-ndjson" {
		t.Errorf("Content-Type: got %q, want application/x-ndjson", ct)
	}
	// Zero lines and no trailer/summary line (ADR-0015).
	if rec.Body.Len() != 0 {
		t.Errorf("empty catalog should export an empty body, got %q", rec.Body)
	}
}

func TestCatalogExportRepositoryErrors(t *testing.T) {
	t.Run("career page read failure is a 500 before any byte streams", func(t *testing.T) {
		pages := &fakeCareerPageRepo{listErr: errors.New("boom")}
		srv := newHandler(api.Config{CareerPages: pages})

		rec := exportBody(t, srv)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status: got %d, want 500; body=%s", rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("error Content-Type: got %q, want application/json", ct)
		}
	})

	t.Run("company read failure is a 500 before any byte streams", func(t *testing.T) {
		companies := &fakeCompanyRepo{listErr: errors.New("boom")}
		srv := newHandler(api.Config{Companies: companies})

		rec := exportBody(t, srv)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status: got %d, want 500; body=%s", rec.Code, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("error Content-Type: got %q, want application/json", ct)
		}
	})
}
