// This file is the bench package's STREAM-weighted view of the Extract Gate
// (ADR-0043, #262): the same extract-vs-skip rows ScoreExtract folds, re-read
// through each row's sampling weight so the numbers describe the live extract
// stream rather than the sample's own composition. It exists because a gold set is
// drawn under a design -- per-verdict caps, per-cell quotas -- and reading its raw
// mix as the stream's overstates whatever the design oversampled.
//
// What population it describes matters as much as the arithmetic. The capture tap
// that produced these rows sits DOWNSTREAM of the Extract Gate, so the stream it
// samples is the pages the crawler ALREADY pays an extract call on. Read the numbers
// accordingly: ExtractCallRate is the share of TODAY'S calls a gate config would
// still make, and Recall the share of today's real postings it would keep. A page
// the gate already rejects never reached the tap and is invisible here -- measuring
// those is the Boundary Stratum's and the Shadow Extraction's job.
//
// PURE -- no parser, network, or model, mirroring freeextract.go's separation:
// cmd/llmbench replays the real gate to PRODUCE the rows, and this folds them.
package bench

import "math"

// StreamScorecard is the sampling-weighted estimate of the live extract stream.
// Every field is DESCRIPTIVE: nothing here moves an exit code, because a weighted
// estimate carries sampling error and depends on the stream's composition, which is
// exactly the kind of number ADR-0020 refuses to threshold.
type StreamScorecard struct {
	// Rows and WeightSum are the raw sample size and its total weight. EffectiveN is
	// Kish's effective sample size (Sum(w))^2 / Sum(w^2): the number of
	// equally-weighted rows this weighted sample is worth. It falls below Rows as the
	// weights spread, and it is the honest answer to "how much does this rest on".
	Rows       int     `json:"rows"`
	WeightSum  float64 `json:"weight_sum"`
	EffectiveN float64 `json:"effective_n"`
	// Counts are the RAW rows behind each weighted share, so nobody reads a share
	// resting on two rows as if it were solid.
	Counts map[ExtractLabel]int `json:"counts"`
	// Composition is each label's weighted share of the stream: what the crawler's
	// extract calls are actually spent on.
	Composition map[ExtractLabel]float64 `json:"composition"`
	// ExtractCallRate is the weighted share of the stream this gate config extracts.
	// Against a stream sampled downstream of the gate it reads as the share of
	// today's calls the config would still make -- the cost multiplier.
	ExtractCallRate float64 `json:"extract_call_rate"`
	// Precision is the weighted share of the rows the config extracts that really are
	// single postings; Recall the weighted share of the stream's real postings it
	// keeps.
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

// ScoreExtractStream folds weighted extract verdict rows into a StreamScorecard.
// PURE -- no parser, network, or LLM. Both maps are initialized for every label, so
// the JSON round-trips and an EMPTY cell is visible rather than absent. A zero total
// weight yields zeroes rather than a division: an empty or unweighted selection is a
// caller error the scorer reports by producing nothing, never by panicking.
func ScoreExtractStream(rows []ExtractVerdictRow) StreamScorecard {
	sc := StreamScorecard{
		Counts:      map[ExtractLabel]int{},
		Composition: map[ExtractLabel]float64{},
	}
	for _, l := range AllExtractLabels {
		sc.Counts[l] = 0
		sc.Composition[l] = 0
	}

	weightByLabel := map[ExtractLabel]float64{}
	total, squares, extracted, detail, detailExtracted := 0.0, 0.0, 0.0, 0.0, 0.0
	for _, row := range rows {
		sc.Rows++
		sc.Counts[row.Label]++
		weightByLabel[row.Label] += row.Weight
		total += row.Weight
		squares += row.Weight * row.Weight
		if row.Extract {
			extracted += row.Weight
		}
		if row.Label.Positive() {
			detail += row.Weight
			if row.Extract {
				detailExtracted += row.Weight
			}
		}
	}

	sc.WeightSum = math.Round(total*10000) / 10000
	if squares > 0 {
		sc.EffectiveN = math.Round(total*total/squares*10) / 10
	}
	for label, w := range weightByLabel {
		sc.Composition[label] = weightedRatio(w, total)
	}
	sc.ExtractCallRate = weightedRatio(extracted, total)
	sc.Precision = weightedRatio(detailExtracted, extracted)
	sc.Recall = weightedRatio(detailExtracted, detail)
	return sc
}
