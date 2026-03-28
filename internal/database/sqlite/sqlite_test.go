package sqlite_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/sqlite"
	_ "modernc.org/sqlite"
)

func TestSQLiteURLRepository(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("error creating in memory db: %v", err)
	}

	err = sqlite.Setup(t.Context(), db)
	if err != nil {
		t.Fatalf("error setting up tables: %v", err)
	}

	var urlRepository crawler.URLRepository = sqlite.NewURLRepository(db)

	testURL := "https://example.de"
	isNew, err := urlRepository.Save(t.Context(), testURL)
	if err != nil {
		t.Fatalf("error saving url: %v", err)
	}
	if !isNew {
		t.Error("first save should report url as new")
	}

	isNew, err = urlRepository.Save(t.Context(), testURL)
	if err != nil {
		t.Fatalf("error saving same url twice: %v", err)
	}
	if isNew {
		t.Error("second save should report url as duplicate")
	}

	visited, err := urlRepository.Visited(t.Context(), testURL)
	if err != nil {
		t.Fatalf("error checking visited url: %v", err)
	}

	if !visited {
		t.Errorf("%s should be visited", testURL)
	}

	nonExistingURL := "non-existing url"
	visited, err = urlRepository.Visited(t.Context(), nonExistingURL)
	if err != nil {
		t.Fatalf("error checking visited url: %v", err)
	}

	if visited {
		t.Errorf("%s should not be visited", nonExistingURL)
	}
}

func TestSQLiteJobListingRepository(t *testing.T) {
	db, err := sqlite.Open(":memory:")
	if err != nil {
		t.Fatalf("error creating in memory db: %v", err)
	}

	err = sqlite.Setup(t.Context(), db)
	if err != nil {
		t.Fatalf("error setting up tables: %v", err)
	}

	var jobListingRepository crawler.JobListingRepository = sqlite.NewJobListingRepository(db)

	jobListing := &crawler.JobListing{
		URL:       "https://netflix.com/jobs/123", // yeah right, lol
		Title:     "Senior Software Engineer",
		Company:   "netflix",
		Location:  "Germany/remote",
		TechStack: []string{"golang", "sqlite"},
	}

	err = jobListingRepository.Save(t.Context(), jobListing)
	if err != nil {
		t.Fatalf("error saving job listing: %v", err)
	}

	jobListings, err := jobListingRepository.Find(t.Context())
	if err != nil {
		t.Fatalf("error finding job listings: %v", err)
	}

	if len(jobListings) != 1 {
		t.Fatalf("should have found one job listing")
	}

	wantURL := jobListing.URL
	gotURL := jobListings[0].URL
	if wantURL != gotURL {
		t.Errorf("want url: %s, got: %s", wantURL, gotURL)
	}

	if len(jobListings[0].TechStack) != 2 {
		t.Fatalf("should have found two tech stack items, found: %v", jobListings[0].TechStack)
	}
	wantTechStack1 := jobListing.TechStack[0]
	gotTechStack1 := jobListings[0].TechStack[0]
	if wantTechStack1 != gotTechStack1 {
		t.Errorf("want tech stack item: %s, got: %s", wantTechStack1, gotTechStack1)
	}
}
