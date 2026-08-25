package ats_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
)

// newBambooHRFetcher stands up an httptest server with handler, registers its
// cleanup, and returns a BambooHRFetcher pointed at it with pacing disabled — every
// test but the two pacing ones is about mapping, not timing.
func newBambooHRFetcher(t *testing.T, handler http.HandlerFunc) *ats.BambooHRFetcher {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return ats.NewBambooHRFetcher(
		ats.WithBambooHRBaseURL(srv.URL),
		ats.WithBambooHRDetailDelay(0),
	)
}

// bhRecorder is an inline BambooHR careers-site double. Its handler serves list for
// /careers/list and details[id] for /careers/<id>/detail (an id in detailStatus
// answers that status with the board's real not-found body; an id in neither map
// 404s), while recording every request's auth headers, the detail ids in order, and
// each detail call's arrival time — so tests can assert no-auth, the 404-vs-429
// split, pacing, and the detail budget.
type bhRecorder struct {
	mu           sync.Mutex
	list         string
	listStatus   int // 0 == 200
	details      map[string]string
	detailStatus map[string]int
	authHeaders  []string
	apiKeyHeader []string
	detailIDs    []string
	detailTimes  []time.Time
	// onDetail runs inside the handler before the reply, keyed by posting id — the
	// hook the cancellation tests use to interrupt a run at a known point.
	onDetail func(id string)
}

func (rec *bhRecorder) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.authHeaders = append(rec.authHeaders, r.Header.Get("Authorization"))
		rec.apiKeyHeader = append(rec.apiKeyHeader, r.Header.Get("X-Api-Key"))
		rec.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")

		id := bhDetailID(r.URL.Path)
		if id == "" {
			if rec.listStatus != 0 && rec.listStatus != http.StatusOK {
				w.WriteHeader(rec.listStatus)
				return
			}
			_, _ = w.Write([]byte(rec.list))
			return
		}

		rec.mu.Lock()
		rec.detailIDs = append(rec.detailIDs, id)
		rec.detailTimes = append(rec.detailTimes, time.Now())
		hook := rec.onDetail
		rec.mu.Unlock()
		if hook != nil {
			hook(id)
		}

		if status, ok := rec.detailStatus[id]; ok {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(bhDetailNotFoundBody))
			return
		}
		body, ok := rec.details[id]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(bhDetailNotFoundBody))
			return
		}
		_, _ = w.Write([]byte(body))
	}
}

func (rec *bhRecorder) detailCalls() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return len(rec.detailIDs)
}

// bhDetailID returns the posting id from a careers-site path, or "" for the list
// path (which has no "/detail" suffix). The base may carry a prefix (the templating
// test), so the id is the last segment before "/detail".
func bhDetailID(path string) string {
	trimmed, ok := strings.CutSuffix(path, "/detail")
	if !ok {
		return ""
	}
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}

// The fixtures below are the live BambooHR careers-site JSON, captured 2026-08-25
// from https://giottoai.bamboohr.com/careers/list and .../careers/<id>/detail. Values
// are verbatim — including the board's escaped forward slashes (`\/`), the trailing
// space in "Machine Learning Engineer ", the null-vs-populated split between the two
// location objects, and the \u00a0 non-breaking spaces inside a description. Only the
// description bodies are trimmed, to representative opening fragments (the real ones
// run to ~8 KB); everything kept is byte-for-byte what the board served.

// bhListGiottoAI is the whole three-posting board. Note the alternation the mapper's
// Location fallback exists for: id 36 and 42 populate `location` (locationType "2"),
// id 39 populates only `atsLocation` (locationType "1"), and neither object is ever
// populated at the same time as the other.
const bhListGiottoAI = `{"meta":{"totalCount":3},"result":[
	{"id":"36","jobOpeningName":"AI Research Scientist","departmentId":"18596","departmentLabel":"AI","employmentStatusLabel":"Full-Time","employmentType":null,"location":{"city":"Lausanne","state":"Vaud"},"atsLocation":{"country":null,"state":null,"province":null,"city":null},"isRemote":null,"locationType":"2"},
	{"id":"39","jobOpeningName":"Machine Learning Engineer ","departmentId":"18600","departmentLabel":"Engineering","employmentStatusLabel":"Contractor","employmentType":null,"location":{"city":null,"state":null},"atsLocation":{"country":"Switzerland","state":null,"province":"Vaud","city":"Lausanne"},"isRemote":null,"locationType":"1"},
	{"id":"42","jobOpeningName":"Senior Research Engineer \/ Research Scientist - Post-Training, Reinforcement Learning & Training Systems","departmentId":"18596","departmentLabel":"AI","employmentStatusLabel":"Full-Time","employmentType":null,"location":{"city":"Lausanne","state":"Vaud"},"atsLocation":{"country":null,"state":null,"province":null,"city":null},"isRemote":null,"locationType":"2"}
]}`

