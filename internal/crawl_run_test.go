package crawler_test

import (
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

func TestRunStatusTerminal(t *testing.T) {
	cases := map[crawler.RunStatus]bool{
		crawler.RunStatusRunning:   false,
		crawler.RunStatusStopping:  false,
		crawler.RunStatusPausing:   false,
		crawler.RunStatusPaused:    false, // parked but resumable — not terminal
		crawler.RunStatusStopped:   true,
		crawler.RunStatusCompleted: true,
		crawler.RunStatusFailed:    true,
	}
	for status, want := range cases {
		if got := status.Terminal(); got != want {
			t.Errorf("%q.Terminal() = %v, want %v", status, got, want)
		}
	}
}
