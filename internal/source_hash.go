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
//
// Callers therefore pass FLATTENED TEXT, never a Structural Rendering (ADR-0046): a
// rendering reaching this hash re-keys ~46k rows in one wave. The flatten stays at the
// two call sites — the save processor and the refetch lane — rather than being hidden
// in here, because both stub this function in their tests, and a derivation applied
// behind a stubbed seam is a production behaviour no test exercises.
func SourceHash(content string, maxChars int) string {
	sum := sha256.Sum256([]byte(capChars(content, maxChars)))
	return hex.EncodeToString(sum[:])
}