// bhDetail36 shows what the detail adds over the list row: the Posting Body, a
// date-only datePosted, and location.postalCode/addressCountry (the list's copy of
// `location` carries city/state only, so the country arrives here or not at all).
const bhDetail36 = `{"meta":{},"result":{"jobOpening":{
	"jobOpeningShareUrl":"https:\/\/giottoai.bamboohr.com\/careers\/36",
	"jobOpeningName":"AI Research Scientist",
	"jobOpeningStatus":"Open",
	"jobCategoryId":null,
	"departmentId":"18596",
	"departmentLabel":"AI",
	"employmentStatusLabel":"Full-Time",
	"employmentType":null,
	"location":{"city":"Lausanne","state":"Vaud","postalCode":"1003","addressCountry":"Switzerland"},
	"atsLocation":{"country":null,"countryId":null,"state":null,"city":null},
	"description":"<p>Giotto.ai is a Switzerland-based AI company building intelligence systems for Switzerland and Europe.<\/p>\n<p>Expected core stack:<\/p>\n<ul>\n<li>MLflow or Weights &amp; Biases for experiment tracking<\/li>\n<li>Docker<\/li>\n<\/ul>",
	"compensation":null,
	"datePosted":"2026-07-21",
	"minimumExperience":null,
	"locationType":"2",
	"seekPromoted":false
}}}`

// bhDetail39 is the atsLocation-only posting: its `location` object is all nulls even
// on the detail, so both Location and CountryHint have to come from the fallback. Its
// description is the \u00a0-riddled variant BambooHR's editor produces.
const bhDetail39 = `{"meta":{},"result":{"jobOpening":{
	"jobOpeningShareUrl":"https:\/\/giottoai.bamboohr.com\/careers\/39",
	"jobOpeningName":"Machine Learning Engineer ",
	"jobOpeningStatus":"Open",
	"departmentId":"18600",
	"departmentLabel":"Engineering",
	"employmentStatusLabel":"Contractor",
	"location":{"city":null,"state":null,"postalCode":null,"addressCountry":null},
	"atsLocation":{"country":"Switzerland","countryId":"204","state":"Vaud","city":"Lausanne"},
	"description":"<p><span><span>Giotto.ai is a Switzerland-based AI company building intelligence systems for\u00a0<\/span><span>Switzerland and <\/span><span>Europe.<\/span><\/span><span>\u00a0<\/span><\/p>",
	"compensation":null,
	"datePosted":"2026-07-02",
	"minimumExperience":null,
	"locationType":"1",
	"seekPromoted":false
}}}`

const bhDetail42 = `{"meta":{},"result":{"jobOpening":{
	"jobOpeningShareUrl":"https:\/\/giottoai.bamboohr.com\/careers\/42",
	"jobOpeningName":"Senior Research Engineer \/ Research Scientist - Post-Training, Reinforcement Learning & Training Systems",
	"jobOpeningStatus":"Open",
	"departmentId":"18596",
	"departmentLabel":"AI",
	"employmentStatusLabel":"Full-Time",
	"location":{"city":"Lausanne","state":"Vaud","postalCode":"1003","addressCountry":"Switzerland"},
	"atsLocation":{"country":null,"countryId":null,"state":null,"city":null},
	"description":"<p>Giotto.ai is a Switzerland-based AI company building intelligence systems for Switzerland and Europe.<br>Our mission is to enable governments and enterprises to retain control over the AI systems they use.<\/p>",
	"compensation":null,
	"datePosted":"2026-07-29",
	"minimumExperience":null,
	"locationType":"2",
	"seekPromoted":false
}}}`

// bhListEmpty is the literal body a board with no open roles serves (captured from
// alchemab.bamboohr.com, which really does have zero).
const bhListEmpty = `{"meta":{"totalCount":0},"result":[]}`

// bhDetailNotFoundBody is the board's real 404 payload for an unknown posting id.
const bhDetailNotFoundBody = `{"type":"not_found","title":"Resource not found.","details":"Looks like the id you provided doesn't exist.","meta":[]}`

// bhGiottoRecorder wires the whole captured giottoai board — list plus all three
// details — into a recorder.
func bhGiottoRecorder() *bhRecorder {
	return &bhRecorder{
		list: bhListGiottoAI,
		details: map[string]string{
			"36": bhDetail36,
			"39": bhDetail39,
			"42": bhDetail42,
		},
	}
}

