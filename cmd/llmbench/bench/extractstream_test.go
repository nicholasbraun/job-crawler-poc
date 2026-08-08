package bench_test

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// streamRow builds one weighted extract verdict row.
func streamRow(url string, label bench.ExtractLabel, extract bool, weight float64) bench.ExtractVerdictRow {
	return bench.ExtractVerdictRow{URL: url, Label: label, Extract: extract, Weight: weight}
}

// closeTo reports whether two rounded figures agree. The scorer rounds to four
// decimals, so an exact comparison against a hand-computed fraction is the wrong
// question to ask.
func closeTo(got, want float64) bool { return math.Abs(got-want) <= 1e-4 }

// TestScoreExtractStreamWeightsTheSampleBackToTheStream is the property the whole
// stratum exists for: with equal weights the weighted numbers ARE the raw ones, and
// with the real ~6:1 abstain:accept weighting a single heavily-weighted row outvotes
// several light ones. A scorer that quietly ignored Weight would pass the first case
// and fail the second.
func TestScoreExtractStreamWeightsTheSampleBackToTheStream(t *testing.T) {
	t.Run("equal weights reproduce the raw sample", func(t *testing.T) {
		rows := []bench.ExtractVerdictRow{
			streamRow("https://a.test/1", bench.ExtractDetail, true, 1),
			streamRow("https://a.test/2", bench.ExtractDetail, false, 1),
			streamRow("https://a.test/3", bench.ExtractHubIndex, true, 1),
			streamRow("https://a.test/4", bench.ExtractResidue, false, 1),
		}
		got := bench.ScoreExtractStream(rows)

		if got.Rows != 4 || !closeTo(got.WeightSum, 4) {
			t.Errorf("rows %d weight-sum %.4f, want 4 and 4", got.Rows, got.WeightSum)
		}
		// Kish's effective n is the row count exactly when no row is worth more than
		// another; it is the number that tells a reader how much a weighted estimate
		// actually rests on.
		if !closeTo(got.EffectiveN, 4) {
			t.Errorf("effective n %.4f, want 4 under equal weights", got.EffectiveN)
		}
		if !closeTo(got.Composition[bench.ExtractDetail], 0.5) {
			t.Errorf("detail composition %.4f, want 0.5", got.Composition[bench.ExtractDetail])
		}
		if !closeTo(got.ExtractCallRate, 0.5) {
			t.Errorf("extract-call rate %.4f, want 0.5", got.ExtractCallRate)
		}
		if !closeTo(got.Precision, 0.5) || !closeTo(got.Recall, 0.5) {
			t.Errorf("precision %.4f recall %.4f, want 0.5 and 0.5", got.Precision, got.Recall)
		}
	})

	t.Run("a heavy row outvotes several light ones", func(t *testing.T) {
		// The committed drawing's own weights: an accept row is worth 0.2259 of the
		// stream, an abstain row 1.38705. Three accept-side detail rows against one
		// abstain-side residue row is a 3:1 raw majority and a 0.678:1.387 weighted
		// minority, so composition must invert.
		const wAccept, wAbstain = 0.2259, 1.38705
		rows := []bench.ExtractVerdictRow{
			streamRow("https://a.test/1", bench.ExtractDetail, true, wAccept),
			streamRow("https://a.test/2", bench.ExtractDetail, true, wAccept),
			streamRow("https://a.test/3", bench.ExtractDetail, true, wAccept),
			streamRow("https://a.test/4", bench.ExtractResidue, false, wAbstain),
		}
		got := bench.ScoreExtractStream(rows)

		total := 3*wAccept + wAbstain
		wantDetail := 3 * wAccept / total // 0.3283
		if !closeTo(got.Composition[bench.ExtractDetail], wantDetail) {
			t.Errorf("detail composition %.4f, want %.4f (the raw share is 0.75)", got.Composition[bench.ExtractDetail], wantDetail)
		}
		if got.Composition[bench.ExtractDetail] >= 0.5 {
			t.Errorf("detail composition %.4f did not fall below the raw 0.75 majority; the weights are being ignored", got.Composition[bench.ExtractDetail])
		}
		if !closeTo(got.ExtractCallRate, wantDetail) {
			t.Errorf("extract-call rate %.4f, want %.4f", got.ExtractCallRate, wantDetail)
		}
		// The counts exist so nobody reads a weighted share resting on one row as if
		// it were solid.
		if got.Counts[bench.ExtractDetail] != 3 || got.Counts[bench.ExtractResidue] != 1 {
			t.Errorf("counts %v, want 3 detail and 1 residue", got.Counts)
		}
		if got.EffectiveN >= float64(got.Rows) {
			t.Errorf("effective n %.1f is not below the %d rows despite spread weights", got.EffectiveN, got.Rows)
		}
	})

	t.Run("precision and recall are weighted over a confusion", func(t *testing.T) {
		rows := []bench.ExtractVerdictRow{
			streamRow("https://a.test/kept", bench.ExtractDetail, true, 2),     // TP, weight 2
			streamRow("https://a.test/dropped", bench.ExtractDetail, false, 6), // FN, weight 6
			streamRow("https://a.test/leak", bench.ExtractHubIndex, true, 2),   // FP, weight 2
			streamRow("https://a.test/shed", bench.ExtractResidue, false, 2),   // TN, weight 2
		}
		got := bench.ScoreExtractStream(rows)

		if !closeTo(got.Precision, 0.5) {
			t.Errorf("precision %.4f, want 0.5 (2 of the 4 extracted weight)", got.Precision)
		}
		if !closeTo(got.Recall, 0.25) {
			t.Errorf("recall %.4f, want 0.25 (2 of the 8 detail weight -- the raw recall is 0.5)", got.Recall)
		}
	})

	t.Run("zero weight yields zeroes rather than a division", func(t *testing.T) {
		got := bench.ScoreExtractStream([]bench.ExtractVerdictRow{
			streamRow("https://a.test/1", bench.ExtractDetail, true, 0),
		})
		if got.Rows != 1 || got.WeightSum != 0 || got.EffectiveN != 0 {
			t.Errorf("scorecard = %+v, want 1 row with zero weight and zero effective n", got)
		}
		if got.ExtractCallRate != 0 || got.Precision != 0 || got.Recall != 0 {
			t.Errorf("scorecard = %+v, want zeroed rates", got)
		}
	})

	t.Run("an empty selection does not panic", func(t *testing.T) {
		got := bench.ScoreExtractStream(nil)
		if got.Rows != 0 {
			t.Errorf("rows = %d, want 0", got.Rows)
		}
		for _, l := range bench.AllExtractLabels {
			if _, ok := got.Composition[l]; !ok {
				t.Errorf("composition has no entry for %q; an empty cell must be visible, not absent", l)
			}
		}
	})
}

