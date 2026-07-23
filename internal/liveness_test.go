package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// TestNextLiveness exercises the pure Listing-Liveness reducer (ADR-0035) across
// every ProbeOutcome: reopen on Alive, the ATS incomplete-fetch interlock on Dead,
// the crawl-lane Inconclusive staleness ladder, and the streak reset/preserve rules.
func TestNextLiveness(t *testing.T) {
	cases := []struct {
		name      string
		current   crawler.LifecycleState
		outcome   crawler.ProbeOutcome
		complete  bool
		threshold int
		want      crawler.LifecycleState
	}{
		{
			name:      "alive keeps an open listing open and streak clear",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
			outcome:   crawler.ProbeAlive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
		},
		{
			name:      "alive reopens a closed listing",
			current:   crawler.LifecycleState{Open: false, InconclusiveStreak: 0},
			outcome:   crawler.ProbeAlive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
		},
		{
			name:      "alive resets an accrued streak so a flap never accumulates",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 2},
			outcome:   crawler.ProbeAlive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
		},
		{
			name:      "dead on a complete observation closes (crawl 404 / ATS absent-complete)",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
			outcome:   crawler.ProbeDead,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: false, InconclusiveStreak: 0},
		},
		{
			name:      "dead on an incomplete fetch closes nothing (ATS interlock)",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
			outcome:   crawler.ProbeDead,
			complete:  false,
			threshold: 3,
			want:      crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
		},
		{
			name:      "first inconclusive increments the streak, stays open",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
			outcome:   crawler.ProbeInconclusive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: true, InconclusiveStreak: 1},
		},
		{
			name:      "second inconclusive increments the streak, stays open",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 1},
			outcome:   crawler.ProbeInconclusive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: true, InconclusiveStreak: 2},
		},
		{
			name:      "inconclusive reaching the threshold fires the staleness backstop",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 2},
			outcome:   crawler.ProbeInconclusive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: false, InconclusiveStreak: 3},
		},
		{
			name:      "threshold of 1 closes on the first inconclusive",
			current:   crawler.LifecycleState{Open: true, InconclusiveStreak: 0},
			outcome:   crawler.ProbeInconclusive,
			complete:  true,
			threshold: 1,
			want:      crawler.LifecycleState{Open: false, InconclusiveStreak: 1},
		},
		{
			name:      "inconclusive on an already-closed listing stays closed (defensive)",
			current:   crawler.LifecycleState{Open: false, InconclusiveStreak: 3},
			outcome:   crawler.ProbeInconclusive,
			complete:  true,
			threshold: 3,
			want:      crawler.LifecycleState{Open: false, InconclusiveStreak: 4},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := crawler.NextLiveness(tc.current, tc.outcome, tc.complete, tc.threshold)
			if got != tc.want {
				t.Errorf("NextLiveness(%+v, %v, complete=%v, thr=%d) = %+v, want %+v",
					tc.current, tc.outcome, tc.complete, tc.threshold, got, tc.want)
			}
		})
	}
}

// TestProbeOutcomeZeroValue pins the zero value of ProbeOutcome to Inconclusive —
// the do-least-harm default, so an unset probe can never close a listing.
func TestProbeOutcomeZeroValue(t *testing.T) {
	var zero crawler.ProbeOutcome
	if zero != crawler.ProbeInconclusive {
		t.Errorf("zero ProbeOutcome = %v, want ProbeInconclusive", zero)
	}
}