func TestBambooHRFetchMapsBoard(t *testing.T) {
	rec := bhGiottoRecorder()
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "giottoai")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3", len(got))
	}

	first := got[0]
	if first.Title != "AI Research Scientist" {
		t.Errorf("Title = %q, want the jobOpeningName", first.Title)
	}
	// The canonical URL is CONSTRUCTED from tenant + id and must equal the board's own
	// jobOpeningShareUrl, so the upsert key never depends on the detail call.
	if first.URL != "https://giottoai.bamboohr.com/careers/36" {
		t.Errorf("URL = %q, want the constructed share URL", first.URL)
	}
	if first.SourceID != "36" {
		t.Errorf("SourceID = %q, want the posting id (URL re-slug stable, ADR-0034)", first.SourceID)
	}
	// The detail's location adds addressCountry, which the list's copy lacks.
	if first.Location != "Lausanne, Vaud, Switzerland" {
		t.Errorf("Location = %q, want the detail-enriched location", first.Location)
	}
	if first.CountryHint != "Switzerland" {
		t.Errorf("CountryHint = %q, want location.addressCountry", first.CountryHint)
	}
	if first.Department != "AI" {
		t.Errorf("Department = %q, want the departmentLabel", first.Department)
	}
	if first.WorkArrangement != crawler.WorkArrangementHybrid {
		t.Errorf("WorkArrangement = %q, want hybrid for locationType %q", first.WorkArrangement, "2")
	}
	wantDesc := "Giotto.ai is a Switzerland-based AI company building intelligence systems for Switzerland and Europe. " +
		"Expected core stack: MLflow or Weights & Biases for experiment tracking Docker"
	if first.Description != wantDesc {
		t.Errorf("Description = %q, want %q", first.Description, wantDesc)
	}
	// datePosted is date-only, so FirstPublished lands at midnight UTC.
	wantTime := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	if !first.FirstPublished.Equal(wantTime) {
		t.Errorf("FirstPublished = %v, want %v (date-only → midnight UTC)", first.FirstPublished, wantTime)
	}
	// Company/CompanyKey belong to the ingest lane, stamped from the page's Owner
	// (ADR-0022) — a fetcher must never fill them.
	if first.Company != "" || first.CompanyKey != "" {
		t.Errorf("Company/CompanyKey = %q/%q, want both empty (the lane stamps them from Owner)", first.Company, first.CompanyKey)
	}

	// The atsLocation-only posting: nothing in `location`, everything in the fallback.
	second := got[1]
	if second.Title != "Machine Learning Engineer" {
		t.Errorf("Title = %q, want the jobOpeningName with its trailing space trimmed", second.Title)
	}
	if second.URL != "https://giottoai.bamboohr.com/careers/39" {
		t.Errorf("URL = %q, want the constructed share URL", second.URL)
	}
	if second.Location != "Lausanne, Vaud, Switzerland" {
		t.Errorf("Location = %q, want the atsLocation fallback", second.Location)
	}
	if second.CountryHint != "Switzerland" {
		t.Errorf("CountryHint = %q, want atsLocation.country", second.CountryHint)
	}
	if second.WorkArrangement != crawler.WorkArrangementRemote {
		t.Errorf("WorkArrangement = %q, want remote for locationType %q", second.WorkArrangement, "1")
	}
	// The NBSPs collapse like any other whitespace.
	wantSecondDesc := "Giotto.ai is a Switzerland-based AI company building intelligence systems for Switzerland and Europe."
	if second.Description != wantSecondDesc {
		t.Errorf("Description = %q, want %q", second.Description, wantSecondDesc)
	}

	if got[2].Title != "Senior Research Engineer / Research Scientist - Post-Training, Reinforcement Learning & Training Systems" {
		t.Errorf("Title = %q, want the escaped-slash title decoded", got[2].Title)
	}
	if rec.detailCalls() != 3 {
		t.Errorf("detail calls = %d, want 3 (one per posting)", rec.detailCalls())
	}
}

func TestBambooHREmptyBoard(t *testing.T) {
	// A board with no open roles is complete and empty: an empty NON-NIL slice, no
	// error, and — since there is nothing to enrich — not one detail call.
	rec := &bhRecorder{list: bhListEmpty}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "alchemab")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got == nil {
		t.Fatal("listings = nil, want an empty non-nil slice for an empty board")
	}
	if len(got) != 0 {
		t.Errorf("got %d listings, want 0", len(got))
	}
	if rec.detailCalls() != 0 {
		t.Errorf("detail calls = %d, want 0 on an empty board", rec.detailCalls())
	}
}

