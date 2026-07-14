package api

import (
	"testing"
	"time"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

// utcDay is the UTC midnight of the given calendar day, for building hermetic
// fixtures without leaning on time.Now.
func utcDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func sum(counts []crawler.DayCount) int {
	total := 0
	for _, c := range counts {
		total += c.Count
	}
	return total
}

func TestCatalogSparkline(t *testing.T) {
	t.Run("empty catalog yields an empty, non-nil series", func(t *testing.T) {
		got := catalogSparkline(nil, utcDay(2026, 1, 10), 90)
		if got == nil {
			t.Fatal("want a non-nil empty slice, got nil")
		}
		if len(got) != 0 {
			t.Errorf("want an empty series, got %v", got)
		}
	})

	t.Run("a single day is a single cumulative point", func(t *testing.T) {
		counts := []crawler.DayCount{{Day: utcDay(2026, 1, 10), Count: 3}}
		got := catalogSparkline(counts, utcDay(2026, 1, 10), 90)
		want := []int{3}
		if !equalInts(got, want) {
			t.Errorf("want %v, got %v", want, got)
		}
	})

	t.Run("a gap day carries the running total forward as a plateau", func(t *testing.T) {
		// Jan 10: +2, Jan 11: nothing (gap), Jan 12: +3. now = Jan 12.
		counts := []crawler.DayCount{
			{Day: utcDay(2026, 1, 10), Count: 2},
			{Day: utcDay(2026, 1, 12), Count: 3},
		}
		got := catalogSparkline(counts, utcDay(2026, 1, 12), 90)
		want := []int{2, 2, 5}
		if !equalInts(got, want) {
			t.Errorf("want %v (plateau across the empty Jan 11), got %v", want, got)
		}
	})

	t.Run("the series extends to today as a flat trailing plateau", func(t *testing.T) {
		// Last catalogued Jan 10; now is Jan 13, so the total holds flat to today.
		counts := []crawler.DayCount{{Day: utcDay(2026, 1, 10), Count: 4}}
		got := catalogSparkline(counts, utcDay(2026, 1, 13), 90)
		want := []int{4, 4, 4, 4}
		if !equalInts(got, want) {
			t.Errorf("want %v (flat to today), got %v", want, got)
		}
	})

	t.Run("the endpoint always equals the cumulative total", func(t *testing.T) {
		cases := [][]crawler.DayCount{
			{{Day: utcDay(2026, 1, 10), Count: 1}},
			{{Day: utcDay(2026, 1, 10), Count: 2}, {Day: utcDay(2026, 1, 15), Count: 5}},
			{{Day: utcDay(2025, 12, 1), Count: 7}, {Day: utcDay(2026, 1, 20), Count: 3}},
		}
		now := utcDay(2026, 2, 1)
		for _, counts := range cases {
			got := catalogSparkline(counts, now, 90)
			if len(got) == 0 {
				t.Fatalf("non-empty counts %v produced an empty series", counts)
			}
			if last := got[len(got)-1]; last != sum(counts) {
				t.Errorf("endpoint %d != cumulative total %d for %v", last, sum(counts), counts)
			}
		}
	})

	t.Run("downsampling caps the point count, keeps it monotonic, and pins the total", func(t *testing.T) {
		// 10 consecutive days, +1 each → full daily series 1..10.
		counts := []crawler.DayCount{}
		for i := 0; i < 10; i++ {
			counts = append(counts, crawler.DayCount{Day: utcDay(2026, 1, 10+i), Count: 1})
		}
		const maxPoints = 3
		got := catalogSparkline(counts, utcDay(2026, 1, 19), maxPoints)

		// stride ⌈10/3⌉ = 4 → samples idx 0,4,8 (values 1,5,9) plus the pinned
		// last (10). The pin can push the count to at most maxPoints+1.
		if len(got) > maxPoints+1 {
			t.Errorf("downsampled series has %d points, want <= %d", len(got), maxPoints+1)
		}
		if got[0] != 1 {
			t.Errorf("first sampled point should be the series start 1, got %d", got[0])
		}
		if last := got[len(got)-1]; last != 10 {
			t.Errorf("pinned endpoint should be the total 10, got %d", last)
		}
		for i := 1; i < len(got); i++ {
			if got[i] < got[i-1] {
				t.Errorf("cumulative series must be non-decreasing, got %v", got)
			}
		}
	})

	t.Run("a series at exactly the cap is returned unsampled", func(t *testing.T) {
		counts := []crawler.DayCount{}
		for i := 0; i < 5; i++ {
			counts = append(counts, crawler.DayCount{Day: utcDay(2026, 1, 10+i), Count: 2})
		}
		got := catalogSparkline(counts, utcDay(2026, 1, 14), 5)
		want := []int{2, 4, 6, 8, 10}
		if !equalInts(got, want) {
			t.Errorf("want the full %v when len == maxPoints, got %v", want, got)
		}
	})

	t.Run("unsorted input is handled by day, not by position", func(t *testing.T) {
		counts := []crawler.DayCount{
			{Day: utcDay(2026, 1, 12), Count: 3},
			{Day: utcDay(2026, 1, 10), Count: 2},
		}
		got := catalogSparkline(counts, utcDay(2026, 1, 12), 90)
		want := []int{2, 2, 5}
		if !equalInts(got, want) {
			t.Errorf("want %v regardless of input order, got %v", want, got)
		}
	})

	t.Run("non-midnight day timestamps are bucketed to their UTC day", func(t *testing.T) {
		counts := []crawler.DayCount{
			{Day: time.Date(2026, 1, 10, 8, 30, 0, 0, time.UTC), Count: 1},
			{Day: time.Date(2026, 1, 11, 23, 15, 0, 0, time.UTC), Count: 4},
		}
		got := catalogSparkline(counts, time.Date(2026, 1, 11, 12, 0, 0, 0, time.UTC), 90)
		want := []int{1, 5}
		if !equalInts(got, want) {
			t.Errorf("want %v with sub-day timestamps truncated, got %v", want, got)
		}
	})
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
