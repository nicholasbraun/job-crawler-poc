package crawler

// DefaultCrawlStaleThreshold is the number of consecutive Inconclusive crawl-lane
// refetch probes (5xx / timeout / soft-404) tolerated before the staleness backstop
// closes a Job Listing (ADR-0035): high enough that a transient provider blip never
// false-closes a live role. The collection engine may override it via config.
const DefaultCrawlStaleThreshold = 3

// ProbeOutcome is the classified result of one Liveness probe of a Job Listing
// (ADR-0035), lane-independent. The ATS lane derives it from board presence
// (present -> Alive; absent on a complete fetch -> Dead); the crawl lane from a
// direct refetch (200 + matching source_hash -> Alive; 404/410 -> Dead;
// 5xx/timeout/soft-404 -> Inconclusive). Classification from raw HTTP/board signals
// lives in the per-lane collectors; this type is the reducer's input. The zero value
// is Inconclusive: the do-least-harm outcome, so an unset probe never closes.
type ProbeOutcome int

const (
	ProbeInconclusive ProbeOutcome = iota
	ProbeAlive
	ProbeDead
)

// LifecycleState is a Job Listing's derived liveness state as the reducer sees it:
// whether it is currently Open and, for the crawl lane, how many consecutive
// Inconclusive probes it has accrued (the staleness counter; the ATS lane leaves it
// zero). Persisted as (job_listing.closed_at IS NULL) and job_listing.inconclusive_streak.
type LifecycleState struct {
	Open               bool
	InconclusiveStreak int
}

// NextLiveness is the pure Listing-Liveness reducer (ADR-0035) — the load-bearing
// correctness of the pipeline, isolated at the highest test seam. It decides a
// listing's next LifecycleState from its current state, the latest ProbeOutcome,
// whether the observation was complete (boardComplete — the ATS interlock; a direct
// crawl refetch always passes true), and staleThreshold (consecutive Inconclusive
// probes tolerated before the crawl-lane staleness backstop closes). It never
// deletes; a closed listing reopens on the next Alive, and reopen is decided
// elsewhere by preserving first_seen.
func NextLiveness(current LifecycleState, outcome ProbeOutcome, boardComplete bool, staleThreshold int) LifecycleState {
	switch outcome {
	case ProbeAlive:
		// Confirmed present/live: (re)open and clear the staleness counter.
		return LifecycleState{Open: true, InconclusiveStreak: 0}
	case ProbeDead:
		if !boardComplete {
			// ATS absence on an incomplete/failed fetch is NOT authoritative — a
			// partial board must never close anything. (Crawl 404/410 passes
			// boardComplete=true, so it still closes.)
			return current
		}
		return LifecycleState{Open: false, InconclusiveStreak: 0}
	case ProbeInconclusive:
		streak := current.InconclusiveStreak + 1
		if streak >= staleThreshold {
			// Persistently-inconclusive tail: close. Keep the accrued streak so the
			// close reason stays legible.
			return LifecycleState{Open: false, InconclusiveStreak: streak}
		}
		return LifecycleState{Open: current.Open, InconclusiveStreak: streak}
	default:
		return current
	}
}
