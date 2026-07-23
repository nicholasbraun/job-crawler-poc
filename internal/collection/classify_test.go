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

// TestClassifyPageProbe is an internal test of the pure Career-Page dormancy seam
// (ADR-0035 / #190): whole-page death — a 404/410 board OR a reachable 200 that no
// longer classifies as a careers page — is Dead; a transient GET, or a parse/classify
// failure on a reachable page, is Inconclusive; a reachable page that still classifies
// is Alive.
func TestClassifyPageProbe(t *testing.T) {
	tests := []struct {
		name            string
		getErr          error
		stillCareerPage bool
		classifyErr     error
		want            crawler.ProbeOutcome
	}{
		{"404 board is dead, classification ignored", &downloader.StatusError{StatusCode: 404}, true, nil, crawler.ProbeDead},
		{"410 board is dead", &downloader.StatusError{StatusCode: 410}, true, nil, crawler.ProbeDead},
		{"transient 5xx GET is inconclusive", &downloader.StatusError{StatusCode: 503, Retryable: true}, true, nil, crawler.ProbeInconclusive},
		{"transport error is inconclusive", errors.New("dial tcp: timeout"), true, nil, crawler.ProbeInconclusive},
		{"reachable + still classifies is alive", nil, true, nil, crawler.ProbeAlive},
		{"reachable + no longer classifies is dead", nil, false, nil, crawler.ProbeDead},
		{"reachable + classify error is inconclusive", nil, false, errors.New("llm down"), crawler.ProbeInconclusive},
		{"classify error dominates a would-be-alive verdict", nil, true, errors.New("llm down"), crawler.ProbeInconclusive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPageProbe(tt.getErr, tt.stillCareerPage, tt.classifyErr); got != tt.want {
				t.Errorf("classifyPageProbe(%v, %v, %v) = %v, want %v",
					tt.getErr, tt.stillCareerPage, tt.classifyErr, got, tt.want)
			}
		})
	}
}