func TestBambooHRLocationFallback(t *testing.T) {
	// `location` and `atsLocation` are ALTERNATES, not parts of one address: across 51
	// live postings exactly one of the two is ever populated. So the composer prefers
	// `location` and falls back to `atsLocation`, never merging the two.
	cases := []struct {
		name        string
		detail      string
		wantLoc     string
		wantCountry string
	}{
		{
			name:        "location populated wins",
			detail:      bhDetailWith(`"location":{"city":"Saskatoon","state":"Saskatchewan","postalCode":"S7K 5T6","addressCountry":"Canada"},"atsLocation":{"country":null,"state":null,"city":null}`, ""),
			wantLoc:     "Saskatoon, Saskatchewan, Canada", // postalCode is deliberately excluded
			wantCountry: "Canada",
		},
		{
			name:        "atsLocation is the fallback",
			detail:      bhDetailWith(`"location":{"city":null,"state":null,"postalCode":null,"addressCountry":null},"atsLocation":{"country":"Slovenia","countryId":"195","state":null,"city":null}`, ""),
			wantLoc:     "Slovenia",
			wantCountry: "Slovenia",
		},
		{
			name:        "both empty keeps the posting with no location",
			detail:      bhDetailWith(`"location":{"city":null,"state":null},"atsLocation":{"country":null,"state":null,"city":null}`, ""),
			wantLoc:     "",
			wantCountry: "",
		},
		{
			name:        "a city equal to its state is not repeated",
			detail:      bhDetailWith(`"location":{"city":"Berlin","state":"berlin","addressCountry":"Germany"},"atsLocation":{}`, ""),
			wantLoc:     "Berlin, Germany",
			wantCountry: "Germany",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &bhRecorder{
				list:    `{"meta":{"totalCount":1},"result":[{"id":"1","jobOpeningName":"Role","location":{},"atsLocation":{}}]}`,
				details: map[string]string{"1": tc.detail},
			}
			fetcher := newBambooHRFetcher(t, rec.handler())

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].Location != tc.wantLoc {
				t.Errorf("Location = %q, want %q", got[0].Location, tc.wantLoc)
			}
			if got[0].CountryHint != tc.wantCountry {
				t.Errorf("CountryHint = %q, want %q", got[0].CountryHint, tc.wantCountry)
			}
		})
	}
}

// bhDetailWith builds a one-posting detail body carrying the given raw location JSON
// and locationType, so the location and work-arrangement tables can vary one field at
// a time against the real envelope shape.
func bhDetailWith(locationJSON, locationType string) string {
	return `{"meta":{},"result":{"jobOpening":{
		"jobOpeningName":"Role",
		"jobOpeningStatus":"Open",
		"departmentLabel":"Engineering",
		` + locationJSON + `,
		"description":"<p>Body.</p>",
		"datePosted":"2026-07-21",
		"locationType":"` + locationType + `"
	}}}`
}

func TestBambooHRWorkArrangement(t *testing.T) {
	// BambooHR's own board renderer appends "Remote" for locationType 1 and "(Hybrid)"
	// for 2, but prints NO arrangement word for 0 — the provider itself declines to
	// characterise it. ADR-0030: a source that does not positively state the mode is
	// unspecified, never onsite.
	cases := []struct {
		locationType string
		want         crawler.WorkArrangement
	}{
		{"1", crawler.WorkArrangementRemote},
		{"2", crawler.WorkArrangementHybrid},
		{"0", crawler.WorkArrangementUnspecified},
		{"", crawler.WorkArrangementUnspecified},
		{"7", crawler.WorkArrangementUnspecified},
	}
	for _, tc := range cases {
		t.Run("locationType "+tc.locationType, func(t *testing.T) {
			rec := &bhRecorder{
				list: `{"meta":{"totalCount":1},"result":[{"id":"1","jobOpeningName":"Role","locationType":"` + tc.locationType + `","location":{"city":"Lausanne"},"atsLocation":{}}]}`,
				details: map[string]string{
					"1": bhDetailWith(`"location":{"city":"Lausanne"},"atsLocation":{}`, tc.locationType),
				},
			}
			fetcher := newBambooHRFetcher(t, rec.handler())

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1", len(got))
			}
			if got[0].WorkArrangement != tc.want {
				t.Errorf("WorkArrangement = %q, want %q for locationType %q", got[0].WorkArrangement, tc.want, tc.locationType)
			}
		})
	}
}

func TestBambooHRDescriptionStripsSingleEncodedHTML(t *testing.T) {
	// The Posting Body is single-encoded real HTML: literal tags, text-level entities
	// escaped exactly once, and literal non-breaking spaces. The reduction must strip
	// the tags, decode &amp; to "&" ONCE, keep an entity-encoded angle bracket as text,
	// and collapse the NBSPs — i.e. the single-encode helper, not the double-encode one.
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":1},"result":[{"id":"1","jobOpeningName":"Role","location":{},"atsLocation":{}}]}`,
		details: map[string]string{
			"1": `{"meta":{},"result":{"jobOpening":{"jobOpeningName":"Role","description":"<p><span>Research &amp; development\u00a0with teams of &lt;10 engineers.<\/span><\/p>\n<ul>\n<li>Docker<\/li>\n<\/ul>","datePosted":"2026-07-21","location":{},"atsLocation":{}}}}`,
		},
	}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	want := "Research & development with teams of <10 engineers. Docker"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
}

