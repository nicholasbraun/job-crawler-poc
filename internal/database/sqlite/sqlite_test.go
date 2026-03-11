package sqlite_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/sqlite"
	_ "modernc.org/sqlite"
)

func TestSQLite(t *testing.T) {
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
	err = urlRepository.Save(t.Context(), testURL)
	if err != nil {
		t.Fatalf("error saving url: %v", err)
	}

	err = urlRepository.Save(t.Context(), testURL)
	if err != nil {
		t.Fatalf("error saving same url twice: %v", err)
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
