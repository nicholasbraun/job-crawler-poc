package bench_test

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/cmd/llmbench/bench"
)

// fields is the judgment triple a clean detail row both expects and produces.
func fields() bench.FreeExtractionFields {
	return bench.FreeExtractionFields{Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified"}
}

// freeRow builds a row the Free Extraction served, expecting exactly what it
// produced -- the clean case every fatal-condition test perturbs one field of.
func freeRow(url string, label bench.ExtractLabel) bench.FreeExtractionRow {
	want := fields()
	return bench.FreeExtractionRow{URL: url, Label: label, Weight: 1, Free: true, Got: fields(), Want: &want}
}

// contains reports whether urls holds url.
func contains(urls []string, url string) bool {
	for _, u := range urls {
		if u == url {
			return true
		}
	}
	return false
}

// TestScoreFreeExtractionFatalConditions asserts the two silent failure modes the
// whole guard exists for -- a Free Extraction saving a page that is not a posting,
// and one mis-reading a field -- each directly, from rows in to scorecard out.
func TestScoreFreeExtractionFatalConditions(t *testing.T) {
	t.Run("a clean detail row scores free with no fatal signal", func(t *testing.T) {
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{freeRow("https://a.test/jobs/1", bench.ExtractDetail)})
		if r.Failed() {
			t.Errorf("clean row failed the check: %+v", r.Free)
		}
		if r.Free.Free != 1 || r.Free.Coverage != 1 {
			t.Errorf("free = %d coverage = %v, want 1 and 1", r.Free.Free, r.Free.Coverage)
		}
	})

	t.Run("firing on a hub-index row is fatal and names the url", func(t *testing.T) {
		const url = "https://a.test/careers"
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{freeRow(url, bench.ExtractHubIndex)})
		if !r.Failed() {
			t.Error("a Free Extraction on a hub-index row did not fail the check")
		}
		if !contains(r.Free.FiredOnNonPosting, url) {
			t.Errorf("fired-on-non-posting = %v, want it to name %q", r.Free.FiredOnNonPosting, url)
		}
	})

	t.Run("firing on a residue row is fatal and names the url", func(t *testing.T) {
		const url = "https://a.test/about"
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{freeRow(url, bench.ExtractResidue)})
		if !r.Failed() {
			t.Error("a Free Extraction on a residue row did not fail the check")
		}
		if !contains(r.Free.FiredOnNonPosting, url) {
			t.Errorf("fired-on-non-posting = %v, want it to name %q", r.Free.FiredOnNonPosting, url)
		}
	})

	t.Run("an accepted fire on a residue row is descriptive, not fatal", func(t *testing.T) {
		const url = "https://a.test/jobs/expired"
		row := freeRow(url, bench.ExtractResidue)
		row.AcceptedFire = true
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
		if r.Failed() {
			t.Errorf("an explicitly accepted fire failed the check: %+v", r.Free)
		}
		if !contains(r.Free.AcceptedFires, url) {
			t.Errorf("accepted-fires = %v, want it to name %q", r.Free.AcceptedFires, url)
		}
		if contains(r.Free.FiredOnNonPosting, url) {
			t.Errorf("an accepted fire is also counted fatal: %v", r.Free.FiredOnNonPosting)
		}
	})

	t.Run("an accepted fire on a hub-index row is still fatal", func(t *testing.T) {
		const url = "https://a.test/jobs"
		row := freeRow(url, bench.ExtractHubIndex)
		row.AcceptedFire = true
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
		if !r.Failed() {
			t.Error("a hub-index fire was excused; a hub is the shape the predicate rejects structurally")
		}
		if !contains(r.Free.FiredOnNonPosting, url) {
			t.Errorf("fired-on-non-posting = %v, want it to name %q", r.Free.FiredOnNonPosting, url)
		}
		if contains(r.Free.AcceptedFires, url) {
			t.Errorf("a hub-index fire landed in accepted-fires: %v", r.Free.AcceptedFires)
		}
	})

	t.Run("a diverging field is fatal and names the field and the url", func(t *testing.T) {
		const url = "https://a.test/jobs/1"
		tests := []struct {
			field string
			got   bench.FreeExtractionFields
			want  string
		}{
			{"title", bench.FreeExtractionFields{Title: "Junior Go Engineer", Location: "Berlin, DE", WorkArrangement: "unspecified"}, "Senior Go Engineer"},
			{"location", bench.FreeExtractionFields{Title: "Senior Go Engineer", Location: "Munich, DE", WorkArrangement: "unspecified"}, "Berlin, DE"},
			{"work_arrangement", bench.FreeExtractionFields{Title: "Senior Go Engineer", Location: "Berlin, DE", WorkArrangement: "remote"}, "unspecified"},
		}
		for _, tt := range tests {
			t.Run(tt.field, func(t *testing.T) {
				row := freeRow(url, bench.ExtractDetail)
				row.Got = tt.got
				r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
				if !r.Failed() {
					t.Fatalf("a diverging %s did not fail the check", tt.field)
				}
				if len(r.Free.FieldDivergences) != 1 {
					t.Fatalf("divergences = %+v, want exactly one", r.Free.FieldDivergences)
				}
				d := r.Free.FieldDivergences[0]
				if d.URL != url || d.Field != tt.field || d.Want != tt.want {
					t.Errorf("divergence = %+v, want %s [%s] want %q", d, url, tt.field, tt.want)
				}
			})
		}
	})

	t.Run("several fields diverging on one page are reported one by one", func(t *testing.T) {
		row := freeRow("https://a.test/jobs/1", bench.ExtractDetail)
		row.Got = bench.FreeExtractionFields{Title: "x", Location: "y", WorkArrangement: "remote"}
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
		if len(r.Free.FieldDivergences) != 3 {
			t.Errorf("divergences = %+v, want one per differing field", r.Free.FieldDivergences)
		}
	})

	t.Run("an expected location of empty diverges from a produced one", func(t *testing.T) {
		row := freeRow("https://a.test/jobs/1", bench.ExtractDetail)
		row.Want.Location = ""
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
		if !r.Failed() {
			t.Fatal("a location invented for a page that publishes none did not fail the check")
		}
		if d := r.Free.FieldDivergences[0]; d.Field != "location" || d.Want != "" || d.Got != "Berlin, DE" {
			t.Errorf("divergence = %+v, want an empty location against %q", d, "Berlin, DE")
		}
	})

	t.Run("firing with no expected value is fatal", func(t *testing.T) {
		const url = "https://a.test/jobs/1"
		row := freeRow(url, bench.ExtractDetail)
		row.Want = nil
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
		if !r.Failed() || !contains(r.Free.MissingExpectation, url) {
			t.Errorf("missing-expectation = %v (failed %v), want it to name %q", r.Free.MissingExpectation, r.Failed(), url)
		}
	})

	t.Run("an expected value on a page that did not fire is fatal", func(t *testing.T) {
		const url = "https://a.test/jobs/1"
		row := freeRow(url, bench.ExtractDetail)
		row.Free = false
		row.Got = bench.FreeExtractionFields{}
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{row})
		if !r.Failed() || !contains(r.Free.UnusedExpectation, url) {
			t.Errorf("unused-expectation = %v (failed %v), want it to name %q", r.Free.UnusedExpectation, r.Failed(), url)
		}
	})
}

