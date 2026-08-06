package crawler_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestSourceHash pins the extraction-cache key (ADR-0035) to literal values, not
// just to self-consistency: the hash is PERSISTED in job_listing.source_hash and the
// refetch lane compares it every Cycle, so a changed value here would silently
// re-extract the whole Corpus.
func TestSourceHash(t *testing.T) {
	t.Run("golden vector", func(t *testing.T) {
		assertStrings(t,
			"060129a269483ebdecde720932e2295c346aa76e91a047d5b82eae108af3567e",
			crawler.SourceHash("some page text", 8000))
	})

	t.Run("empty input hashes the empty string", func(t *testing.T) {
		assertStrings(t,
			"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			crawler.SourceHash("", 8000))
	})

	t.Run("cap is applied before hashing", func(t *testing.T) {
		// sha256("aaaaa") — the first 5 runes of a 10-rune input.
		assertStrings(t,
			"ed968e840d10d2d313a870bc131a4e2c311d7ad09bdf32b3418147221f51a6e2",
			crawler.SourceHash(strings.Repeat("a", 10), 5))
	})

	t.Run("content past the cap is invisible to the key", func(t *testing.T) {
		base := strings.Repeat("a", 10)
		if crawler.SourceHash(base, 5) != crawler.SourceHash(base+"IGNORED", 5) {
			t.Error("SourceHash must hash only the first maxChars runes")
		}
	})

	t.Run("cap counts runes, not bytes", func(t *testing.T) {
		// A byte cut at 2 would split "ä" and hash a different string.
		sum := sha256.Sum256([]byte("äö"))
		assertStrings(t, hex.EncodeToString(sum[:]), crawler.SourceHash("äöüß", 2))
	})
}
