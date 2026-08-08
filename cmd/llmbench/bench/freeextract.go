// This file is the bench package's Free Extraction fidelity scorer (ADR-0042 /
// ADR-0043, #256): the parallel of ScoreExtract for the one path that saves a Job
// Listing with NO model veto anywhere in the loop. It scores two failure modes the
// live metrics cannot see, because a wrong listing looks exactly like a right one:
// a Free Extraction firing on a page that is not a posting, and one firing
// correctly but mis-reading a field.
//
// PURE -- no parser, network, model, or decorator: cmd/llmbench drives the real
// freeextraction.Extractor over the Extract Gold Set to PRODUCE the rows, and this
// package folds them.
package bench

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
)

// FreeExtractionFields are the judgment fields a Free Extraction produces for one
// page (ADR-0042): what the page's own structured data says the posting is. They
// are compared by EXACT string equality, so an expected value is the page's
// declared text after the same entity-decoding and whitespace collapse the domain
// applies -- never a paraphrase.
type FreeExtractionFields struct {
	Title           string `json:"title"`
	Location        string `json:"location"`
	WorkArrangement string `json:"work_arrangement"`
}

// FreeExtractionRow is one Extract Gold Set page's Free Extraction outcome and the
// SOLE input to ScoreFreeExtraction.
type FreeExtractionRow struct {
	URL    string
	Label  ExtractLabel // gold
	Weight float64      // the row's sampling weight (ADR-0043), for the stream-weighted shares

	// Free reports that the Free Extraction served this page. It is OBSERVED FROM
	// THE MODEL STUB -- the stub was never called -- never read off the extraction's
	// own Free marker, so the measurement never rests on the thing it measures.
	Free bool
	// Got is what the Free Extraction produced. Meaningful only when Free.
	Got FreeExtractionFields
	// Want is the row's expected extraction; nil when the row carries none. A fired
	// row with no expectation is fatal: without that, deleting an expected block
	// would silently disarm the field guard.
	Want *FreeExtractionFields

	// AcceptedFire is a per-row, human-owned acceptance of a Free Extraction on a
	// page that is not a posting -- an argued exception carried as data on the row
	// (ADR-0043), never a softened threshold or a code-side allowlist. It is
	// honoured for ExtractResidue ONLY: a hub-index is the exact shape ADR-0042's
	// predicate rejects structurally, so excusing one would mean the predicate
	// itself is broken.
	AcceptedFire bool
}

// FreeFieldDivergence names one field of one page the Free Extraction read
// differently from its expected value. Fatal: it is a wrong Job Listing saved with
// no model veto, and nothing downstream can tell it from a right one.
type FreeFieldDivergence struct {
	URL string `json:"url"`
	// Field is "title", "location" or "work_arrangement".
	Field string `json:"field"`
	Want  string `json:"want"`
	Got   string `json:"got"`
}

// FreeExtractionScorecard is the deterministic Free Extraction fidelity report.
// The four list fields are the fatal signals; every count and rate is descriptive,
// matching ADR-0020's split between a hard structural guard and soft,
// composition-dependent measurements.
type FreeExtractionScorecard struct {
	Total int `json:"total"`
	Free  int `json:"free"` // rows the Free Extraction served
	// FreeRate is Free/Total -- SOFT, no threshold: it measures the gold set's
	// composition as much as the mechanism.
	FreeRate float64 `json:"free_rate"`
	// StreamFreeShare is the sampling-weighted share of the scored stream served
	// free -- SOFT. It is the number that describes the live stream rather than the
	// file, because the file oversamples the mechanism's own stratum ~54x.
	StreamFreeShare float64 `json:"stream_free_share"`
	// DetailTotal / DetailFree count the detail-labelled rows and how many of them
	// the Free Extraction served.
	DetailTotal int `json:"detail_total"`
	DetailFree  int `json:"detail_free"`
	// Coverage is DetailFree/DetailTotal -- SOFT: the share of real postings served
	// without a model. WeightedCoverage is the same, sampling-weighted.
	Coverage         float64 `json:"coverage"`
	WeightedCoverage float64 `json:"weighted_coverage"`

	// FiredOnNonPosting are URLs of hub-index or residue rows a Free Extraction
	// saved with no model in the loop, minus the per-row accepted exceptions. FATAL.
	FiredOnNonPosting []string `json:"fired_on_non_posting"`
	// AcceptedFires are URLs of residue rows carrying an explicit, human-owned
	// acceptance. Descriptive: excused, still printed and counted, because an
	// exception nobody sees is an exception nobody can withdraw.
	AcceptedFires []string `json:"accepted_fires"`
	// MissingExpectation are URLs the Free Extraction served that carry no expected
	// extraction. FATAL: an unexpected firing is unscored, so deleting an expected
	// block would silently disarm the field guard.
	MissingExpectation []string `json:"missing_expectation"`
	// UnusedExpectation are URLs carrying an expected extraction the Free Extraction
	// did not fire on. FATAL: a mechanism that stopped firing entirely would
	// otherwise produce a clean scorecard.
	UnusedExpectation []string `json:"unused_expectation"`
	// FieldDivergences name every field read differently from its expected value,
	// one entry per field, so a page can contribute up to three. FATAL.
	FieldDivergences []FreeFieldDivergence `json:"field_divergences"`
}