func TestBambooHRMalformedDatePosted(t *testing.T) {
	// A bad or absent timestamp leaves FirstPublished zero and must NEVER drop the
	// posting.
	cases := []struct {
		name       string
		datePosted string
		want       time.Time
	}{
		{"absent", "", time.Time{}},
		{"unparseable", "not-a-date", time.Time{}},
		{"date-only", "2026-07-21", time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &bhRecorder{
				list: `{"meta":{"totalCount":1},"result":[{"id":"1","jobOpeningName":"Role","location":{},"atsLocation":{}}]}`,
				details: map[string]string{
					"1": `{"meta":{},"result":{"jobOpening":{"jobOpeningName":"Role","description":"<p>Body.</p>","datePosted":"` + tc.datePosted + `","location":{},"atsLocation":{}}}}`,
				},
			}
			fetcher := newBambooHRFetcher(t, rec.handler())

			got, err := fetcher.Fetch(t.Context(), "acme")
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d listings, want 1 (a bad timestamp never drops a posting)", len(got))
			}
			if !got[0].FirstPublished.Equal(tc.want) {
				t.Errorf("FirstPublished = %v, want %v", got[0].FirstPublished, tc.want)
			}
		})
	}
}

func TestBambooHRListNon200ReturnsErrBoardStatus(t *testing.T) {
	rec := &bhRecorder{listStatus: http.StatusNotFound}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "missing")
	if err == nil {
		t.Fatal("Fetch err = nil, want ErrBoardStatus on a non-200 list")
	}
	if !errors.Is(err, ats.ErrBoardStatus) {
		t.Errorf("err = %v, want it to wrap ErrBoardStatus", err)
	}
	// The code must survive the wrap so classifyBoard can tell a dead board from a
	// throttled one (ADR-0035).
	var se *ats.BoardStatusError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As(err, *BoardStatusError) = false, want the status code to survive")
	}
	if se.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d", se.StatusCode, http.StatusNotFound)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a board-level failure", got)
	}
}

func TestBambooHRRedirectOffBoardHostIsDeadBoard(t *testing.T) {
	// A nonexistent or deactivated tenant does not 404: BambooHR 302s to its marketing
	// site, which then answers a non-JSON page. Without the off-host guard that would
	// read as a status/decode error → ProbeInconclusive, so a genuinely dead board
	// would never tip its Career Page dormant and its Open listings would never Close
	// (ADR-0035). The guard reports it as 404 → ProbeDead.
	marketing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<!DOCTYPE html><html><body>BambooHR</body></html>"))
	}))
	t.Cleanup(marketing.Close)
	board := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, marketing.URL+"/", http.StatusFound)
	}))
	t.Cleanup(board.Close)
	fetcher := ats.NewBambooHRFetcher(ats.WithBambooHRBaseURL(board.URL), ats.WithBambooHRDetailDelay(0))

	got, err := fetcher.Fetch(t.Context(), "nosuchtenant")
	if err == nil {
		t.Fatal("Fetch err = nil, want a dead-board error when the board redirects off-host")
	}
	var se *ats.BoardStatusError
	if !errors.As(err, &se) {
		t.Fatalf("errors.As(err, *BoardStatusError) = false, want a board-status error; got %v", err)
	}
	if se.StatusCode != http.StatusNotFound {
		t.Errorf("StatusCode = %d, want %d so classifyBoard reads ProbeDead", se.StatusCode, http.StatusNotFound)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for a dead board", got)
	}
}

