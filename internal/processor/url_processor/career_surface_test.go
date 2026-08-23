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

// careerShaped is rule 2's predicate built exactly as cmd/server builds it: out of
// the crawl definition's own pass vocabulary, not a second, looser one.
func careerShaped() func(string) bool {
	passSubdomains := urlfilter.PassSubdomains("jobs", "karriere")
	passPathSegments := urlfilter.PassPathSegments("jobs", "karriere")
	return func(u string) bool {
		return errors.Is(passSubdomains(u), filter.ErrPass) ||
			errors.Is(passPathSegments(u), filter.ErrPass)
	}
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
				CareerShaped:       careerShaped(),
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
		CareerShaped:       careerShaped(),
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
		CareerShaped:       careerShaped(),
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
