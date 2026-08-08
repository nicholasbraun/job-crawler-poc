// This file is the bench package's BOUNDARY view of the Extract Gate (ADR-0043,
// #263): the same extract-vs-skip rows ScoreExtract folds, restricted to the pages
// where today's blanket accept and the tiered Positive Evidence rule DISAGREE.
//
// What population it describes matters as much as the arithmetic. The Boundary
// Stratum is a CENSUS of that disagreement's accept half, taken because a uniform
// sample of a stream that is overwhelmingly non-postings cannot detect a small
// false-drop rate at all. It is therefore a deliberately non-random population:
// nothing here estimates the stream, nothing here is weighted, and no figure here
// may be pooled with the Random Stratum's. Weighting a census of a disagreement set
// would invent a population it never sampled.
//
// PURE -- no parser, network, or model, mirroring extractstream.go's separation:
// cmd/llmbench replays the real gate to PRODUCE the rows, and this folds them.
package bench

// BoundaryRow is one Boundary Stratum row as the boundary scorecard reads it: the
// scored verdict row plus whether a human has actually confirmed its label. The
// verdict row is EMBEDDED rather than wrapped so every reader keeps reaching
// straight through to URL / Label / Extract.
type BoundaryRow struct {
	ExtractVerdictRow
	// Confirmed is true once a human has signed the label off. A false-drop count
	// over unconfirmed labels is one model's opinion of another model's opinion
	// (ADR-0043), so the scorecard reports the split rather than hiding it.
	Confirmed bool
}

// BoundaryScorecard is the Boundary Stratum's own view: what a gate config does to
// the pages where today's blanket accept and the tiered Positive Evidence rule
// disagree. UNWEIGHTED BY DESIGN (see the file comment).
type BoundaryScorecard struct {
	Rows int `json:"rows"`
	// Counts is the raw row count per label, ambiguous included, so nobody reads a
	// drop count without seeing what it was measured over.
	Counts map[ExtractLabel]int `json:"counts"`
	// Extracted and Skipped split the stratum by what THIS config decides.
	Extracted int `json:"extracted"`
	Skipped   int `json:"skipped"`
	// FalseDrops are the Job Listings this config drops here -- the number a
	// hard-zero guard turns on, listed so a reviewer never has to re-derive it.
	FalseDrops []string `json:"false_drops"`
	// AmbiguousSkipped is the dropped rows nobody could classify: not counted as a
	// false-drop, and not silently forgiven either.
	AmbiguousSkipped int `json:"ambiguous_skipped"`
	// Confirmed and Unconfirmed report how many of these rows a human has signed off.
	Confirmed   int `json:"confirmed"`
	Unconfirmed int `json:"unconfirmed"`
}

// ScoreExtractBoundary folds Boundary Stratum rows into a BoundaryScorecard. PURE
// -- no parser, network, or LLM. Counts is initialized for every scored label plus
// ambiguous, so an EMPTY cell is visible rather than absent, and FalseDrops is
// non-nil so the JSON round-trips.
func ScoreExtractBoundary(rows []BoundaryRow) BoundaryScorecard {
	sc := BoundaryScorecard{
		Counts:     map[ExtractLabel]int{},
		FalseDrops: []string{},
	}
	for _, l := range AllExtractLabels {
		sc.Counts[l] = 0
	}
	sc.Counts[ExtractAmbiguous] = 0

	for _, row := range rows {
		sc.Rows++
		sc.Counts[row.Label]++
		if row.Extract {
			sc.Extracted++
		} else {
			sc.Skipped++
		}
		if row.Confirmed {
			sc.Confirmed++
		} else {
			sc.Unconfirmed++
		}
		if row.Extract {
			continue
		}
		if !row.Label.Scored() {
			sc.AmbiguousSkipped++
			continue
		}
		if row.Label.Positive() {
			sc.FalseDrops = append(sc.FalseDrops, row.URL)
		}
	}
	return sc
}
