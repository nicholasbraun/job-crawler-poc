package collection

import (
	"errors"
	"net/http"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
)

// classifyStatus maps a Downloader.Get result to a Liveness ProbeOutcome
// (ADR-0035): nil (a 2xx reach) → Alive; a 404/410 StatusError → Dead; any other
// error (5xx/429/408, another 4xx, or a transport/timeout error) → Inconclusive so
// a transient failure never closes anything.
//
// The Career-Page dormancy probe uses it directly (a reachable page is Alive). The
// per-listing refetch calls it only on the error path — a 200 there is decided by
// the source_hash compare, not here — so the nil→Alive branch is exercised only by
// the page probe.
func classifyStatus(err error) crawler.ProbeOutcome {
	if err == nil {
		return crawler.ProbeAlive
	}
	var se *downloader.StatusError
	if errors.As(err, &se) && (se.StatusCode == http.StatusNotFound || se.StatusCode == http.StatusGone) {
		return crawler.ProbeDead
	}
	return crawler.ProbeInconclusive
}
