package collection_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
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

// captureLogs installs a JSON slog handler writing into buf for the duration of fn,
// then restores the previous default logger. Mirrors the frontier package's helper.
func captureLogs(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
}

// logLines parses buf as one JSON object per line, so a test can assert an
// announcement's level and attributes without matching message text.
func logLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	lines := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("could not parse log line %q: %v", line, err)
		}
		lines = append(lines, entry)
	}
	return lines
}

// TestScopeBudgetAnnounce pins the Cycle-start announcement of ADR-0053: one line per
// Cycle start, at the level that matches what the derivation returned, carrying the
// numbers an operator has to act on. "Exactly one line" is a real assertion rather than
// decoration -- a start that both warned and informed would make the record of which
// Cycles ran with a lapsed guarantee ambiguous. The attribute KEYS are load-bearing:
// they are what a log search for a lapsed guarantee matches on.
func TestScopeBudgetAnnounce(t *testing.T) {
	runID := uuid.New()

	tests := []struct {
		name      string
		budget    collection.ScopeBudget
		wantLevel string
		wantAttrs map[string]float64
		// absentAttrs must not appear at all: a healthy Cycle that named a
		// required_visited_cap would be reporting a fix for a problem it does not have.
		absentAttrs []string
	}{
		{
			name:      "a derived budget that holds the ceiling is announced at INFO",
			budget:    collection.ScopeBudget{URLsPerScope: 3636, Scopes: 1100, HoldsCeiling: true},
			wantLevel: "INFO",
			wantAttrs: map[string]float64{"scopes": 1100, "budget": 3636},
			// A healthy Cycle names no fix.
			absentAttrs: []string{"required_visited_cap"},
		},
		{
			name:      "a lapsed guarantee is announced at WARN naming the ceiling that would restore it",
			budget:    collection.ScopeBudget{URLsPerScope: 500, Scopes: 20_000, HoldsCeiling: false, RequiredVisitedCap: 12_500_000},
			wantLevel: "WARN",
			wantAttrs: map[string]float64{"scopes": 20_000, "budget": 500, "required_visited_cap": 12_500_000},
		},
		{
			name:      "a Cycle with no scoped Seeds says no budget applies, and does not warn",
			budget:    collection.ScopeBudget{HoldsCeiling: true},
			wantLevel: "INFO",
			wantAttrs: map[string]float64{"scopes": 0},
		},
		{
			name: "a Cycle with Scopes but no derivable ceiling warns",
			// URLsPerScope 0 AND HoldsCeiling false: the switch must read the lapse,
			// not the benign "nothing to bound".
			budget:    collection.ScopeBudget{URLsPerScope: 0, Scopes: 1100, HoldsCeiling: false, RequiredVisitedCap: 687_500},
			wantLevel: "WARN",
			wantAttrs: map[string]float64{"scopes": 1100, "budget": 0, "required_visited_cap": 687_500},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			captureLogs(t, &buf, func() { tt.budget.Announce(runID) })

			lines := logLines(t, &buf)
			if len(lines) != 1 {
				t.Fatalf("Announce wrote %d lines, want exactly 1; logs:\n%s", len(lines), buf.String())
			}
			got := lines[0]
			if got["level"] != tt.wantLevel {
				t.Errorf("level = %v, want %s; logs:\n%s", got["level"], tt.wantLevel, buf.String())
			}
			if got["run_id"] != runID.String() {
				t.Errorf("run_id = %v, want %s", got["run_id"], runID)
			}
			for key, want := range tt.wantAttrs {
				v, ok := got[key].(float64)
				if !ok {
					t.Errorf("attribute %q missing or not a number: %v; logs:\n%s", key, got[key], buf.String())
					continue
				}
				if v != want {
					t.Errorf("attribute %q = %v, want %v", key, v, want)
				}
			}
			for _, key := range tt.absentAttrs {
				if _, ok := got[key]; ok {
					t.Errorf("attribute %q present but must not be; logs:\n%s", key, buf.String())
				}
			}
		})
	}

	t.Run("the announcement carries the derivation's own numbers", func(t *testing.T) {
		// Derived, never hand-built: ADR-0053's 20,000-Scope row, where the floor
		// overrides the division and the guarantee lapses. This is what stops the WARN
		// from naming a number the derivation would never produce.
		b := collection.DeriveScopeBudget(5_000_000, scopeSeeds(20_000))

		var buf bytes.Buffer
		captureLogs(t, &buf, func() { b.Announce(runID) })

		lines := logLines(t, &buf)
		if len(lines) != 1 {
			t.Fatalf("Announce wrote %d lines, want exactly 1; logs:\n%s", len(lines), buf.String())
		}
		got := lines[0]
		if got["level"] != "WARN" {
			t.Errorf("level = %v, want WARN; logs:\n%s", got["level"], buf.String())
		}
		if got["budget"] != float64(500) {
			t.Errorf("budget = %v, want 500", got["budget"])
		}
		if got["required_visited_cap"] != float64(12_500_000) {
			t.Errorf("required_visited_cap = %v, want 12500000", got["required_visited_cap"])
		}
	})
}