// TestScoreFreeExtractionSoftMeasurements pins the descriptive half of the
// scorecard: coverage is the share of REAL POSTINGS served without a model, and the
// stream shares are sampling-weighted, so the file's ~54x oversample of the
// mechanism's own stratum never reads as a stream number.
func TestScoreFreeExtractionSoftMeasurements(t *testing.T) {
	t.Run("coverage counts detail rows only", func(t *testing.T) {
		delegated := func(url string, label bench.ExtractLabel) bench.FreeExtractionRow {
			return bench.FreeExtractionRow{URL: url, Label: label, Weight: 1}
		}
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{
			freeRow("https://a.test/jobs/1", bench.ExtractDetail),
			freeRow("https://a.test/jobs/2", bench.ExtractDetail),
			delegated("https://a.test/jobs/3", bench.ExtractDetail),
			delegated("https://a.test/about", bench.ExtractResidue),
		})
		if r.Failed() {
			t.Fatalf("soft-measurement rows failed the check: %+v", r.Free)
		}
		if r.Free.DetailTotal != 3 || r.Free.DetailFree != 2 {
			t.Errorf("detail %d/%d free, want 3/2", r.Free.DetailFree, r.Free.DetailTotal)
		}
		if math.Abs(r.Free.Coverage-2.0/3.0) > 1e-4 {
			t.Errorf("coverage = %v, want 2/3", r.Free.Coverage)
		}
		if r.Free.FreeRate != 0.5 {
			t.Errorf("free-rate = %v, want 0.5 over 4 rows", r.Free.FreeRate)
		}
	})

	t.Run("the stream shares are sampling-weighted", func(t *testing.T) {
		free := freeRow("https://a.test/jobs/1", bench.ExtractDetail)
		free.Weight = 0.5
		delegated := bench.FreeExtractionRow{URL: "https://a.test/jobs/2", Label: bench.ExtractDetail, Weight: 2.5}
		r := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{free, delegated})

		// 0.5 of 3.0 total weight, and the same over the detail weight, against an
		// unweighted 1 of 2.
		if r.Free.StreamFreeShare != 0.1667 {
			t.Errorf("stream-free-share = %v, want 0.1667 (0.5 of 3.0)", r.Free.StreamFreeShare)
		}
		if r.Free.WeightedCoverage != 0.1667 {
			t.Errorf("weighted-coverage = %v, want 0.1667 (0.5 of 3.0)", r.Free.WeightedCoverage)
		}
		if r.Free.FreeRate != 0.5 || r.Free.Coverage != 0.5 {
			t.Errorf("unweighted rates = %v/%v, want 0.5 each", r.Free.FreeRate, r.Free.Coverage)
		}
		if r.Free.StreamFreeShare == r.Free.FreeRate {
			t.Error("the weighted share equals the unweighted rate, so the weights are doing nothing")
		}
	})

	t.Run("an empty row set scores zero rather than dividing by zero", func(t *testing.T) {
		r := bench.ScoreFreeExtraction(nil)
		if r.Failed() {
			t.Error("an empty row set failed the check")
		}
		if r.Free.Total != 0 || r.Free.FreeRate != 0 || r.Free.Coverage != 0 || r.Free.StreamFreeShare != 0 || r.Free.WeightedCoverage != 0 {
			t.Errorf("empty scorecard = %+v, want zeros", r.Free)
		}
	})
}

