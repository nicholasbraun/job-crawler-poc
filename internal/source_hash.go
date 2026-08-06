package crawler

import (
	"crypto/sha256"
	"encoding/hex"
)

// SourceHash returns the extraction-cache key for a page's main content
// (ADR-0035): the SHA-256, hex-encoded, of that content capped to maxChars runes.
// The crawl-lane refetch pass compares a freshly-fetched page's key against the
// stored one to confirm a listing alive with NO model call, so both the extractor
// (which hashes the exact capped text it sent the model) and the refetch lane
// resolve to this one definition rather than to an invariant held by a comment.
// Callers pass the extractor's prompt-window cap; the stored Posting Body is
// bounded by DefaultDescriptionMaxChars instead — different knobs on purpose.
//
// Its value is PERSISTED (job_listing.source_hash) and must never change: a
// different value here silently re-extracts the whole Corpus.
func SourceHash(content string, maxChars int) string {
	sum := sha256.Sum256([]byte(capChars(content, maxChars)))
	return hex.EncodeToString(sum[:])
}