// TestStreamScorecardIsDescriptive is ADR-0020's split made testable: the extract
// run fails on a structural false-drop and on nothing else. A weighted estimate
// carries sampling error and depends on the stream's composition, so it can never
// move an exit code however bad it looks.
func TestStreamScorecardIsDescriptive(t *testing.T) {
	awful := bench.StreamScorecard{Rows: 120, ExtractCallRate: 1, Precision: 0, Recall: 0}
	r := bench.ScoreExtract([]bench.ExtractVerdictRow{
		streamRow("https://a.test/1", bench.ExtractHubIndex, true, 1),
	})
	r.Stream = &awful
	if r.Failed() {
		t.Error("a stream scorecard of zero precision and zero recall failed the run; the weighted numbers must be descriptive")
	}
}

// TestStreamScorecardRoundTrips guards the -json surface: both maps must serialize
// with every label present, so a consumer reading a zero share can tell it from a
// missing one.
func TestStreamScorecardRoundTrips(t *testing.T) {
	want := bench.ScoreExtractStream([]bench.ExtractVerdictRow{
		streamRow("https://a.test/1", bench.ExtractDetail, true, 0.2259),
		streamRow("https://a.test/2", bench.ExtractHubIndex, false, 1.38705),
	})

	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got bench.StreamScorecard
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, l := range bench.AllExtractLabels {
		if _, ok := got.Composition[l]; !ok {
			t.Errorf("round-tripped composition has no entry for %q", l)
		}
		if _, ok := got.Counts[l]; !ok {
			t.Errorf("round-tripped counts have no entry for %q", l)
		}
	}
	if got.Rows != want.Rows || !closeTo(got.WeightSum, want.WeightSum) || !closeTo(got.Recall, want.Recall) {
		t.Errorf("round-tripped %+v, want %+v", got, want)
	}
}

// TestScoreExtractStreamKeepsAmbiguousOutOfPrecisionAndRecall pins where an
// unclassifiable page belongs in a weighted estimate: in the composition and in the
// call rate, because the crawler pays for it either way, and on NEITHER side of
// precision or recall, because it is neither a Job Listing nor known not to be one.
func TestScoreExtractStreamKeepsAmbiguousOutOfPrecisionAndRecall(t *testing.T) {
	sc := bench.ScoreExtractStream([]bench.ExtractVerdictRow{
		{URL: "https://a.test/posting", Label: bench.ExtractDetail, Extract: true, Weight: 1},
		{URL: "https://b.test/about", Label: bench.ExtractResidue, Extract: false, Weight: 2},
		{URL: "https://c.test/unclear", Label: bench.ExtractAmbiguous, Extract: true, Weight: 1},
	})

	if sc.Rows != 3 {
		t.Errorf("Rows = %d, want 3", sc.Rows)
	}
	if sc.Counts[bench.ExtractAmbiguous] != 1 {
		t.Errorf("the ambiguous row was not counted: %v", sc.Counts)
	}
	if got := sc.Composition[bench.ExtractAmbiguous]; got != 0.25 {
		t.Errorf("ambiguous composition = %g, want 0.25 (weight 1 of 4)", got)
	}
	// Two of the four weight units are extracted, the ambiguous one included.
	if sc.ExtractCallRate != 0.5 {
		t.Errorf("ExtractCallRate = %g, want 0.5 (an unclassifiable page still costs a call)", sc.ExtractCallRate)
	}
	// Precision is over the SCORED extractions alone: one detail extracted out of one
	// scored extraction, not out of two.
	if sc.Precision != 1 {
		t.Errorf("Precision = %g, want 1; the ambiguous extraction must not count against it", sc.Precision)
	}
	if sc.Recall != 1 {
		t.Errorf("Recall = %g, want 1", sc.Recall)
	}
}
