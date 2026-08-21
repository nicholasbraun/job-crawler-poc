package main

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// goldFidelityWordRun builds n distinct words with the given prefix, so a test can
// state a retention EXACTLY rather than approximately. With every word distinct, a
// run of n words carries n-2 distinct 3-grams, and replacing the last k of them
// destroys exactly k grams -- the grams a replaced word appears in are precisely those
// starting at index n-k-2 and later.
func goldFidelityWordRun(prefix string, n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("%s%03d", prefix, i)
	}
	return strings.Join(words, " ")
}

// goldFidelityDrifted returns the captured run with its last k words replaced, which
// is a retention of (n-2-k)/(n-2).
func goldFidelityDrifted(n, k int) string {
	kept := strings.Fields(goldFidelityWordRun("w", n))[:n-k]
	return strings.Join(kept, " ") + " " + goldFidelityWordRun("z", k)
}

// TestGoldFidelityOf is the pure rule ADR-0047 rests on, table-driven across all three
// states. Every `gone` row is additionally asserted to carry NO live view, because "a
// gone row is never offered a live view" is the rule that protects a label rather than
// merely an aid, and this is where it is decided.
func TestGoldFidelityOf(t *testing.T) {
	const captured = "We are hiring a Senior Go Engineer in Berlin to build the crawling platform end to end."
	const capturedTitle = "Senior Go Engineer -- Acme"

	// 102 distinct words is 100 distinct 3-grams, so a retention lands on a round
	// percentage and the two thresholds can be tested AT their boundary.
	longCaptured := goldFidelityWordRun("w", 102)

	tests := []struct {
		name string
		in   goldFidelityInput
		want goldFidelity
	}{
		{
			name: "an identical page is the page the label is about",
			in:   goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: capturedTitle, CapturedText: captured, LiveText: captured},
			want: fidelitySame,
		},
		{
			name: "boilerplate added around the captured text keeps every captured gram",
			in: goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: capturedTitle,
				CapturedText: captured, LiveText: "Cookie preferences. " + captured + " Follow us on social media."},
			want: fidelitySame,
		},
		{
			name: "retention exactly at the same threshold is same, inclusively",
			in: goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: capturedTitle,
				CapturedText: longCaptured, LiveText: goldFidelityDrifted(102, 10)},
			want: fidelitySame,
		},
		{
			name: "the same text under a changed title only ever withdraws the claim",
			in: goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: "Careers at Acme",
				CapturedText: captured, LiveText: captured},
			want: fidelityDrifted,
		},
		{
			name: "a capture that carried no title lets retention decide alone",
			in: goldFidelityInput{Status: 200, CapturedTitle: "", LiveTitle: "Careers at Acme",
				CapturedText: captured, LiveText: captured},
			want: fidelitySame,
		},
		{
			name: "half the captured text replaced is drift",
			in: goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: capturedTitle,
				CapturedText: longCaptured, LiveText: goldFidelityDrifted(102, 50)},
			want: fidelityDrifted,
		},
		{
			name: "retention exactly at the gone threshold is still drift; gone is strictly below",
			in: goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: capturedTitle,
				CapturedText: longCaptured, LiveText: goldFidelityDrifted(102, 70)},
			want: fidelityDrifted,
		},
		{
			name: "a 200 whose captured text has vanished is gone, not drifted",
			in: goldFidelityInput{Status: 200, CapturedTitle: capturedTitle, LiveTitle: capturedTitle,
				CapturedText: captured,
				LiveText:     "This position has been filled. Applications are closed. Browse our current openings."},
			want: fidelityGone,
		},
		{
			name: "a 404 is gone",
			in:   goldFidelityInput{Status: http.StatusNotFound, CapturedText: captured},
			want: fidelityGone,
		},
		{
			name: "a 500 is gone",
			in:   goldFidelityInput{Status: http.StatusInternalServerError, CapturedText: captured},
			want: fidelityGone,
		},
		{
			name: "a host that never answered is gone",
			in:   goldFidelityInput{Status: 0, CapturedText: captured},
			want: fidelityGone,
		},
		{
			name: "a JS shell renders empty in both, so the aid agrees with the capture",
			in: goldFidelityInput{Status: 200, CapturedTitle: "Jobs at Acme", LiveTitle: "Jobs at Acme",
				CapturedText: "", LiveText: ""},
			want: fidelitySame,
		},
		{
			name: "a capture that said nothing against a live page that says something is a hint, never gone",
			in: goldFidelityInput{Status: 200, CapturedTitle: "Jobs at Acme", LiveTitle: "Jobs at Acme",
				CapturedText: "", LiveText: captured},
			want: fidelityDrifted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goldFidelityOf(tt.in)
			if got.State != tt.want {
				t.Fatalf("goldFidelityOf = %q (retention %.2f over %d grams), want %q -- %s",
					got.State, got.Retention, got.Grams, tt.want, got.Reason)
			}
			if got.Reason == "" {
				t.Error("the report carries no reason; the labeller is told what the aid is worth in words, not in a state name")
			}
			wantLiveView := tt.want != fidelityGone
			if got.LiveView != wantLiveView {
				t.Errorf("live_view = %v on a %q row, want %v: a gone row is never offered a live view -- a closed posting argues confidently for the wrong label (ADR-0047)",
					got.LiveView, got.State, wantLiveView)
			}
		})
	}
}

