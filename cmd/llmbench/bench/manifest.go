package bench

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
)

// ErrInvalidManifest wraps every structural manifest failure (malformed JSON,
// missing fields, unknown label/category, polarity mismatch, duplicate file).
var ErrInvalidManifest = errors.New("bench: invalid manifest")

// Entry is one Gold-Set fixture: raw HTML on disk (File, under pages/) plus its
// real URL and human-owned ground truth. The pipeline classifies at URL, so the
// stored HTML and the URL must be the same page.
type Entry struct {
	File      string   `json:"file"`
	URL       string   `json:"url"`
	Label     Label    `json:"label"`
	Category  Category `json:"category"`
	Verified  bool     `json:"verified"`
	FetchedAt string   `json:"fetched_at"`
	Note      string   `json:"note"`
	// ProposedLabel/ProposedCategory hold the labeler model's raw proposal (see
	// `llmbench label`), kept separate from the committed, human-owned Label/Category
	// so the review queue can detect a hand-edit that disagrees with the labeler.
	// omitempty so old manifests and pre-label capture stubs round-trip unchanged.
	ProposedLabel    Label    `json:"proposed_label,omitempty"`
	ProposedCategory Category `json:"proposed_category,omitempty"`
}

// Manifest is the Gold-Set index: the ordered list of fixtures to score.
type Manifest struct {
	Entries []Entry
}

// LoadManifest reads and validates manifest.json (a bare JSON array of Entry)
// from fsys. It wraps ErrInvalidManifest on: malformed JSON; an empty File or
// URL; an unknown Label or Category; a Label/Category polarity mismatch
// (Label.Positive() != Category.Positive()); or a duplicate File.
func LoadManifest(fsys fs.FS) (Manifest, error) {
	data, err := fs.ReadFile(fsys, "manifest.json")
	if err != nil {
		return Manifest{}, fmt.Errorf("bench: read manifest.json: %w", err)
	}

	entries := []Entry{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}

	seen := map[string]struct{}{}
	for i, e := range entries {
		if e.File == "" {
			return Manifest{}, fmt.Errorf("%w: entry %d: empty file", ErrInvalidManifest, i)
		}
		if e.URL == "" {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): empty url", ErrInvalidManifest, i, e.File)
		}
		if !e.Label.Valid() {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): unknown label %q", ErrInvalidManifest, i, e.File, e.Label)
		}
		if !e.Category.Valid() {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): unknown category %q", ErrInvalidManifest, i, e.File, e.Category)
		}
		if e.Label.Positive() != e.Category.Positive() {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): label %q and category %q disagree on polarity", ErrInvalidManifest, i, e.File, e.Label, e.Category)
		}
		if e.ProposedCategory != "" && !e.ProposedCategory.Valid() {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): unknown proposed_category %q", ErrInvalidManifest, i, e.File, e.ProposedCategory)
		}
		if e.ProposedLabel != "" && !e.ProposedLabel.Valid() {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): unknown proposed_label %q", ErrInvalidManifest, i, e.File, e.ProposedLabel)
		}
		if e.ProposedLabel != "" && e.ProposedCategory != "" && e.ProposedLabel.Positive() != e.ProposedCategory.Positive() {
			return Manifest{}, fmt.Errorf("%w: entry %d (%s): proposed_label %q and proposed_category %q disagree on polarity", ErrInvalidManifest, i, e.File, e.ProposedLabel, e.ProposedCategory)
		}
		if _, dup := seen[e.File]; dup {
			return Manifest{}, fmt.Errorf("%w: duplicate file %q", ErrInvalidManifest, e.File)
		}
		seen[e.File] = struct{}{}
	}

	return Manifest{Entries: entries}, nil
}

// ReadHTML returns e's fixture bytes (pages/<File>) from fsys.
func (e Entry) ReadHTML(fsys fs.FS) ([]byte, error) {
	b, err := fs.ReadFile(fsys, "pages/"+e.File)
	if err != nil {
		return nil, fmt.Errorf("bench: read fixture %q: %w", e.File, err)
	}
	return b, nil
}
