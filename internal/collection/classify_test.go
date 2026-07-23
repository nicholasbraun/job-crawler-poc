package collection

import (
	"errors"
	"fmt"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
)

// TestClassifyStatus is an internal test: classifyStatus is unexported pure logic
// (the crawl-lane status→ProbeOutcome mapping, ADR-0035) with no public surface.
func TestClassifyStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want crawler.ProbeOutcome
	}{
		{"nil (2xx reach) is alive", nil, crawler.ProbeAlive},
		{"404 is dead", &downloader.StatusError{StatusCode: 404}, crawler.ProbeDead},
		{"410 is dead", &downloader.StatusError{StatusCode: 410}, crawler.ProbeDead},
		{"403 is inconclusive", &downloader.StatusError{StatusCode: 403}, crawler.ProbeInconclusive},
		{"500 is inconclusive", &downloader.StatusError{StatusCode: 500, Retryable: true}, crawler.ProbeInconclusive},
		{"429 is inconclusive", &downloader.StatusError{StatusCode: 429, Retryable: true}, crawler.ProbeInconclusive},
		{"transport/timeout error is inconclusive", errors.New("dial tcp: timeout"), crawler.ProbeInconclusive},
		{"wrapped 404 is still dead", fmt.Errorf("get: %w", &downloader.StatusError{StatusCode: 404}), crawler.ProbeDead},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyStatus(tt.err); got != tt.want {
				t.Errorf("classifyStatus(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