func TestBambooHRDetail404SkipsPostingAndMarksIncomplete(t *testing.T) {
	// A 404/410 detail means the posting went away between the list and the detail.
	// It is DROPPED rather than saved list-derived: upserting a gone posting would
	// reopen it in place (ADR-0035). The board is then not provably whole, so the
	// sweep is skipped.
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":2},"result":[
			{"id":"1","jobOpeningName":"Kept","location":{"city":"Berlin"},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Gone","location":{"city":"Berlin"},"atsLocation":{}}
		]}`,
		details: map[string]string{
			"1": bhDetailWith(`"location":{"city":"Berlin"},"atsLocation":{}`, "0"),
		},
		detailStatus: map[string]int{"2": http.StatusNotFound},
	}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want ErrBoardIncomplete after a failed detail", err)
	}
	if got == nil {
		t.Fatal("listings = nil, want the presence sample alongside ErrBoardIncomplete")
	}
	if len(got) != 1 {
		t.Fatalf("got %d listings, want 1 (the 404'd posting is dropped)", len(got))
	}
	if got[0].SourceID != "1" {
		t.Errorf("kept SourceID = %q, want the posting whose detail resolved", got[0].SourceID)
	}
}

func TestBambooHRDetail429KeepsPostingAndMarksIncomplete(t *testing.T) {
	// A 429/5xx detail means upstream is degraded — the posting is NOT known gone, so
	// its list-derived form is KEPT (identity, URL, title, location intact; no Posting
	// Body). Collapsing this into the 404 bucket is the #140 bug.
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":2},"result":[
			{"id":"1","jobOpeningName":"Enriched","location":{"city":"Berlin"},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Throttled","departmentLabel":"Sales","locationType":"1","location":{},"atsLocation":{"country":"Slovenia"}}
		]}`,
		details: map[string]string{
			"1": bhDetailWith(`"location":{"city":"Berlin"},"atsLocation":{}`, "0"),
		},
		detailStatus: map[string]int{"2": http.StatusTooManyRequests},
	}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want ErrBoardIncomplete after a throttled detail", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d listings, want 2 (a throttled detail must not drop the posting)", len(got))
	}
	throttled := got[1]
	if throttled.Title != "Throttled" {
		t.Errorf("Title = %q, want the list-derived title", throttled.Title)
	}
	if throttled.URL != "https://acme.bamboohr.com/careers/2" {
		t.Errorf("URL = %q, want the list-derived canonical URL", throttled.URL)
	}
	if throttled.Location != "Slovenia" || throttled.CountryHint != "Slovenia" {
		t.Errorf("Location/CountryHint = %q/%q, want the list-derived atsLocation values", throttled.Location, throttled.CountryHint)
	}
	if throttled.WorkArrangement != crawler.WorkArrangementRemote {
		t.Errorf("WorkArrangement = %q, want the list-derived remote", throttled.WorkArrangement)
	}
	if throttled.Description != "" {
		t.Errorf("Description = %q, want empty — the detail that carries it failed", throttled.Description)
	}
}

func TestBambooHRIncompleteOnCountMismatch(t *testing.T) {
	// meta.totalCount is the completeness oracle: a list shorter than it claims (an
	// undiscovered server-side cap, say) degrades to skip-the-sweep, never a mass-close.
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":5},"result":[
			{"id":"1","jobOpeningName":"One","location":{},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Two","location":{},"atsLocation":{}}
		]}`,
		details: map[string]string{
			"1": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
			"2": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
		},
	}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want ErrBoardIncomplete when the mapped count falls short of totalCount", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d listings, want the 2 the board actually delivered", len(got))
	}
}

func TestBambooHRSkipsPostingWithoutID(t *testing.T) {
	// The canonical URL is constructed from the id, so a row without one has no upsert
	// key and cannot be saved. The count cross-check reports the resulting shortfall.
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":2},"result":[
			{"id":"","jobOpeningName":"No id","location":{},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Fine","location":{},"atsLocation":{}}
		]}`,
		details: map[string]string{"2": bhDetailWith(`"location":{},"atsLocation":{}`, "0")},
	}
	fetcher := newBambooHRFetcher(t, rec.handler())

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want ErrBoardIncomplete when a row is dropped for a missing id", err)
	}
	if len(got) != 1 || got[0].SourceID != "2" {
		t.Fatalf("got %v, want only the posting that has an id", got)
	}
	if calls := rec.detailCalls(); calls != 1 {
		t.Errorf("detail calls = %d, want 1 — an id-less row is skipped before its detail", calls)
	}
}

func TestBambooHRTruncatedBodyIsHardError(t *testing.T) {
	// A body cut mid-JSON surfaces as a decode error, never a silent partial and never
	// ErrBoardIncomplete: truncation is a hard failure (ADR-0035).
	fetcher := newBambooHRFetcher(t, serveJSON(`{"meta":{"totalCount":2},"result":[{"id":"1"`))

	got, err := fetcher.Fetch(t.Context(), "acme")
	if err == nil {
		t.Fatal("want a decode error for a truncated body")
	}
	if errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatal("a truncated read is a hard error, never ErrBoardIncomplete")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil on a hard failure", got)
	}
}

func TestBambooHREmptyTenant(t *testing.T) {
	// An empty tenant is a caller bug the fetcher rejects BEFORE any request — the
	// default base would otherwise build the bogus host "https://.bamboohr.com".
	rt := &bhCountingTransport{}
	fetcher := ats.NewBambooHRFetcher(ats.WithBambooHRHTTPClient(&http.Client{Transport: rt}))

	got, err := fetcher.Fetch(t.Context(), "")
	if err == nil {
		t.Fatal("Fetch(\"\") err = nil, want an error for an empty tenant")
	}
	if got != nil {
		t.Errorf("listings = %v, want nil for an empty tenant", got)
	}
	if rt.count != 0 {
		t.Errorf("issued %d requests, want 0 — the guard runs before any templating", rt.count)
	}
}

// bhCountingTransport counts requests and replies with an empty board, so a test can
// assert a request was (or was not) issued without a real server.
type bhCountingTransport struct {
	mu    sync.Mutex
	count int
	host  string
	path  string
}

