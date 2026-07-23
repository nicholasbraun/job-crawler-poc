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
// The per-listing refetch calls it only on the error path — a 200 there is decided
// by the source_hash compare, not here. The Career-Page dormancy probe reaches it
// via classifyPageProbe, which reuses the nil→Alive/404→Dead reach mapping but then
// overrides a reachable page to Dead when it no longer classifies as a careers page.
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

// classifyPageProbe maps one Career-Page dormancy probe to a ProbeOutcome (ADR-0035),
// folding page reach AND re-classification into a single hard-dead test. Whole-page
// death — the outcome the dormancy cascade exists to close — is a 404/410 board OR a
// reachable 200 that no longer classifies as a careers page (a redesign into a
// marketing/landing page that lists no openings); both are Dead and accrue dormancy
// identically. This is the pure seam: the impure GET and LLM re-classification live
// in RefetchProcessor.probePage, which passes their results here.
//
//   - getErr != nil → classifyStatus(getErr): 404/410 Dead, everything else
//     Inconclusive. The page was never reached, so the classification args are ignored.
//   - getErr == nil, classifyErr != nil → Inconclusive: the page reached 200 but no
//     classification signal was obtained (a parse or LLM failure). Do-least-harm, so a
//     classify blip never counts toward dormancy — exactly like a transient GET.
//   - getErr == nil, stillCareerPage → Alive: reachable and still a careers page;
//     resets the dormancy counter.
//   - getErr == nil, !stillCareerPage → Dead: reachable but no longer a careers page.
func classifyPageProbe(getErr error, stillCareerPage bool, classifyErr error) crawler.ProbeOutcome {
	if getErr != nil {
		return classifyStatus(getErr)
	}
	if classifyErr != nil {
		return crawler.ProbeInconclusive
	}
	if stillCareerPage {
		return crawler.ProbeAlive
	}
	return crawler.ProbeDead
}