// TestFreeExtractionReportRoundTrip proves every tagged field survives the JSON
// surface the score-free verb emits, so a scorecard read by a tool is the scorecard
// the scorer produced.
func TestFreeExtractionReportRoundTrip(t *testing.T) {
	diverging := freeRow("https://a.test/jobs/diverge", bench.ExtractDetail)
	diverging.Got.Title = "Something Else"
	accepted := freeRow("https://a.test/jobs/expired", bench.ExtractResidue)
	accepted.AcceptedFire = true
	unused := freeRow("https://a.test/jobs/quiet", bench.ExtractDetail)
	unused.Free, unused.Got = false, bench.FreeExtractionFields{}
	missing := freeRow("https://a.test/jobs/new", bench.ExtractDetail)
	missing.Want = nil
	leaked := freeRow("https://a.test/careers", bench.ExtractHubIndex)

	orig := bench.ScoreFreeExtraction([]bench.FreeExtractionRow{diverging, accepted, unused, missing, leaked})
	if !orig.Failed() {
		t.Fatal("the round-trip fixture exercises no fatal list")
	}

	var buf bytes.Buffer
	if err := bench.EncodeFreeExtractionReport(&buf, orig); err != nil {
		t.Fatalf("EncodeFreeExtractionReport: %v", err)
	}
	var got bench.FreeExtractionReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode free extraction report: %v", err)
	}
	if !reflect.DeepEqual(orig, got) {
		t.Errorf("round-trip mismatch:\n orig = %+v\n got  = %+v", orig, got)
	}
}
