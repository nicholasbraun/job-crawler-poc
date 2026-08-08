// This file is the bench package's Extract Gate scoring layer (ADR-0020, #114):
// the parallel, additive counterpart to the classifier scorer in scorer.go. It
// scores the Extract Gate's binary extract-vs-skip decision over pages labelled
// detail / hub-index / residue, with a false-drop hard guard (a real
// single-posting detail the gate skips fails the run). Both extract-gate row
// sources feed it: the synthetic reject-rung fixtures under
// ../extract-testdata (the extract verb) and the real captured pages of the
// Extract Gold Set (the score-capture verb, ADR-0043). It
// reuses the pure helpers ClassScore/scoreClass/confusion/ratio and the
// ErrInvalidManifest sentinel unchanged; it adds no LLM, network, or parser work
// of its own -- cmd/llmbench's extract verb drives the real parser -> ShouldExtract
// replay to PRODUCE the rows, and ScoreExtract folds them.
package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
)

// ExtractLabel is the human-owned ground truth for one extract-gate row -- a
// reject-rung regression fixture or an Extract Gold Set page.
// Scoring collapses it to a binary extract-vs-skip decision via Positive():
// detail is the positive (extract) class; hub-index and residue both collapse to
// the negative (skip) class. ExtractAmbiguous is the one label that collapses to
// NEITHER: see Scored().
type ExtractLabel string

const (
	// ExtractDetail is a single job-posting page (an ATS posting, or a self-hosted
	// posting under a job-segment path). The gate MUST extract it; a detail the
	// gate skips is a false-drop and fails the run.
	ExtractDetail ExtractLabel = "detail"
	// ExtractHubIndex is a careers hub or openings index (an ATS board root, a bare
	// /careers, or a self-hosted "open roles" list). It should skip: the crawler
	// harvests its individual postings rather than extract the index itself.
	ExtractHubIndex ExtractLabel = "hub-index"
	// ExtractResidue is a structurally-silent non-posting that trips a career
	// keyword (a career-landing, "work with us", culture, or about page). It should
	// skip. It is the population the deferred L2 content confirm (ADR-0020) is
	// measured against.
	ExtractResidue ExtractLabel = "residue"
	// ExtractAmbiguous is a page whose correct class a review could not settle. It
	// is NOT a fourth class the gate is scored against: it is EXCLUDED from every
	// confusion count, so an undecidable page can never silently become a false-drop
	// nor a non-posting the gate wrongly extracted. It exists because forcing such a
	// page into detail or residue would fabricate exactly the evidence the Boundary
	// Stratum is meant to supply honestly (ADR-0043).
	ExtractAmbiguous ExtractLabel = "ambiguous"
)

// Valid reports whether l is one of the known extract labels, ExtractAmbiguous
// included: a row a review could not settle is labelled, not unlabelled, and the
// tooling that reads a gold set must carry it rather than skip it.
func (l ExtractLabel) Valid() bool {
	return l == ExtractDetail || l == ExtractHubIndex || l == ExtractResidue || l == ExtractAmbiguous
}

// Scored reports whether l takes part in the confusion counts. Everything valid
// does except ExtractAmbiguous, which is reported and never scored.
func (l ExtractLabel) Scored() bool { return l.Valid() && l != ExtractAmbiguous }

// Positive reports whether l is the positive (extract) class. It is the single
// source of the binary collapse: detail is positive, hub-index and residue both
// collapse to the negative (skip) class. Mirrors Label.Positive(). It is never
// consulted for ExtractAmbiguous, which Scored() excludes before the collapse.
func (l ExtractLabel) Positive() bool { return l == ExtractDetail }

// AllExtractLabels is the fixed print / slice order for per-class breakdowns. It
// is the three SCORED classes: ExtractAmbiguous is deliberately absent, because
// this slice also drives the "every label is exercised" assertion on the committed
// gold set, and no drawing can promise an undecidable row exists.
var AllExtractLabels = []ExtractLabel{ExtractDetail, ExtractHubIndex, ExtractResidue}

// ExtractEntry is one extract reject-rung regression fixture: raw HTML on disk
// (File, under pages/) plus its real URL and human-owned label. The gate decides
// at URL, so the stored HTML and the URL must be the same page. The Extract Gold
// Set does NOT use this shape -- it stores parsed Content, never re-fetchable
// HTML (ADR-0043).
type ExtractEntry struct {
	File  string       `json:"file"`
	URL   string       `json:"url"`
	Label ExtractLabel `json:"label"`
	// Verified marks a human-signed-off label. It is carried for parity with the
	// classifier set's ground-truth flag; #114 builds no extract review queue.
	Verified  bool   `json:"verified"`
	FetchedAt string `json:"fetched_at"`
	Note      string `json:"note"`
}

