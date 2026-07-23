package crawler

import "time"

// DefaultPageDormancyThreshold is the number of consecutive hard-dead Cycles a
// Career Page tolerates before it goes dormant (ADR-0035): drops out of the seed
// set and Closes its remaining Open Job Listings. It is deliberately higher than
// the per-listing DefaultCrawlStaleThreshold (3) because a dormant page's blast
// radius is a whole board, not one posting, so it must absorb more transient
// blips before acting. The collection engine may override it via config.
const DefaultPageDormancyThreshold = 5

// NextDormancy folds one Career-Page reach probe into the dormancy counters
// (ADR-0035), the pure reducer mirroring NextLiveness one level up. It reuses
// ProbeOutcome from liveness.go: Dead (a 404 board or a page that no longer
// reaches) increments the consecutive-failure count and leaves lastOK unchanged;
// Alive resets the count to 0 and stamps lastOK at now; Inconclusive (a transient
// 5xx/timeout) changes nothing, so a blip never counts toward dormancy. Dormancy
// itself is derived from the returned count via Dormant, never stored.
func NextDormancy(failures int, lastOK time.Time, outcome ProbeOutcome, now time.Time) (nextFailures int, nextLastOK time.Time) {
	switch outcome {
	case ProbeAlive:
		return 0, now
	case ProbeDead:
		return failures + 1, lastOK
	default: // ProbeInconclusive: transient, never counts.
		return failures, lastOK
	}
}

// Dormant reports whether a Career Page with failures consecutive hard-dead
// probes is dormant at the given threshold (ADR-0035). Dormancy is derived from
// the counter, never a stored flag, so a threshold change re-derives it for free.
func Dormant(failures, threshold int) bool {
	return failures >= threshold
}
