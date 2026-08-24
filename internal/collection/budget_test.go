package collection_test

import (
	"fmt"
	"testing"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
)

// scopeSeeds builds n crawl Seeds with n distinct Scopes — the shape RouteSeeds hands
// a Cycle, one Scope per Company.
func scopeSeeds(n int) []crawler.Seed {
	seeds := []crawler.Seed{}
	for i := range n {
		scope := fmt.Sprintf("company-%d.example", i)
		seeds = append(seeds, crawler.Seed{URL: "https://" + scope + "/jobs", Scope: scope, Owner: scope})
	}
	return seeds
}

// TestDeriveScopeBudget pins ADR-0053's derivation over its whole range: a typical
// Catalog landing between the bounds, the clamp down to the ceiling and up to the
// floor, the exact Scope count where the floor starts overriding the division (the
// two neighbouring rows across which HoldsCeiling must flip), Seeds sharing one Scope
// counting once, the empty and wholly unscoped Seed sets that disable the budget
// rather than divide by zero, and a disabled seen-memory ceiling. The literal
// expectations are what pin the three package constants; every row also re-asserts
// the ADR's own claim — a budget that holds the ceiling fits under the headroom share.
func TestDeriveScopeBudget(t *testing.T) {
	tests := []struct {
		name       string
		visitedCap int
		seeds      []crawler.Seed
		want       collection.ScopeBudget
	}{
		{
			name:       "a typical Catalog lands between the floor and the ceiling",
			visitedCap: 5_000_000,
			seeds:      scopeSeeds(1100),
			want:       collection.ScopeBudget{URLsPerScope: 3636, Scopes: 1100, HoldsCeiling: true},
		},
		{
			name:       "a mid-sized Catalog divides exactly",
			visitedCap: 5_000_000,
			seeds:      scopeSeeds(4000),
			want:       collection.ScopeBudget{URLsPerScope: 1000, Scopes: 4000, HoldsCeiling: true},
		},
		{
			name:       "few Scopes are clamped down to the ceiling",
			visitedCap: 5_000_000,
			seeds:      scopeSeeds(100),
			// The division alone would hand out 40,000.
			want: collection.ScopeBudget{URLsPerScope: 10_000, Scopes: 100, HoldsCeiling: true},
		},
		{
			name:       "many Scopes are clamped up to the floor and the guarantee lapses",
			visitedCap: 5_000_000,
			seeds:      scopeSeeds(20_000),
			// The division alone would hand out 200 — ADR-0053's 20,000-Scope row.
			want: collection.ScopeBudget{URLsPerScope: 500, Scopes: 20_000, HoldsCeiling: false, RequiredVisitedCap: 12_500_000},
		},
		{
			name:       "the crossover still holds exactly at the floor",
			visitedCap: 5_000_000,
			seeds:      scopeSeeds(8000),
			// 500 × 8,000 == 4,000,000, the whole headroom share and not a URL more.
			want: collection.ScopeBudget{URLsPerScope: 500, Scopes: 8000, HoldsCeiling: true},
		},
		{
			name:       "one Scope past the crossover the floor overrides the division",
			visitedCap: 5_000_000,
			seeds:      scopeSeeds(8001),
			want:       collection.ScopeBudget{URLsPerScope: 500, Scopes: 8001, HoldsCeiling: false, RequiredVisitedCap: 5_000_625},
		},
		{
			name: "duplicate Scopes across Seeds count once",
			// A deliberately small ceiling: counting the four Seeds instead of the two
			// Scopes would yield 2,000 rather than 4,000 and fail loudly, where a 5M
			// ceiling would clamp both counts to 10,000 and prove nothing.
			visitedCap: 10_000,
			seeds: []crawler.Seed{
				{URL: "https://acme.com/jobs", Scope: "acme.com", Owner: "acme.com"},
				{URL: "https://careers.acme.com/openings", Scope: "acme.com", Owner: "acme.com"},
				{URL: "https://beta.com/jobs", Scope: "beta.com", Owner: "beta.com"},
				{URL: "https://jobs.beta.com/", Scope: "beta.com", Owner: "beta.com"},
			},
			want: collection.ScopeBudget{URLsPerScope: 4000, Scopes: 2, HoldsCeiling: true},
		},
		{
			name:       "an empty Seed set disables the budget instead of dividing by zero",
			visitedCap: 5_000_000,
			seeds:      nil,
			// HoldsCeiling stays true: nothing to bound is nothing to warn about.
			want: collection.ScopeBudget{URLsPerScope: 0, Scopes: 0, HoldsCeiling: true},
		},
		{
			name:       "Seeds carrying no Scope are not counted, so a roaming Seed set disables the budget",
			visitedCap: 5_000_000,
			seeds: []crawler.Seed{
				{URL: "https://example.com"},
				{URL: "https://other.example/jobs"},
			},
			want: collection.ScopeBudget{URLsPerScope: 0, Scopes: 0, HoldsCeiling: true},
		},
		{
			name:       "an unscoped Seed does not dilute a scoped one",
			visitedCap: 10_000,
			seeds: []crawler.Seed{
				{URL: "https://acme.com/jobs", Scope: "acme.com", Owner: "acme.com"},
				{URL: "https://example.com"},
			},
			want: collection.ScopeBudget{URLsPerScope: 8000, Scopes: 1, HoldsCeiling: true},
		},
		{
			name:       "a disabled seen-memory ceiling yields no derivable budget and names the ceiling that would restore it",
			visitedCap: 0,
			seeds:      scopeSeeds(1100),
			// 1,100 × 625, ADR-0053's scopes × floor ÷ 0.8.
			want: collection.ScopeBudget{URLsPerScope: 0, Scopes: 1100, HoldsCeiling: false, RequiredVisitedCap: 687_500},
		},
		{
			name:       "a negative seen-memory ceiling behaves the same",
			visitedCap: -1,
			seeds:      scopeSeeds(1100),
			want:       collection.ScopeBudget{URLsPerScope: 0, Scopes: 1100, HoldsCeiling: false, RequiredVisitedCap: 687_500},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collection.DeriveScopeBudget(tt.visitedCap, tt.seeds)
			if got != tt.want {
				t.Errorf("DeriveScopeBudget(%d, %d seeds) = %+v, want %+v",
					tt.visitedCap, len(tt.seeds), got, tt.want)
			}

			// The whole point of the derivation (ADR-0053): whenever it claims to
			// hold the ceiling, the sum of all Scope Budgets fits under the headroom
			// share, so the Cycle's seen-memory never has to forget. Restated here
			// from the ADR rather than read back off the package constants.
			if got.HoldsCeiling {
				share := int(float64(max(tt.visitedCap, 0)) * 0.8)
				if got.URLsPerScope*got.Scopes > share {
					t.Errorf("claims to hold the ceiling but %d Scopes × %d URLs = %d exceeds the headroom share %d",
						got.Scopes, got.URLsPerScope, got.URLsPerScope*got.Scopes, share)
				}
			}
		})
	}
}
