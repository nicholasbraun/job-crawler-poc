package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestLonePosting covers the shared structured-posting read (ADR-0042) directly: it is
// a pure function, and now the single definition the Extract Gate, the Posting Body
// derivation and the Free Extraction all depend on, so the schema-traversal variations
// are pinned here once instead of at each caller.
func TestLonePosting(t *testing.T) {
	tests := []struct {
		name    string
		content *crawler.Content
		want    crawler.StructuredPosting
		wantOK  bool
	}{
		// --- Page shape and traversal ---
		{
			name: "flat lone JobPosting yields every field",
			content: &crawler.Content{JSONLD: []string{`{"@context":"https://schema.org","@type":"JobPosting",
				"title":"Backend Engineer","description":"We are hiring.",
				"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress","addressLocality":"Berlin","addressCountry":"DE"}}}`}},
			want: crawler.StructuredPosting{
				Title:           "Backend Engineer",
				Description:     "We are hiring.",
				Location:        "Berlin, DE",
				Country:         "DE",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			// The common WordPress/Yoast shape: everything under one @graph container.
			name: "@graph-nested lone posting beside an Organization is found",
			content: &crawler.Content{JSONLD: []string{`{"@context":"https://schema.org","@graph":[
				{"@type":"Organization","name":"Acme"},
				{"@type":"JobPosting","title":"Engineer","description":"We are hiring."}]}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Description:     "We are hiring.",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			name:    "@type as an array is recognized",
			content: &crawler.Content{JSONLD: []string{`{"@type":["JobPosting"],"title":"Engineer"}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			name:    "@type as a full schema.org URL is recognized",
			content: &crawler.Content{JSONLD: []string{`{"@type":"https://schema.org/JobPosting","title":"Engineer"}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			// An ItemList wrapping even a SINGLE posting is the openings-index shape the
			// Extract Gate rejects, so it can never read as a lone posting.
			name: "ItemList wrapping one posting is an openings index, not a lone posting",
			content: &crawler.Content{JSONLD: []string{`{"@context":"https://schema.org","@type":"ItemList","itemListElement":[
				{"@type":"ListItem","position":1,"item":{"@type":"JobPosting","title":"Engineer","description":"Engineer body."}}]}`}},
			wantOK: false,
		},
		{
			name: "two postings in two blocks are ambiguous",
			content: &crawler.Content{JSONLD: []string{
				`{"@type":"JobPosting","title":"Engineer"}`,
				`{"@type":"JobPosting","title":"Designer"}`,
			}},
			wantOK: false,
		},
		{
			name: "two postings inside one array block are ambiguous",
			content: &crawler.Content{JSONLD: []string{`[
				{"@type":"JobPosting","title":"Engineer"},
				{"@type":"JobPosting","title":"Designer"}]`}},
			wantOK: false,
		},
		{
			// The ItemList must wrap a POSTING to count: a site-navigation list beside a
			// real posting is not an openings index.
			name: "ItemList of non-posting items leaves a lone posting lone",
			content: &crawler.Content{JSONLD: []string{
				`{"@type":"ItemList","itemListElement":[{"@type":"SiteNavigationElement","name":"About"}]}`,
				`{"@type":"JobPosting","title":"Engineer"}`,
			}},
			want:   crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK: true,
		},
		{
			name:    "a page with no structured data has no lone posting",
			content: &crawler.Content{},
			wantOK:  false,
		},
		{
			name:    "structured data without a posting has no lone posting",
			content: &crawler.Content{JSONLD: []string{`{"@type":"Organization","name":"Acme"}`}},
			wantOK:  false,
		},
		{
			// One broken script must not cost the read of the others (fail-safe).
			name: "unparseable block alongside a lone posting contributes nothing",
			content: &crawler.Content{JSONLD: []string{
				`{"@type":"JobPosting", NOT JSON`,
				`{"@type":"JobPosting","title":"Engineer"}`,
			}},
			want:   crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK: true,
		},
		{
			name:    "an unparseable block alone never fails the read",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting", NOT JSON`}},
			wantOK:  false,
		},

		// --- Location composition (ADR-0029: free text the Country Resolver reads) ---
		{
			// Redundant parts are deliberately NOT de-duplicated: ADR-0042 measured
			// "Berlin, Berlin, DE" against the real Resolver and it resolves correctly.
			name: "locality, region and country compose in order, redundancy kept",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer",
				"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress",
					"addressLocality":"Berlin","addressRegion":"Berlin","addressCountry":"DE"}}}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Berlin, Berlin, DE",
				Country:         "DE",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			name: "country as a nested named node is read",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer",
				"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress",
					"addressLocality":"Minato","addressRegion":"Tokyo","addressCountry":{"@type":"Country","name":"Japan"}}}}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Minato, Tokyo, Japan",
				Country:         "Japan",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			// Live pages ship single-element arrays for address parts; without the array
			// case each one would silently lose its location.
			name: "address parts given as one-element arrays compose normally",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer",
				"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress",
					"addressLocality":["Walldürn"],"addressCountry":["DE"]}}}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Walldürn, DE",
				Country:         "DE",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			// Country is carried separately because the composed Location cannot be
			// resolved safely on its own: the Country Resolver has no bare alpha-2
			// country keys but does key US state abbreviations, so "Perth, WA, AU"
			// resolves to US on the region and never reaches AU. Same shape for
			// "Almere, FL, NL" and "Milano, MI, IT".
			name: "a region colliding with a US state still declares its own country",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer",
				"jobLocation":{"@type":"Place","address":{"@type":"PostalAddress",
					"addressLocality":"Perth","addressRegion":"WA","addressCountry":"AU"}}}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Perth, WA, AU",
				Country:         "AU",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			// Joining all offices would hand the Resolver several countries at once and
			// read as noise wherever the listing is displayed.
			name: "jobLocation array takes the first office only",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","jobLocation":[
				{"@type":"Place","address":{"addressLocality":"Austin","addressRegion":"TX","addressCountry":"US"}},
				{"@type":"Place","address":{"addressLocality":"New York","addressRegion":"NY","addressCountry":"US"}}]}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Austin, TX, US",
				Country:         "US",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			name: "an empty first office falls through to the next one",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","jobLocation":[
				{"@type":"Place","address":{}},
				{"@type":"Place","address":{"addressLocality":"Hamburg","addressCountry":"DE"}}]}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Hamburg, DE",
				Country:         "DE",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			name: "an address published as a bare string is taken verbatim",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer",
				"jobLocation":{"@type":"Place","address":"Edmonton, AB"}}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "Edmonton, AB",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			name: "a country-only address composes to just the country",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer",
				"jobLocation":{"@type":"Place","address":{"addressCountry":"DE"}}}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Location:        "DE",
				Country:         "DE",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			name:    "a posting with no jobLocation has an empty Location",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},

		// --- Working mode (ADR-0030: never guessed) ---
		{
			name:    "TELECOMMUTE is the remote marker",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","jobLocationType":"TELECOMMUTE"}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementRemote},
			wantOK:  true,
		},
		{
			name:    "jobLocationType as an array is recognized",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","jobLocationType":["TELECOMMUTE"]}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementRemote},
			wantOK:  true,
		},
		{
			// "Hybrid" is not a schema.org value (1 of 1167 live nodes ships it). A mode
			// the schema does not define is never guessed.
			name:    "a non-schema jobLocationType stays Unspecified",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","jobLocationType":"Hybrid"}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			name:    "an absent jobLocationType is Unspecified, never Onsite",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			name:    "a null jobLocationType is Unspecified",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","jobLocationType":null}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},

		// --- Field reading ---
		{
			// 2.8% of live posting nodes ship an entity-encoded title.
			name:    "an entity-encoded title is decoded",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Office 365 &#038; Azure Engineer"}`}},
			want:    crawler.StructuredPosting{Title: "Office 365 & Azure Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			name:    "ragged whitespace in a title is collapsed and trimmed",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"  Backend\n\tEngineer   (m/w/d) "}`}},
			want:    crawler.StructuredPosting{Title: "Backend Engineer (m/w/d)", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			// A malformed field degrades to absent, and the page's SHAPE is still lone:
			// what a title-less posting is worth is the caller's call, not this read's.
			name:    "a non-string title reads as absent, and the page is still lone",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":42,"description":"We are hiring."}`}},
			want:    crawler.StructuredPosting{Description: "We are hiring.", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			// The seam the Free Extraction branches on: ok reports the shape, and an
			// empty Title is what sends a half-published page to the model.
			name:    "a posting with no title is still lone, with an empty Title",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","description":"We are hiring."}`}},
			want:    crawler.StructuredPosting{Description: "We are hiring.", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
		{
			// Description is returned RAW: the strip/unescape/strip reduction and the
			// rune cap belong to PostingBody (ADR-0041), which owns their ordering.
			name:    "the description keeps its markup, unlike every other field",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","description":"<p>Build <b>things</b> &amp; ship.</p>"}`}},
			want: crawler.StructuredPosting{
				Title:           "Engineer",
				Description:     "<p>Build <b>things</b> &amp; ship.</p>",
				WorkArrangement: crawler.WorkArrangementUnspecified,
			},
			wantOK: true,
		},
		{
			// Guards PostingBody's fallback: an empty Description sends it to main content.
			name:    "a non-string description reads as absent",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer","description":42}`}},
			want:    crawler.StructuredPosting{Title: "Engineer", WorkArrangement: crawler.WorkArrangementUnspecified},
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := crawler.LonePosting(tt.content)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("posting = %+v, want %+v", got, tt.want)
			}
		})
	}
}

// TestWithdrawalNotice covers the guard that keeps a filled or expired posting out of
// the Corpus (ADR-0042). Sites produce two shapes of it -- a body replaced by a
// one-line message, and the whole posting left in place under a banner -- and both
// have to be recognized, since a page serving either is otherwise indistinguishable
// from a live posting.
func TestWithdrawalNotice(t *testing.T) {
	const declared = "We are hiring a Go engineer to work on our crawler. You will design and build " +
		"distributed systems, own services end to end, and mentor other engineers. We offer a " +
		"competitive salary, a learning budget, and a hybrid working model out of our Berlin office."

	tests := []struct {
		name        string
		description string
		mainContent string
		want        bool
	}{
		{
			name:        "a page rendering the posting it declares is not a withdrawal notice",
			description: declared,
			mainContent: "Backend Engineer " + declared + " Apply now.",
			want:        false,
		},
		{
			// Measured: real postings reach a declared/rendered ratio of 1.49 without
			// being withdrawn, so a body merely shorter than the posting must pass.
			name:        "a body somewhat shorter than the posting is not a withdrawal notice",
			description: declared,
			mainContent: declared[:len(declared)*2/3],
			want:        false,
		},
		{
			// Shape one: the body is replaced by a message, so the page declares far
			// more than it renders.
			name:        "a one-line message under a full declared posting",
			description: declared,
			mainContent: "Backend Engineer This position is no longer active.",
			want:        true,
		},
		{
			// Shape two: the posting is all still there and only the banner says it is
			// gone, so no length comparison can see it. Quoted from the pages it is
			// drawn from -- their wording, not the domain's (CONTEXT.md, Job Listing).
			name:        "a banner above an otherwise complete posting",
			description: declared,
			mainContent: "Data Operations Specialist UVeye This job is no longer accepting applications " +
				"See open jobs at UVeye. " + declared,
			want: true,
		},
		{
			// The phrases carry literal spaces, so a banner broken across the lines a
			// Structural Rendering introduces would go unseen if the guard read the
			// content field raw. It reads the page's Flattened Text instead (ADR-0046),
			// and a missed banner puts a job that does not exist into the Corpus with no
			// model in the loop and no way back out.
			name:        "a banner broken across a rendering's line structure",
			description: declared,
			mainContent: "Data Operations Specialist\n\nThis job is no longer\naccepting applications\n\n" + declared,
			want:        true,
		},
		{
			name:        "a removal banner naming the time it happened",
			description: declared,
			mainContent: "Product Training Specialist Sorry, this job was removed at 04:14 p.m. (MST) on Saturday. " + declared,
			want:        true,
		},
		{
			name:        "a banner in another language",
			description: declared,
			mainContent: "HR Manager L'offre n'est plus disponible. " + declared,
			want:        true,
		},
		{
			// The phrases are whole sentences, never tokens, because the same capture
			// holds a "don't show again" control and prose about internally filled
			// roles that a token would refuse.
			name:        "a dismiss control is not a closure banner",
			description: declared,
			mainContent: "Backend Engineer " + declared + " Nicht mehr anzeigen Bewerben Job merken",
			want:        false,
		},
		{
			name:        "prose about roles filled internally is not a closure banner",
			description: declared,
			mainContent: "Store Manager " + declared + " En 2025, 55% des postes de managers en magasins ont été pourvus en interne.",
			want:        false,
		},
		{
			// Absence of a description is not evidence of withdrawal, so such a page is
			// judged by its banner alone.
			name:        "a posting declaring no description is judged by its banner alone",
			description: "",
			mainContent: "Backend Engineer Apply now.",
			want:        false,
		},
		{
			name:        "a posting declaring no description still shows its banner",
			description: "",
			mainContent: "Backend Engineer This job is no longer accepting applications.",
			want:        true,
		},
		{
			// HTML is reduced before measuring, or markup would inflate the posting's
			// length and refuse live pages the parser already stripped.
			name:        "markup in the declared posting is not counted as length",
			description: "<div class=\"x\"><p>" + declared + "</p></div>",
			mainContent: declared,
			want:        false,
		},
		{
			name:        "a page rendering nothing under a declared posting",
			description: declared,
			mainContent: "",
			want:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := &crawler.Content{MainContent: tt.mainContent}
			posting := crawler.StructuredPosting{Title: "Backend Engineer", Description: tt.description}
			if got := crawler.WithdrawalNotice(content, posting); got != tt.want {
				t.Errorf("WithdrawalNotice = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLonePostingNilContent pins the nil-page contract: no lone posting and the zero
// value, never a panic — a caller that ignores ok reads empty fields, not a half-page.
func TestLonePostingNilContent(t *testing.T) {
	got, ok := crawler.LonePosting(nil)
	if ok {
		t.Fatalf("ok = true, want false for a nil page")
	}
	if got != (crawler.StructuredPosting{}) {
		t.Errorf("posting = %+v, want the zero value", got)
	}
}

// TestHasOpeningsIndex covers the companion report over the same scan: the page shapes
// that mark a hub to crawl rather than a posting to extract.
func TestHasOpeningsIndex(t *testing.T) {
	tests := []struct {
		name    string
		content *crawler.Content
		want    bool
	}{
		{
			name: "two standalone posting nodes are an openings index",
			content: &crawler.Content{JSONLD: []string{
				`{"@type":"JobPosting","title":"Engineer"}`,
				`{"@type":"JobPosting","title":"Designer"}`,
			}},
			want: true,
		},
		{
			name: "an ItemList wrapping even one posting is an openings index",
			content: &crawler.Content{JSONLD: []string{`{"@type":"ItemList","itemListElement":[
				{"@type":"ListItem","position":1,"item":{"@type":"JobPosting","title":"Engineer"}}]}`}},
			want: true,
		},
		{
			name: "two postings nested in an @graph are counted",
			content: &crawler.Content{JSONLD: []string{`{"@graph":[
				{"@type":"JobPosting","title":"Engineer"},
				{"@type":"JobPosting","title":"Designer"}]}`}},
			want: true,
		},
		{
			name:    "an ItemList of non-posting items is not an openings index",
			content: &crawler.Content{JSONLD: []string{`{"@type":"ItemList","itemListElement":[{"@type":"SiteNavigationElement","name":"About"}]}`}},
			want:    false,
		},
		{
			// The signal only ever ADDS credit on the Discovery Gate, so a lone posting
			// must earn none: otherwise a single posting could become a False-Certain.
			name:    "a lone posting is one Job Listing, not a hub",
			content: &crawler.Content{JSONLD: []string{`{"@type":"JobPosting","title":"Engineer"}`}},
			want:    false,
		},
		{
			name:    "a page with no structured data fires nothing",
			content: &crawler.Content{},
			want:    false,
		},
		{
			name:    "an unparseable block fires nothing (fail-safe)",
			content: &crawler.Content{JSONLD: []string{`{"@type":"ItemList", NOT JSON`}},
			want:    false,
		},
		{
			name:    "a nil page fires nothing",
			content: nil,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := crawler.HasOpeningsIndex(tt.content); got != tt.want {
				t.Errorf("HasOpeningsIndex = %v, want %v", got, tt.want)
			}
		})
	}
}
