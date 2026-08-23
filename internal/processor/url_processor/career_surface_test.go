package urlprocessor_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	urlprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/url_processor"
)

// The page the walk is on, and the five links it offers. Only the first sits in the
// page's main region; the other four sit in the site's Site Chrome, and the three
// rules of the Career Surface (ADR-0051) decide which of those the walk still
// follows. The page path carries no career token, so each case below is decided by
// exactly one rule.
const (
	careerSurfacePage = "https://acme.com/open-roles"

	inMainRegion  = "/open-roles/senior-go-engineer" // rule 1: the main region
	careerToken   = "/karriere/stellen"              // rule 2: career-shaped, in the chrome
	samePathQuery = "/open-roles?page=2"             // rule 3: same path, query differs
	plainChrome   = "/solutions"                     // on no rule: Site Chrome
	offPathQuery  = "/kontakt?page=2"                // on no rule: a query, but another path
)

// careerSurfaceContent is the page as the walk's parser produces it (ADR-0051):
// MainRegionURLs is a SUBSET of the unchanged whole-document URLs, spelled with the
// identical raw hrefs, which is what lets the walk test membership by string.
func careerSurfaceContent() *crawler.Content {
	return &crawler.Content{
		Title:          "Open roles",
		MainContent:    "body",
		URLs:           []string{inMainRegion, careerToken, samePathQuery, plainChrome, offPathQuery},
		MainRegionURLs: []string{inMainRegion},
	}
}

// careerPath and careerHost are rule 2's two halves, built exactly as cmd/server
// builds them: out of the crawl definition's own pass vocabulary, not a second,
// looser one. They are kept apart because the walk applies the host half only across
// hosts (see TestProcessCareerSurfaceOnACareerSubdomain).
func careerPath() func(string) bool {
	passPathSegments := urlfilter.PassPathSegments("jobs", "karriere")
	return func(u string) bool { return errors.Is(passPathSegments(u), filter.ErrPass) }
}

func careerHost() func(string) bool {
	passSubdomains := urlfilter.PassSubdomains("jobs", "karriere")
	return func(u string) bool { return errors.Is(passSubdomains(u), filter.ErrPass) }
}

