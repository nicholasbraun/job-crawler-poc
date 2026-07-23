package crawler_test

import (
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestNextDormancy(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	lastOK := now.Add(-48 * time.Hour)

	tests := []struct {
		name         string
		failures     int
		outcome      crawler.ProbeOutcome
		wantFailures int
		wantLastOK   time.Time
	}{
		{"dead increments and leaves lastOK", 2, crawler.ProbeDead, 3, lastOK},
		{"dead from zero", 0, crawler.ProbeDead, 1, lastOK},
		{"alive resets and stamps now", 4, crawler.ProbeAlive, 0, now},
		{"inconclusive is a no-op", 2, crawler.ProbeInconclusive, 2, lastOK},
		{"inconclusive from zero stays zero", 0, crawler.ProbeInconclusive, 0, lastOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotFailures, gotLastOK := crawler.NextDormancy(tt.failures, lastOK, tt.outcome, now)
			if gotFailures != tt.wantFailures {
				t.Errorf("failures = %d, want %d", gotFailures, tt.wantFailures)
			}
			if !gotLastOK.Equal(tt.wantLastOK) {
				t.Errorf("lastOK = %v, want %v", gotLastOK, tt.wantLastOK)
			}
		})
	}
}

func TestDormant(t *testing.T) {
	tests := []struct {
		failures  int
		threshold int
		want      bool
	}{
		{0, 5, false},
		{4, 5, false},
		{5, 5, true},
		{6, 5, true},
	}
	for _, tt := range tests {
		if got := crawler.Dormant(tt.failures, tt.threshold); got != tt.want {
			t.Errorf("Dormant(%d, %d) = %v, want %v", tt.failures, tt.threshold, got, tt.want)
		}
	}
}