// ExtractManifest is the reject-rung fixture-set index: the ordered list of
// fixtures to score.
type ExtractManifest struct {
	Entries []ExtractEntry
}

// LoadExtractManifest reads and validates manifest.json (a bare JSON array of
// ExtractEntry) from fsys. It wraps ErrInvalidManifest on: malformed JSON; an
// empty File or URL; an unknown Label; or a duplicate File. Unlike LoadManifest
// it has no category/polarity/whitelist checks -- the extract set is
// single-field-labelled.
func LoadExtractManifest(fsys fs.FS) (ExtractManifest, error) {
	data, err := fs.ReadFile(fsys, "manifest.json")
	if err != nil {
		return ExtractManifest{}, fmt.Errorf("bench: read manifest.json: %w", err)
	}

	entries := []ExtractEntry{}
	if err := json.Unmarshal(data, &entries); err != nil {
		return ExtractManifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}

	seen := map[string]struct{}{}
	for i, e := range entries {
		if e.File == "" {
			return ExtractManifest{}, fmt.Errorf("%w: entry %d: empty file", ErrInvalidManifest, i)
		}
		if e.URL == "" {
			return ExtractManifest{}, fmt.Errorf("%w: entry %d (%s): empty url", ErrInvalidManifest, i, e.File)
		}
		if !e.Label.Valid() {
			return ExtractManifest{}, fmt.Errorf("%w: entry %d (%s): unknown label %q", ErrInvalidManifest, i, e.File, e.Label)
		}
		if _, dup := seen[e.File]; dup {
			return ExtractManifest{}, fmt.Errorf("%w: duplicate file %q", ErrInvalidManifest, e.File)
		}
		seen[e.File] = struct{}{}
	}

	return ExtractManifest{Entries: entries}, nil
}

// ReadHTML returns e's fixture bytes (pages/<File>) from fsys. Mirrors
// Entry.ReadHTML.
func (e ExtractEntry) ReadHTML(fsys fs.FS) ([]byte, error) {
	b, err := fs.ReadFile(fsys, "pages/"+e.File)
	if err != nil {
		return nil, fmt.Errorf("bench: read fixture %q: %w", e.File, err)
	}
	return b, nil
}

// ExtractVerdictRow is one fixture's Extract Gate outcome and the SOLE input to
// ScoreExtract. Extract is pagegate.ShouldExtract's binary decision (true =
// extract, false = skip).
type ExtractVerdictRow struct {
	URL     string
	Label   ExtractLabel // gold
	Extract bool         // predicted
	// Weight is the row's ADR-0043 sampling weight: what one row of its sampling
	// cell is worth in the live extract stream. It is ZERO for a row drawn outside a
	// weighted stratum -- a synthetic reject-rung fixture, or a raw unsampled capture
	// line -- which is why the stream scorecard is computed over an explicitly
	// selected subset rather than over every row a caller happens to hold.
	Weight float64
}

// ExtractScorecard is the deterministic Extract Gate regression report. The
// false-drop list is the only fatal signal; every other field is descriptive.
type ExtractScorecard struct {
	Total           int     `json:"total"`
	ExtractCalls    int     `json:"extract_calls"`     // rows with Extract==true
	ExtractCallRate float64 `json:"extract_call_rate"` // ratio(ExtractCalls, Total) -- SOFT, no threshold
	// FalseDrops are URLs of detail-labelled fixtures the gate skipped: a real
	// single posting the extractor never sees. This is the sole fatal signal.
	FalseDrops []string `json:"false_drops"`
	// Leaks are URLs of non-posting fixtures (hub-index or residue) the gate
	// extracted. Descriptive: it documents where today's gate leaks, never fails.
	Leaks []string `json:"leaks"`
	// Overall is the binary extract-vs-skip confusion (positive = extract).
	Overall ClassScore `json:"overall"`
	// ByClass slices the confusion per label (detail / hub-index / residue). Each
	// label is single-polarity, so detail's Recall is the fraction extracted (the
	// recall-safety number) and hub-index/residue's Accuracy is the fraction shed.
	ByClass map[ExtractLabel]ClassScore `json:"by_class"`
	// ResidueCount is the number of residue fixtures; ResidueExtracted the number
	// that leaked to the extractor (the ADR-0020 L2 data-gate signal).
	ResidueCount     int `json:"residue_count"`
	ResidueExtracted int `json:"residue_extracted"`
	// Ambiguous is the rows a review could not classify; AmbiguousExtracted how many
	// of them this config extracts. They are REPORTED and never scored: excluded from
	// the confusion, from the per-class slices, from FalseDrops and from Leaks. They
	// still count in Total and ExtractCalls, because a call is paid for whether or
	// not anybody could say what the page was.
	Ambiguous          int `json:"ambiguous"`
	AmbiguousExtracted int `json:"ambiguous_extracted"`
}