func (rt *bhCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.mu.Lock()
	rt.count++
	rt.host = req.URL.Host
	rt.path = req.URL.Path
	rt.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(bhListEmpty)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}, nil
}

func TestBambooHRSendsNoAuthHeader(t *testing.T) {
	// /careers/list and /careers/<id>/detail are zero-auth; the credentialed
	// api.bamboohr.com gateway API (HTTP Basic, API key) is the dual-API trap. No auth
	// header may be sent on the list OR on any detail.
	rec := bhGiottoRecorder()
	fetcher := newBambooHRFetcher(t, rec.handler())

	if _, err := fetcher.Fetch(t.Context(), "giottoai"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.authHeaders) != 4 {
		t.Fatalf("recorded %d requests, want 4 (one list + three details)", len(rec.authHeaders))
	}
	for i, got := range rec.authHeaders {
		if got != "" {
			t.Errorf("request %d Authorization = %q, want it unset (public zero-auth endpoints)", i, got)
		}
	}
	for i, got := range rec.apiKeyHeader {
		if got != "" {
			t.Errorf("request %d X-Api-Key = %q, want it unset", i, got)
		}
	}
}

func TestBambooHRDetailCallsArePaced(t *testing.T) {
	// The N+1 is paced BETWEEN successive detail calls and never before the first
	// (#140): three postings means two gaps.
	const delay = 30 * time.Millisecond
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":3},"result":[
			{"id":"1","jobOpeningName":"One","location":{},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Two","location":{},"atsLocation":{}},
			{"id":"3","jobOpeningName":"Three","location":{},"atsLocation":{}}
		]}`,
		details: map[string]string{
			"1": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
			"2": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
			"3": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
		},
	}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)
	fetcher := ats.NewBambooHRFetcher(ats.WithBambooHRBaseURL(srv.URL), ats.WithBambooHRDetailDelay(delay))

	start := time.Now()
	got, err := fetcher.Fetch(t.Context(), "acme")
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3", len(got))
	}
	if elapsed < 2*delay {
		t.Errorf("elapsed = %v, want at least %v (two inter-detail gaps)", elapsed, 2*delay)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for i := 1; i < len(rec.detailTimes); i++ {
		if gap := rec.detailTimes[i].Sub(rec.detailTimes[i-1]); gap < delay {
			t.Errorf("gap between detail %d and %d = %v, want at least %v", i-1, i, gap, delay)
		}
	}
}

func TestBambooHRCancelDuringPacingReturnsError(t *testing.T) {
	// The inter-detail wait is context-aware, not a bare time.Sleep: cancelling during
	// it returns promptly with a context error rather than holding the pool worker for
	// the full interval, and the next detail is never issued.
	const delay = 5 * time.Second
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":2},"result":[
			{"id":"1","jobOpeningName":"One","location":{},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Two","location":{},"atsLocation":{}}
		]}`,
		details: map[string]string{
			"1": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
			"2": bhDetailWith(`"location":{},"atsLocation":{}`, "0"),
		},
	}
	rec.onDetail = func(id string) {
		if id == "1" {
			cancel()
		}
	}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)
	fetcher := ats.NewBambooHRFetcher(ats.WithBambooHRBaseURL(srv.URL), ats.WithBambooHRDetailDelay(delay))

	start := time.Now()
	got, err := fetcher.Fetch(ctx, "acme")
	elapsed := time.Since(start)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil (no silently partial board)", got)
	}
	if elapsed >= delay {
		t.Errorf("elapsed = %v, want well under the %v pacing interval — the wait must honour ctx.Done", elapsed, delay)
	}
	if calls := rec.detailCalls(); calls != 1 {
		t.Errorf("detail calls = %d, want 1 — the cancelled run must not issue the next detail", calls)
	}
}

func TestBambooHRCancelDuringFinalDetailReturnsError(t *testing.T) {
	// A context cancelled while the LAST posting's detail is in flight must surface as
	// an error, not a silently partial board: that iteration has no successor to catch
	// it at the top of the loop. The handler cancels the run and then holds the
	// connection open so the in-flight GET fails with context.Canceled.
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	list := `{"meta":{"totalCount":1},"result":[{"id":"only","jobOpeningName":"Only","location":{},"atsLocation":{}}]}`

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if bhDetailID(r.URL.Path) == "" {
			_, _ = w.Write([]byte(list))
			return
		}
		cancel()
		<-r.Context().Done()
	}
	fetcher := newBambooHRFetcher(t, handler)

	got, err := fetcher.Fetch(ctx, "acme")
	if err == nil {
		t.Fatalf("Fetch err = nil, want a context error for cancellation during the final detail; got %d listings", len(got))
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
	if got != nil {
		t.Errorf("listings = %v, want nil (no silently partial board)", got)
	}
}

