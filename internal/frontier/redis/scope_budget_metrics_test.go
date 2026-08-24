package redis_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// scopeTruncatedValue returns the summed crawler.frontier.scope.truncated counter
// value for the given run_id and whether such a series exists. The series exists
// (at 0) as soon as one NEW insert has recorded for that run, so its presence
// proves the counter is wired even when nothing truncated.
func scopeTruncatedValue(t *testing.T, rm *metricdata.ResourceMetrics, runID string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.scope.truncated" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("scope.truncated: unexpected data type %T", m.Data)
			}
			var total int64
			found := false
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("run_id"); ok && v.AsString() == runID {
					total += dp.Value
					found = true
				}
			}
			if found {
				return total, true
			}
		}
	}
	return 0, false
}

// scopeBudgetValue returns the crawler.frontier.scope.budget gauge value for the
// given run_id and whether such a series exists. A gauge is last-value; the
// budget is static per run, so every NEW insert records the same number.
func scopeBudgetValue(t *testing.T, rm *metricdata.ResourceMetrics, runID string) (int64, bool) {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		if sm.Scope.Name != "frontier" {
			continue
		}
		for _, m := range sm.Metrics {
			if m.Name != "crawler.frontier.scope.budget" {
				continue
			}
			g, ok := m.Data.(metricdata.Gauge[int64])
			if !ok {
				t.Fatalf("scope.budget: unexpected data type %T", m.Data)
			}
			for _, dp := range g.DataPoints {
				if v, ok := dp.Attributes.Value("run_id"); ok && v.AsString() == runID {
					return dp.Value, true
				}
			}
		}
	}
	return 0, false
}

// captureLogs installs a JSON slog handler writing into buf for the duration of
// fn, then restores the previous default logger. Mirrors url_processor's helper.
func captureLogs(t *testing.T, buf *bytes.Buffer, fn func()) {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	fn()
}

// warnScopeNames returns the "scope" attribute of every WARN line in buf, so a
// test can assert a Scope is named exactly once.
func warnScopeNames(t *testing.T, buf *bytes.Buffer) []string {
	t.Helper()
	names := []string{}
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("could not parse log line %q: %v", line, err)
		}
		if entry["level"] != "WARN" {
			continue
		}
		scope, _ := entry["scope"].(string)
		names = append(names, scope)
	}
	return names
}

