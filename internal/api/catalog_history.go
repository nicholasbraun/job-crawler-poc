package api

import (
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// catalogSparkline reconstructs the Catalog History growth curve from raw
// per-day first-seen counts (see crawler.DayCount): it gap-fills every calendar
// day from the earliest count through now, cumulatively sums so the series is
// monotonic, and downsamples to at most maxPoints+1 points for a fixed-width
// sparkline. It is deliberately pure — now is injected, not read from the clock,
// so the off-by-one-prone bucketing logic is unit-testable without a database.
//
// Gap days carry the running total forward (a flat plateau) rather than being
// skipped: the frontend renders the returned []int on an index-based x-axis, so
// dropping empty days would distort the curve's shape. The final point always
// equals the sum of counts, keeping the sparkline's endpoint in lockstep with
// the headline "N career pages catalogued" count. An empty catalog yields an
// empty, non-nil slice.
func catalogSparkline(counts []crawler.DayCount, now time.Time, maxPoints int) []int {
	if len(counts) == 0 {
		return []int{}
	}

	// Bucket by UTC day, defensively summing any duplicate days and tracking the
	// span so the walk below is independent of the input's order.
	byDay := make(map[time.Time]int, len(counts))
	first := startOfUTCDay(counts[0].Day)
	last := first
	for _, c := range counts {
		day := startOfUTCDay(c.Day)
		byDay[day] += c.Count
		if day.Before(first) {
			first = day
		}
		if day.After(last) {
			last = day
		}
	}

	// Extend the walk to today so the curve holds flat up to the present. Guard
	// against an injected now that predates the last count so the endpoint still
	// covers every bucket (and thus equals the full total).
	end := startOfUTCDay(now)
	if last.After(end) {
		end = last
	}

	series := []int{}
	running := 0
	for day := first; !day.After(end); day = day.AddDate(0, 0, 1) {
		running += byDay[day]
		series = append(series, running)
	}

	return downsample(series, maxPoints)
}

// downsample thins series to at most maxPoints stride-sampled points, always
// appending the true last point (the cumulative total) when the stride skips
// over it. The pin can make the result up to maxPoints+1 long; for a decorative
// fixed-width sparkline that one extra point is immaterial, and pinning keeps
// the endpoint equal to the headline count. Monotonicity is preserved because
// sampling a non-decreasing series in index order stays non-decreasing.
func downsample(series []int, maxPoints int) []int {
	if maxPoints < 1 || len(series) <= maxPoints {
		return series
	}

	stride := (len(series) + maxPoints - 1) / maxPoints // ⌈n/maxPoints⌉
	out := []int{}
	for i := 0; i < len(series); i += stride {
		out = append(out, series[i])
	}
	if lastSampled := ((len(series) - 1) / stride) * stride; lastSampled != len(series)-1 {
		out = append(out, series[len(series)-1])
	}
	return out
}

// startOfUTCDay truncates t to UTC midnight, so every day key and the day-walk
// share one fixed boundary regardless of t's original location.
func startOfUTCDay(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