// TestProcessCareerSurface proves the walk follows a page's Career Surface and drops
// the rest of its host's Site Chrome (ADR-0051) — and that the kill switch restores
// the whole-document harvest exactly.
func TestProcessCareerSurface(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		want    []string
	}{
		{
			// Site Chrome is 67.4% of what a Career Page links, and following it is why a
			// bounded Cycle's walk does not drain. The two chrome links that DO survive are
			// the two ways rule 1 alone loses the very links the walk exists for: an
			// opening a site lists only in its menu, and a paginated board whose control is
			// the <nav aria-label="Pagination"> the HTML spec recommends.
			name:    "on, the walk follows the career surface and drops the rest",
			enabled: true,
			want: []string{
				"https://acme.com/open-roles/senior-go-engineer",
				"https://acme.com/karriere/stellen",
				"https://acme.com/open-roles?page=2",
			},
		},
		{
			// COLLECTION_CAREER_SURFACE_LINKS off: today's whole-document harvest, every
			// link on the page, in document order.
			name:    "off, every link on the page is followed",
			enabled: false,
			want: []string{
				"https://acme.com/open-roles/senior-go-engineer",
				"https://acme.com/karriere/stellen",
				"https://acme.com/open-roles?page=2",
				"https://acme.com/solutions",
				"https://acme.com/kontakt?page=2",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := &stubFrontier{}
			worker := urlprocessor.NewProcessor(&urlprocessor.Config{
				Frontier:           fr,
				Downloader:         &stubDownloader{content: []byte("<html></html>")},
				Parser:             &stubParser{content: careerSurfaceContent()},
				ContentFilter:      func(*crawler.Content) error { return nil },
				URLFilter:          func(string) error { return nil },
				RobotsTxtChecker:   stubRobots{},
				RelevanceFilter:    func(*crawler.Content) error { return errors.New("not a listing") },
				OnJobListing:       func(context.Context, *crawler.RawJobListing) error { return nil },
				CareerSurfaceLinks: tt.enabled,
				CareerPath:         careerPath(),
				CareerHost:         careerHost(),
			})

			page, err := crawler.NewURL(careerSurfacePage)
			if err != nil {
				t.Fatalf("NewURL: %v", err)
			}
			if err := worker.Process(t.Context(), &page); err != nil {
				t.Fatalf("Process returned error: %v", err)
			}

			got := make([]string, len(fr.added))
			for i, u := range fr.added {
				got[i] = u.RawURL
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("enqueued URLs = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestProcessCareerSurfaceKeepsDepth pins the depth decision the ADR took explicitly:
// a same-path variant increments depth like any other link. maxDepth is the only
// thing keeping a faceted board's query variants finite, so exempting rule 3 would
// remove the sole bound on the one growth this decision accepts.
func TestProcessCareerSurfaceKeepsDepth(t *testing.T) {
	fr := &stubFrontier{}
	worker := urlprocessor.NewProcessor(&urlprocessor.Config{
		Frontier:           fr,
		Downloader:         &stubDownloader{content: []byte("<html></html>")},
		Parser:             &stubParser{content: &crawler.Content{URLs: []string{samePathQuery}}},
		ContentFilter:      func(*crawler.Content) error { return nil },
		URLFilter:          func(string) error { return nil },
		RobotsTxtChecker:   stubRobots{},
		RelevanceFilter:    func(*crawler.Content) error { return errors.New("not a listing") },
		OnJobListing:       func(context.Context, *crawler.RawJobListing) error { return nil },
		CareerSurfaceLinks: true,
		CareerPath:         careerPath(),
		CareerHost:         careerHost(),
	})

	page, err := crawler.NewURL(careerSurfacePage)
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	page.Depth = 3
	if err := worker.Process(t.Context(), &page); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	if len(fr.added) != 1 {
		t.Fatalf("want the same-path variant enqueued, got %v", fr.added)
	}
	if fr.added[0].Depth != 4 {
		t.Errorf("same-path variant enqueued at depth %d, want 4 (one deeper than its page)", fr.added[0].Depth)
	}
}

// TestProcessCareerSurfaceAtASiteRoot covers rule 3 on a seed with no path — a
// Pageless Company's Website. The root's trailing slash is the one spelling
// normalization leaves optional, so a paginator there is the case where a naive
// string compare silently drops the second half of a board.
func TestProcessCareerSurfaceAtASiteRoot(t *testing.T) {
	fr := &stubFrontier{}
	worker := urlprocessor.NewProcessor(&urlprocessor.Config{
		Frontier:           fr,
		Downloader:         &stubDownloader{content: []byte("<html></html>")},
		Parser:             &stubParser{content: &crawler.Content{URLs: []string{"/?page=2", "/solutions"}}},
		ContentFilter:      func(*crawler.Content) error { return nil },
		URLFilter:          func(string) error { return nil },
		RobotsTxtChecker:   stubRobots{},
		RelevanceFilter:    func(*crawler.Content) error { return errors.New("not a listing") },
		OnJobListing:       func(context.Context, *crawler.RawJobListing) error { return nil },
		CareerSurfaceLinks: true,
		CareerPath:         careerPath(),
		CareerHost:         careerHost(),
	})

	page, err := crawler.NewURL("https://acme.com")
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	if err := worker.Process(t.Context(), &page); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	got := make([]string, len(fr.added))
	for i, u := range fr.added {
		got[i] = u.RawURL
	}
	want := []string{"https://acme.com/?page=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enqueued URLs = %v, want %v", got, want)
	}
}

// TestProcessCareerSurfaceWithoutCareerShaped pins the documented contract of
// Config.CareerPath / Config.CareerHost: a nil predicate reports false for EVERY URL,
// so a Config that supplies neither falls back to rules 1 and 3 instead of following everything. The opposite
// reading -- "no predicate, so nothing is excluded" -- would quietly turn the Career
// Surface off for any caller that forgot to wire rule 2, which is the one failure
// the switch cannot be used to diagnose: the walk would look exactly as it does with
// COLLECTION_CAREER_SURFACE_LINKS pulled.
func TestProcessCareerSurfaceWithoutCareerShaped(t *testing.T) {
	fr := &stubFrontier{}
	worker := urlprocessor.NewProcessor(&urlprocessor.Config{
		Frontier:         fr,
		Downloader:       &stubDownloader{content: []byte("<html></html>")},
		Parser:           &stubParser{content: careerSurfaceContent()},
		ContentFilter:    func(*crawler.Content) error { return nil },
		URLFilter:        func(string) error { return nil },
		RobotsTxtChecker: stubRobots{},
		RelevanceFilter:  func(*crawler.Content) error { return errors.New("not a listing") },
		OnJobListing:     func(context.Context, *crawler.RawJobListing) error { return nil },
		// Rule 2 left unwired, which is what this test is about.
		CareerSurfaceLinks: true,
	})

	page, err := crawler.NewURL(careerSurfacePage)
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	if err := worker.Process(t.Context(), &page); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	got := make([]string, len(fr.added))
	for i, u := range fr.added {
		got[i] = u.RawURL
	}
	// The career-shaped chrome link is gone with rule 2; rules 1 and 3 still hold.
	want := []string{
		"https://acme.com/open-roles/senior-go-engineer",
		"https://acme.com/open-roles?page=2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enqueued URLs = %v, want %v", got, want)
	}
}

// TestProcessCareerSurfaceOnACareerSubdomain pins the qualifier that keeps rule 2 from
// switching the Career Surface off (ADR-0051). PassSubdomains matches the LINK's host,
// so on a seed already sitting on a career subdomain EVERY same-host link is
// career-shaped and the Surface collapses to the whole-document walk it exists to
// replace — measured live, a fifth of the Catalog's seeds. The subdomain half therefore
// fires only across hosts; the path half, being segment-scoped, still fires anywhere.
func TestProcessCareerSurfaceOnACareerSubdomain(t *testing.T) {
	const (
		boardPage    = "https://jobs.acme.com/openings"
		boardListing = "/openings/senior-go-engineer" // rule 1: the main region
		boardPosting = "/jobs/4711"                   // rule 2 path half: career segment
		otherHost    = "https://karriere.acme.com/stelle-88"
		boardChrome  = "/impressum" // same host, no career segment: Site Chrome
	)
	fr := &stubFrontier{}
	worker := urlprocessor.NewProcessor(&urlprocessor.Config{
		Frontier:      fr,
		Downloader:    &stubDownloader{content: []byte("<html></html>")},
		ContentFilter: func(*crawler.Content) error { return nil },
		Parser: &stubParser{content: &crawler.Content{
			URLs:           []string{boardListing, boardPosting, otherHost, boardChrome},
			MainRegionURLs: []string{boardListing},
		}},
		URLFilter:          func(string) error { return nil },
		RobotsTxtChecker:   stubRobots{},
		RelevanceFilter:    func(*crawler.Content) error { return errors.New("not a listing") },
		OnJobListing:       func(context.Context, *crawler.RawJobListing) error { return nil },
		CareerSurfaceLinks: true,
		CareerPath:         careerPath(),
		CareerHost:         careerHost(),
	})

	page, err := crawler.NewURL(boardPage)
	if err != nil {
		t.Fatalf("NewURL: %v", err)
	}
	if err := worker.Process(t.Context(), &page); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}

	got := make([]string, len(fr.added))
	for i, u := range fr.added {
		got[i] = u.RawURL
	}
	// The board's own imprint is dropped even though its host is a career subdomain;
	// a career subdomain on ANOTHER host is still evidence and still followed.
	want := []string{
		"https://jobs.acme.com/openings/senior-go-engineer",
		"https://jobs.acme.com/jobs/4711",
		otherHost,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("enqueued URLs = %v, want %v", got, want)
	}
}