// FreeExtractionReport is the full free-extraction-mode output.
type FreeExtractionReport struct {
	Free FreeExtractionScorecard `json:"free_extraction"`
}

// Failed reports whether the fidelity check must exit non-zero: a fire on a
// non-posting that is not explicitly accepted, an unscored or unused expectation,
// or any field divergence. Every rate and count is descriptive and never moves it.
func (r FreeExtractionReport) Failed() bool {
	return len(r.Free.FiredOnNonPosting) > 0 ||
		len(r.Free.MissingExpectation) > 0 ||
		len(r.Free.UnusedExpectation) > 0 ||
		len(r.Free.FieldDivergences) > 0
}

// ScoreFreeExtraction folds Free Extraction rows into a FreeExtractionReport.
// PURE -- no parser, network, model, or decorator. Slices are initialized non-nil
// and preserve input order, so the JSON round-trips and tests can use
// reflect.DeepEqual.
func ScoreFreeExtraction(rows []FreeExtractionRow) FreeExtractionReport {
	sc := FreeExtractionScorecard{
		FiredOnNonPosting:  []string{},
		AcceptedFires:      []string{},
		MissingExpectation: []string{},
		UnusedExpectation:  []string{},
		FieldDivergences:   []FreeFieldDivergence{},
	}

	weightTotal, weightFree, weightDetail, weightDetailFree := 0.0, 0.0, 0.0, 0.0
	for _, row := range rows {
		sc.Total++
		weightTotal += row.Weight
		if row.Free {
			sc.Free++
			weightFree += row.Weight
		}
		if row.Label == ExtractDetail {
			sc.DetailTotal++
			weightDetail += row.Weight
			if row.Free {
				sc.DetailFree++
				weightDetailFree += row.Weight
			}
		}

		if row.Free && !row.Label.Positive() {
			if row.AcceptedFire && row.Label == ExtractResidue {
				sc.AcceptedFires = append(sc.AcceptedFires, row.URL)
			} else {
				sc.FiredOnNonPosting = append(sc.FiredOnNonPosting, row.URL)
			}
		}
		switch {
		case row.Free && row.Want == nil:
			sc.MissingExpectation = append(sc.MissingExpectation, row.URL)
		case !row.Free && row.Want != nil:
			sc.UnusedExpectation = append(sc.UnusedExpectation, row.URL)
		case row.Free && row.Want != nil:
			sc.FieldDivergences = append(sc.FieldDivergences, divergences(row.URL, *row.Want, row.Got)...)
		}
	}

	sc.FreeRate = ratio(sc.Free, sc.Total)
	sc.StreamFreeShare = weightedRatio(weightFree, weightTotal)
	sc.Coverage = ratio(sc.DetailFree, sc.DetailTotal)
	sc.WeightedCoverage = weightedRatio(weightDetailFree, weightDetail)

	return FreeExtractionReport{Free: sc}
}

// divergences compares the three judgment fields by EXACT string equality and
// returns one entry per differing field, in a fixed order. An expected empty
// Location against a produced one is a divergence like any other: a page that
// publishes no address must expect "", so an invented location is caught.
func divergences(url string, want, got FreeExtractionFields) []FreeFieldDivergence {
	out := []FreeFieldDivergence{}
	for _, f := range []struct{ field, want, got string }{
		{"title", want.Title, got.Title},
		{"location", want.Location, got.Location},
		{"work_arrangement", want.WorkArrangement, got.WorkArrangement},
	} {
		if f.want != f.got {
			out = append(out, FreeFieldDivergence{URL: url, Field: f.field, Want: f.want, Got: f.got})
		}
	}
	return out
}

// weightedRatio is n/d rounded to four decimals, or 0 when d is 0 -- the
// float-weighted counterpart of ratio, rounding identically so a weighted and an
// unweighted share read at the same precision.
func weightedRatio(n, d float64) float64 {
	if d == 0 {
		return 0
	}
	return math.Round(n/d*10000) / 10000
}

// EncodeFreeExtractionReport writes r as indented JSON plus a trailing newline,
// matching EncodeExtractReport's serialization style. It is the score-free verb's
// -json surface.
func EncodeFreeExtractionReport(w io.Writer, r FreeExtractionReport) error {
	out, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("bench: encode free extraction report: %w", err)
	}
	out = append(out, '\n')
	if _, err := w.Write(out); err != nil {
		return fmt.Errorf("bench: write free extraction report: %w", err)
	}
	return nil
}