// TestScopeBudgetMetrics drives the Frontier through its public API against a
// real testcontainer Redis and asserts the two Scope Budget instruments and the
// truncation WARN log through an OTEL SDK ManualReader (never Lua shape or key
// names). Non-parallel because the manual reader is the process-global meter
// provider and instruments bind at New; the reader is installed once and every
// Frontier is built after it, with subtests isolated by distinct run_id.
func TestScopeBudgetMetrics(t *testing.T) {
	reader := installManualReader(t)
	client := newTestClient(t)

	t.Run("a run under its budget records zero truncations", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(5))
		if err := f.AddURL(t.Context(), scopedURL("acme.com", "http://acme.com/1", "acme.com", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		// The run_id series exists at 0, proving the counter is wired and nothing
		// truncated for this run.
		if v, ok := scopeTruncatedValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 0 {
			t.Errorf("scope.truncated: got %d (ok=%v), want 0", v, ok)
		}
	})

	t.Run("the counter fires once per Scope, not once per rejected URL", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(2))

		for i := 0; i < 2; i++ {
			raw := "http://acme.com/" + strconv.Itoa(i)
			if err := f.AddURL(t.Context(), scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}
		// Six rejected URLs must not move the counter: it counts truncated Scopes,
		// not dropped links.
		for i := 0; i < 6; i++ {
			raw := "http://acme.com/deep" + strconv.Itoa(i)
			if err := f.AddURL(t.Context(), scopedURL("acme.com", raw, "acme.com", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
				t.Fatalf("AddURL %s err = %v, want ErrScopeBudget", raw, err)
			}
		}
		if v, ok := scopeTruncatedValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 1 {
			t.Errorf("scope.truncated after one truncated Scope: got %d (ok=%v), want 1", v, ok)
		}

		// A second Scope dropping a link in the same run is the second increment.
		for i := 0; i < 2; i++ {
			raw := "http://beta.example/" + strconv.Itoa(i)
			if err := f.AddURL(t.Context(), scopedURL("beta.example", raw, "beta.example", 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}
		// Spending the budget is not yet Scope Truncation: this first DROPPED link is.
		if err := f.AddURL(t.Context(), scopedURL("beta.example", "http://beta.example/deep", "beta.example", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
			t.Fatalf("AddURL beta.example/deep err = %v, want ErrScopeBudget", err)
		}
		if v, ok := scopeTruncatedValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 2 {
			t.Errorf("scope.truncated after two truncated Scopes: got %d (ok=%v), want 2", v, ok)
		}
	})

	t.Run("the budget gauge reflects the configured per-run budget", func(t *testing.T) {
		// A non-default number proves the gauge tracks the effective per-run value a
		// dashboard would otherwise hard-code.
		runID := uuid.New()
		const wantBudget = 1234
		f := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(wantBudget))
		if err := f.AddURL(t.Context(), scopedURL("acme.com", "http://acme.com/1", "acme.com", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		if v, ok := scopeBudgetValue(t, collectFrontier(t, reader), runID.String()); !ok || v != wantBudget {
			t.Errorf("scope.budget: got %d (ok=%v), want %d", v, ok, wantBudget)
		}
	})

	t.Run("the truncated Scope is named at WARN exactly once", func(t *testing.T) {
		runID := uuid.New()
		f := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(1))
		admitted := scopedURL("beck.de", "http://beck.de/1", "beck.de", 0)

		var buf bytes.Buffer
		captureLogs(t, &buf, func() {
			// The admission that spends the budget announces nothing; the FIRST rejected
			// link below is the transition.
			if err := f.AddURL(t.Context(), admitted); err != nil {
				t.Fatalf("AddURL: %v", err)
			}
			for i := 0; i < 5; i++ {
				raw := "http://beck.de/deep" + strconv.Itoa(i)
				if err := f.AddURL(t.Context(), scopedURL("beck.de", raw, "beck.de", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
					t.Fatalf("AddURL %s err = %v, want ErrScopeBudget", raw, err)
				}
			}
			// A re-see of the already-admitted URL is rejected by the gate too (it runs
			// before dedup), and is likewise not a second transition.
			if err := f.AddURL(t.Context(), admitted); !errors.Is(err, frontier.ErrScopeBudget) {
				t.Fatalf("AddURL repeat err = %v, want ErrScopeBudget", err)
			}
		})

		got := warnScopeNames(t, &buf)
		if len(got) != 1 || got[0] != "beck.de" {
			t.Errorf("WARN scopes = %v, want exactly [beck.de]; logs:\n%s", got, buf.String())
		}
	})

	t.Run("a Scope that spends its budget without dropping a link is not announced", func(t *testing.T) {
		// Scope Truncation is a walk stopping SHORT (CONTEXT.md). A Scope whose budget
		// runs out on the very last link it ever offers dropped nothing, so naming it
		// would dilute the operator's read of which Companies actually paid the price.
		runID := uuid.New()
		f := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(2))

		var buf bytes.Buffer
		captureLogs(t, &buf, func() {
			for i := 0; i < 2; i++ {
				raw := "http://acme.com/" + strconv.Itoa(i)
				if err := f.AddURL(t.Context(), scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
					t.Fatalf("AddURL %s: %v", raw, err)
				}
			}
		})

		if got := warnScopeNames(t, &buf); len(got) != 0 {
			t.Errorf("WARN scopes = %v, want none; logs:\n%s", got, buf.String())
		}
		// The series still exists at 0 (a NEW insert recorded it), so "nothing
		// truncated" is observable rather than merely absent.
		if v, ok := scopeTruncatedValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 0 {
			t.Errorf("scope.truncated: got %d (ok=%v), want 0", v, ok)
		}
	})

	t.Run("a shrunken budget across a resume still names the truncated Scope once", func(t *testing.T) {
		// The mid-Cycle restart whose Catalog grew, so the re-derived per-Scope number
		// is SMALLER than what this Scope already spent. The charge path is unreachable
		// for it from here on (the gate rejects at spent >= budget), so an announcement
		// keyed on "the charge landed on the budget" could never fire again and the
		// truncation would be silent. Keyed on the Scope, the first dropped link names it.
		runID := uuid.New()

		var buf bytes.Buffer
		captureLogs(t, &buf, func() {
			f1 := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(4))
			for i := 0; i < 3; i++ {
				raw := "http://beck.de/" + strconv.Itoa(i)
				if err := f1.AddURL(t.Context(), scopedURL("beck.de", raw, "beck.de", 0)); err != nil {
					t.Fatalf("AddURL %s: %v", raw, err)
				}
			}

			f2 := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(2))
			for i := 0; i < 3; i++ {
				raw := "http://beck.de/resumed" + strconv.Itoa(i)
				if err := f2.AddURL(t.Context(), scopedURL("beck.de", raw, "beck.de", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
					t.Fatalf("AddURL %s err = %v, want ErrScopeBudget", raw, err)
				}
			}
		})

		got := warnScopeNames(t, &buf)
		if len(got) != 1 || got[0] != "beck.de" {
			t.Errorf("WARN scopes = %v, want exactly [beck.de]; logs:\n%s", got, buf.String())
		}
		if v, ok := scopeTruncatedValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 1 {
			t.Errorf("scope.truncated: got %d (ok=%v), want 1", v, ok)
		}
	})

	t.Run("a grown budget across a resume does not name the Scope twice", func(t *testing.T) {
		// The mirror case: the re-derived number is LARGER, so the spend passes through
		// it a second time. An announcement keyed on that arithmetic fires twice for one
		// Scope; the per-(run, Scope) marker fires once.
		runID := uuid.New()

		var buf bytes.Buffer
		captureLogs(t, &buf, func() {
			f1 := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(2))
			for i := 0; i < 2; i++ {
				raw := "http://acme.com/" + strconv.Itoa(i)
				if err := f1.AddURL(t.Context(), scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
					t.Fatalf("AddURL %s: %v", raw, err)
				}
			}

			f2 := redisfrontier.New(client, runID, redisfrontier.WithScopeBudget(4))
			for i := 0; i < 2; i++ {
				raw := "http://acme.com/resumed" + strconv.Itoa(i)
				if err := f2.AddURL(t.Context(), scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
					t.Fatalf("AddURL %s: %v", raw, err)
				}
			}
			if err := f2.AddURL(t.Context(), scopedURL("acme.com", "http://acme.com/dropped", "acme.com", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
				t.Fatalf("AddURL acme.com/dropped err = %v, want ErrScopeBudget", err)
			}
		})

		got := warnScopeNames(t, &buf)
		if len(got) != 1 || got[0] != "acme.com" {
			t.Errorf("WARN scopes = %v, want exactly [acme.com]; logs:\n%s", got, buf.String())
		}
		if v, ok := scopeTruncatedValue(t, collectFrontier(t, reader), runID.String()); !ok || v != 1 {
			t.Errorf("scope.truncated: got %d (ok=%v), want 1", v, ok)
		}
	})
}