func TestBambooHRDetailBudget(t *testing.T) {
	// Once the detail budget is spent the remaining postings are still emitted from
	// their list rows — identity is never lost, only the Posting Body — and the fetch
	// is marked incomplete so the absence-sweep is skipped.
	rec := &bhRecorder{
		list: `{"meta":{"totalCount":3},"result":[
			{"id":"1","jobOpeningName":"One","location":{"city":"Berlin"},"atsLocation":{}},
			{"id":"2","jobOpeningName":"Two","location":{"city":"Berlin"},"atsLocation":{}},
			{"id":"3","jobOpeningName":"Three","location":{"city":"Berlin"},"atsLocation":{}}
		]}`,
		details: map[string]string{
			"1": bhDetailWith(`"location":{"city":"Berlin","addressCountry":"Germany"},"atsLocation":{}`, "0"),
			"2": bhDetailWith(`"location":{"city":"Berlin","addressCountry":"Germany"},"atsLocation":{}`, "0"),
			"3": bhDetailWith(`"location":{"city":"Berlin","addressCountry":"Germany"},"atsLocation":{}`, "0"),
		},
	}
	srv := httptest.NewServer(rec.handler())
	t.Cleanup(srv.Close)
	fetcher := ats.NewBambooHRFetcher(
		ats.WithBambooHRBaseURL(srv.URL),
		ats.WithBambooHRDetailDelay(0),
		ats.WithBambooHRMaxDetails(1),
	)

	got, err := fetcher.Fetch(t.Context(), "acme")
	if !errors.Is(err, ats.ErrBoardIncomplete) {
		t.Fatalf("err = %v, want ErrBoardIncomplete once the detail budget is spent", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d listings, want 3 — the budget costs enrichment, never a posting", len(got))
	}
	if calls := rec.detailCalls(); calls != 1 {
		t.Errorf("detail calls = %d, want 1 (the budget)", calls)
	}
	if got[0].Description == "" {
		t.Error("the budgeted-for posting has no Description, want the detail's Posting Body")
	}
	if got[2].Description != "" {
		t.Errorf("Description = %q, want empty for a posting past the budget", got[2].Description)
	}
	if got[2].URL != "https://acme.bamboohr.com/careers/3" {
		t.Errorf("URL = %q, want the list-derived canonical URL past the budget", got[2].URL)
	}
}

func TestBambooHRTemplatesTenantSlug(t *testing.T) {
	// The ATS Fetch lane passes the leftmost catalog label (the tenant slug), which the
	// base URL templates into the board host. A test base carrying the {tenant}
	// placeholder in the path proves the substitution without needing a real host; that
	// the DEFAULT base's .bamboohr.com suffix reconstructs the real board host from the
	// slug is pinned end-to-end by collection.TestSubdomainProviderHostContract.
	var gotPath string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bhListEmpty))
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	fetcher := ats.NewBambooHRFetcher(ats.WithBambooHRBaseURL(srv.URL + "/{tenant}"))

	if _, err := fetcher.Fetch(t.Context(), "demo"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if gotPath != "/demo/careers/list" {
		t.Errorf("request path = %q, want %q (tenant slug substituted)", gotPath, "/demo/careers/list")
	}
}

func TestBambooHRDefaultBaseTemplatesTheBoardHost(t *testing.T) {
	// The default base must carry the .bamboohr.com suffix: a bare "https://{tenant}"
	// would build the unresolvable host "giottoai" while every test above (which
	// overrides the base) stayed green.
	rt := &bhCountingTransport{}
	fetcher := ats.NewBambooHRFetcher(ats.WithBambooHRHTTPClient(&http.Client{Transport: rt}))

	if _, err := fetcher.Fetch(t.Context(), "giottoai"); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if rt.host != "giottoai.bamboohr.com" {
		t.Errorf("requested host = %q, want %q", rt.host, "giottoai.bamboohr.com")
	}
	if rt.path != "/careers/list" {
		t.Errorf("requested path = %q, want %q", rt.path, "/careers/list")
	}
}

func TestBambooHRCatalogRecognition(t *testing.T) {
	// ProviderBambooHR MUST equal what catalog.Identify emits, or seed-time routing
	// cannot resolve the fetcher against the Registry (#127). internal/ats stays
	// decoupled from catalog, so this test is the pin.
	u, err := crawler.NewURL("https://giottoai.bamboohr.com/careers/36")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	id := catalog.Identify(u)
	if id.ATSProvider != ats.ProviderBambooHR {
		t.Errorf("ATSProvider = %q, want %q", id.ATSProvider, ats.ProviderBambooHR)
	}
	if id.CompanyKey != "bamboohr:giottoai" {
		t.Errorf("CompanyKey = %q, want %q (the tenant slug the lane hands the fetcher)", id.CompanyKey, "bamboohr:giottoai")
	}
}