// ExtractReport is the full extract-mode output.
type ExtractReport struct {
	Extract ExtractScorecard `json:"extract"`
	// Stream carries the sampling-weighted estimates of the live extract stream,
	// present only when the scored rows include a weighted random stratum (ADR-0043,
	// #262). Nil for the synthetic fixture set, which describes no stream.
	Stream *StreamScorecard `json:"stream,omitempty"`
	// Boundary carries the Boundary Stratum's own unweighted view (ADR-0043, #263),
	// present only when the scored rows include one. Nil for the synthetic fixture
	// set and for a raw capture, neither of which draws a boundary.
	Boundary *BoundaryScorecard `json:"boundary,omitempty"`
}

// Failed reports whether the extract run must exit non-zero: any false-drop (a
// real detail page the gate rejected). Leaks and the extract-call rate are
// descriptive and never move this -- and neither does Stream: every figure in it is
// a weighted estimate with no threshold (ADR-0020's split between a hard structural
// guard and soft, composition-dependent measurements).
func (r ExtractReport) Failed() bool { return len(r.Extract.FalseDrops) > 0 }

// ScoreExtract folds extract verdict rows into an ExtractReport. PURE -- no
// parser, network, or LLM. The positive class is "extract"; gold-positive is
// row.Label.Positive() (== detail), predicted-positive is row.Extract. Because
// gold-positive is exactly detail, every overall false-negative IS a false-drop.
// Slices are initialized non-nil and preserve input order, so the JSON round-trips
// and tests can use reflect.DeepEqual.
//
// A row whose label is not Scored() -- an ambiguous page -- is counted in Total and
// in ExtractCalls and then set aside: it enters no confusion cell, no per-class
// slice, no false-drop and no leak. Collapsing an undecidable page into a class
// would let the labeller's shrug decide whether the build goes red.
func ScoreExtract(rows []ExtractVerdictRow) ExtractReport {
	sc := ExtractScorecard{
		FalseDrops: []string{},
		Leaks:      []string{},
		ByClass:    map[ExtractLabel]ClassScore{},
	}

	overall := [4]int{}
	byLabel := map[ExtractLabel][4]int{}
	for _, row := range rows {
		sc.Total++
		gold := row.Label.Positive()
		if row.Extract {
			sc.ExtractCalls++
		}
		if !row.Label.Scored() {
			sc.Ambiguous++
			if row.Extract {
				sc.AmbiguousExtracted++
			}
			continue
		}
		if gold && !row.Extract {
			sc.FalseDrops = append(sc.FalseDrops, row.URL)
		}
		if !gold && row.Extract {
			sc.Leaks = append(sc.Leaks, row.URL)
		}
		if row.Label == ExtractResidue {
			sc.ResidueCount++
			if row.Extract {
				sc.ResidueExtracted++
			}
		}
		confusion(&overall, row.Extract, gold)
		m := byLabel[row.Label]
		confusion(&m, row.Extract, gold)
		byLabel[row.Label] = m
	}

	sc.ExtractCallRate = ratio(sc.ExtractCalls, sc.Total)
	sc.Overall = scoreClass(overall[0], overall[1], overall[2], overall[3])
	for label, m := range byLabel {
		sc.ByClass[label] = scoreClass(m[0], m[1], m[2], m[3])
	}

	return ExtractReport{Extract: sc}
}

// EncodeExtractReport writes r as indented JSON plus a trailing newline, matching
// EncodeReport's serialization style. It is the extract verb's -json surface.
func EncodeExtractReport(w io.Writer, r ExtractReport) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: encode extract report: %w", err)
	}
	out = append(out, '\n')
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("bench: write extract report: %w", err)
	}
	return nil
}