// TestGoldFidelityRetentionIsExactAtTheThresholds pins the two numbers the states are
// cut at, so a change to either is a deliberate edit and not a drifting side effect.
func TestGoldFidelityRetentionIsExactAtTheThresholds(t *testing.T) {
	tests := []struct {
		replaced int
		want     float64
	}{{0, 1}, {10, 0.90}, {50, 0.50}, {70, 0.30}, {100, 0}}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d words replaced", tt.replaced), func(t *testing.T) {
			got := goldFidelityOf(goldFidelityInput{
				Status:       200,
				CapturedText: goldFidelityWordRun("w", 102),
				LiveText:     goldFidelityDrifted(102, tt.replaced),
			})
			if got.Grams != 100 {
				t.Fatalf("the comparison ran over %d captured 3-grams, want 100", got.Grams)
			}
			if got.Retention != tt.want {
				t.Errorf("retention = %v, want %v", got.Retention, tt.want)
			}
		})
	}
}

// TestGoldFidelityGramsNormalise pins what counts as the same word: punctuation and
// case churn is not drift, and a text too short to carry a 3-gram carries none.
func TestGoldFidelityGramsNormalise(t *testing.T) {
	rep := goldFidelityOf(goldFidelityInput{
		Status:       200,
		CapturedText: "Senior Engineer, Berlin — Remote",
		LiveText:     "senior engineer berlin remote",
	})
	if rep.State != fidelitySame || rep.Retention != 1 {
		t.Errorf("punctuation and case churn read as %q at retention %v, want same at 1", rep.State, rep.Retention)
	}
	if got := len(goldFidelityGrams("two words")); got != 0 {
		t.Errorf("a two-word text carries %d 3-grams, want 0", got)
	}
	if got := len(goldFidelityGrams("exactly three words")); got != 1 {
		t.Errorf("a three-word text carries %d 3-grams, want 1", got)
	}
	// Set semantics: a boilerplate line repeated forty times is one gram, so it cannot
	// dominate the score.
	repeated := strings.TrimSpace(strings.Repeat("apply now today ", 40))
	if got := len(goldFidelityGrams(repeated)); got != 3 {
		t.Errorf("a repeated boilerplate line carries %d distinct 3-grams, want 3", got)
	}
}

// TestGoldFidelityReadsNothingDerivedFromStructuredData is the constraint ADR-0047
// calls out by name, asserted where it can actually be enforced: the rule's input has
// four fields and none of them is a posting-node count, an embed or an element id.
// A field added here would be a signal the row's stratum IS, and the gold set exists
// to test the mechanism that produces it (ADR-0043).
func TestGoldFidelityReadsNothingDerivedFromStructuredData(t *testing.T) {
	want := map[string]bool{"Status": true, "CapturedTitle": true, "LiveTitle": true, "CapturedText": true, "LiveText": true}
	for _, field := range structFieldNames(goldFidelityInput{}) {
		if !want[field] {
			t.Errorf("goldFidelityInput carries %q; the fidelity rule reads the status, the titles and the texts and nothing else (ADR-0047)", field)
		}
		delete(want, field)
	}
	for field := range want {
		t.Errorf("goldFidelityInput no longer carries %q, which the rule is defined over", field)
	}
}

// structFieldNames lists a struct's exported field names, so a test can assert what a
// type is allowed to read rather than trusting a comment to hold the line.
func structFieldNames(v any) []string {
	typ := reflect.TypeOf(v)
	names := make([]string, 0, typ.NumField())
	for i := range typ.NumField() {
		names = append(names, typ.Field(i).Name)
	}
	return names
}
