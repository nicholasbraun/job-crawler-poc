package redis_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/redis/go-redis/v9"
)

// scopedURL builds a URL carrying ADR-0021 provenance, which is what the Scope
// Budget keys on. The package's url() helper leaves Scope empty (the Discovery
// shape), so budget tests need their own.
func scopedURL(host, raw, scope string, depth int) crawler.URL {
	return crawler.URL{Hostname: host, RawURL: raw, Scope: scope, Depth: depth}
}

// spend reads a Scope's charged-admission count out of the run's Scope Budget
// hash, returning 0 when the Scope — or the hash itself — has none.
func spend(t *testing.T, client *redis.Client, prefix, scope string) int64 {
	t.Helper()
	v, err := client.HGet(t.Context(), prefix+"scope_spend", scope).Result()
	if errors.Is(err, redis.Nil) {
		return 0
	}
	if err != nil {
		t.Fatalf("reading scope spend for %q: %v", scope, err)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t.Fatalf("parsing scope spend %q: %v", v, err)
	}
	return n
}

// TestScopeBudget covers the per-Scope admission budget the Frontier enforces
// inside its atomic add script (ADR-0053): the sentinel rejection, per-Scope
// independence, the mutate-nothing ordering invariant, the Discovery lane's
// inertness, what is and is not charged, resume, cleanup, and concurrency.
func TestScopeBudget(t *testing.T) {
	client := newTestClient(t)

	t.Run("admissions are permitted up to the budget, then rejected with the sentinel", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(3))
		prefix := "frontier:" + id.String() + ":"

		for i := 0; i < 3; i++ {
			raw := "http://acme.com/" + strconv.Itoa(i)
			if err := f.AddURL(ctx, scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}

		err := f.AddURL(ctx, scopedURL("acme.com", "http://acme.com/4", "acme.com", 0))
		if !errors.Is(err, frontier.ErrScopeBudget) {
			t.Fatalf("AddURL past budget err = %v, want ErrScopeBudget", err)
		}
		// Distinct from the depth rejection, so a caller can tell a depth drop from
		// a Scope Truncation.
		if errors.Is(err, frontier.ErrMaxDepth) {
			t.Errorf("ErrScopeBudget must not match ErrMaxDepth")
		}
		if got := spend(t, client, prefix, "acme.com"); got != 3 {
			t.Errorf("scope spend = %d, want 3", got)
		}
	})

	t.Run("budgets are independent per Scope", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(1))
		prefix := "frontier:" + id.String() + ":"

		if err := f.AddURL(ctx, scopedURL("a.example", "http://a.example/1", "a.example", 0)); err != nil {
			t.Fatalf("AddURL a/1: %v", err)
		}
		if err := f.AddURL(ctx, scopedURL("a.example", "http://a.example/2", "a.example", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
			t.Fatalf("AddURL a/2 err = %v, want ErrScopeBudget", err)
		}

		// b.example is untouched by a.example spending its budget.
		if err := f.AddURL(ctx, scopedURL("b.example", "http://b.example/1", "b.example", 0)); err != nil {
			t.Fatalf("AddURL b/1 after a spent its budget: %v", err)
		}
		if err := f.AddURL(ctx, scopedURL("b.example", "http://b.example/2", "b.example", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
			t.Fatalf("AddURL b/2 err = %v, want ErrScopeBudget", err)
		}
		if got := spend(t, client, prefix, "a.example"); got != 1 {
			t.Errorf("a.example spend = %d, want 1", got)
		}
		if got := spend(t, client, prefix, "b.example"); got != 1 {
			t.Errorf("b.example spend = %d, want 1", got)
		}
	})

	t.Run("a rejected admission mutates nothing", func(t *testing.T) {
		// The load-bearing test of ADR-0053's ordering invariant: the budget gate
		// sits ABOVE the add script's first mutation (ZADD NX visited). Move it below
		// the dedup short-circuit — the natural tidy-up, since that is the script's
		// cheapest exit — and every rejected URL still burns a visited slot, so a trap
		// minting unbounded distinct URLs saturates the ADR-0027 cap anyway, the Cycle
		// starts forgetting, and the ceiling collapses while the budget still looks
		// like it works. Nothing else in the suite catches that.
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(2))
		prefix := "frontier:" + id.String() + ":"

		for i := 0; i < 2; i++ {
			raw := "http://trap.example/ok" + strconv.Itoa(i)
			if err := f.AddURL(ctx, scopedURL("trap.example", raw, "trap.example", 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}

		wantVisited := client.ZCard(ctx, prefix+"visited").Val()
		wantDomains := client.ZCard(ctx, prefix+"domains").Val()
		wantQueue := client.LLen(ctx, prefix+"q:trap.example").Val()
		wantSpend := spend(t, client, prefix, "trap.example")

		// The trap shape: five further, all-distinct, never-before-seen URLs.
		for i := 0; i < 5; i++ {
			raw := "http://trap.example/deep" + strconv.Itoa(i)
			if err := f.AddURL(ctx, scopedURL("trap.example", raw, "trap.example", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
				t.Fatalf("AddURL %s err = %v, want ErrScopeBudget", raw, err)
			}
		}

		if got := client.ZCard(ctx, prefix+"visited").Val(); got != wantVisited {
			t.Errorf("visited ZCard after rejections = %d, want unchanged %d (the gate ran after the visited insert)", got, wantVisited)
		}
		if got := client.ZCard(ctx, prefix+"domains").Val(); got != wantDomains {
			t.Errorf("domains ZCard after rejections = %d, want unchanged %d", got, wantDomains)
		}
		if got := client.LLen(ctx, prefix+"q:trap.example").Val(); got != wantQueue {
			t.Errorf("queue len after rejections = %d, want unchanged %d", got, wantQueue)
		}
		if got := spend(t, client, prefix, "trap.example"); got != wantSpend {
			t.Errorf("scope spend after rejections = %d, want unchanged %d", got, wantSpend)
		}
	})

	t.Run("an empty Scope is never gated", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(1))
		prefix := "frontier:" + id.String() + ":"

		// The Discovery shape: URLs carry no Scope (ADR-0021), so the budget is off
		// by construction rather than by a lane-specific branch.
		for i := 0; i < 20; i++ {
			raw := "http://roam.example/" + strconv.Itoa(i)
			if err := f.AddURL(ctx, url("roam.example", raw, 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}

		if got := client.ZCard(ctx, prefix+"visited").Val(); got != 20 {
			t.Errorf("visited ZCard = %d, want 20", got)
		}
		if got := client.Exists(ctx, prefix+"scope_spend").Val(); got != 0 {
			t.Errorf("scope_spend key exists (n=%d); the gate must be inert, not merely permissive", got)
		}
	})

	t.Run("a budget of zero or less disables the gate", func(t *testing.T) {
		cases := []struct {
			name string
			opts []redisfrontier.Option
		}{
			{name: "default (no option)"},
			{name: "zero", opts: []redisfrontier.Option{redisfrontier.WithScopeBudget(0)}},
			{name: "negative", opts: []redisfrontier.Option{redisfrontier.WithScopeBudget(-1)}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				ctx := t.Context()
				id := uuid.New()
				f := redisfrontier.New(client, id, tc.opts...)
				prefix := "frontier:" + id.String() + ":"

				for i := 0; i < 20; i++ {
					raw := "http://acme.com/" + strconv.Itoa(i)
					if err := f.AddURL(ctx, scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
						t.Fatalf("AddURL %s: %v", raw, err)
					}
				}
				if got := client.ZCard(ctx, prefix+"visited").Val(); got != 20 {
					t.Errorf("visited ZCard = %d, want 20", got)
				}
				if got := client.Exists(ctx, prefix+"scope_spend").Val(); got != 0 {
					t.Errorf("scope_spend key exists (n=%d); a disabled budget must charge nothing", got)
				}
			})
		}
	})

	t.Run("only a genuinely new admission is charged", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(2))
		prefix := "frontier:" + id.String() + ":"
		u1 := scopedURL("acme.com", "http://acme.com/1", "acme.com", 0)

		if err := f.AddURL(ctx, u1); err != nil {
			t.Fatalf("AddURL u1: %v", err)
		}
		// A re-sighting short-circuits at the dedup insert, below the charge.
		if err := f.AddURL(ctx, u1); err != nil {
			t.Fatalf("AddURL u1 repeat: %v", err)
		}
		// Had the DUP charged, this second distinct URL would be rejected.
		if err := f.AddURL(ctx, scopedURL("acme.com", "http://acme.com/2", "acme.com", 0)); err != nil {
			t.Fatalf("AddURL u2: %v", err)
		}
		if got := spend(t, client, prefix, "acme.com"); got != 2 {
			t.Errorf("scope spend = %d, want 2 (the DUP must be free)", got)
		}
		if err := f.AddURL(ctx, scopedURL("acme.com", "http://acme.com/3", "acme.com", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
			t.Errorf("AddURL u3 err = %v, want ErrScopeBudget", err)
		}
	})

	t.Run("queue churn does not charge the budget", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id,
			redisfrontier.WithScopeBudget(5),
			redisfrontier.WithLeaseTTL(300*time.Millisecond),
			redisfrontier.WithPollInterval(50*time.Millisecond),
		)
		prefix := "frontier:" + id.String() + ":"
		want := scopedURL("acme.com", "http://acme.com/1", "acme.com", 0)

		if err := f.AddURL(ctx, want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		if got := spend(t, client, prefix, "acme.com"); got != 1 {
			t.Fatalf("scope spend after add = %d, want 1", got)
		}

		// Take the URL and deliberately never MarkDone, so the lease expires and a
		// later Next reclaims it — pushing the member back onto its queue. Reclaim
		// and push-back live in the pop script, so neither is an admission.
		if _, err := f.Next(ctx); err != nil {
			t.Fatalf("Next: %v", err)
		}
		reclaimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		reclaimed, err := f.Next(reclaimCtx)
		if err != nil {
			t.Fatalf("Next (reclaim): %v", err)
		}
		if reclaimed != want {
			t.Fatalf("reclaimed = %+v, want %+v", reclaimed, want)
		}
		if got := spend(t, client, prefix, "acme.com"); got != 1 {
			t.Errorf("scope spend after lease reclaim = %d, want 1", got)
		}

		if err := f.MarkDone(ctx, reclaimed.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		// Re-adding a crawled URL is a DUP: still not a new admission.
		if err := f.AddURL(ctx, want); err != nil {
			t.Fatalf("AddURL re-add: %v", err)
		}
		if got := spend(t, client, prefix, "acme.com"); got != 1 {
			t.Errorf("scope spend after re-add = %d, want 1", got)
		}
	})

	t.Run("a second Frontier for the same run observes the spent budget", func(t *testing.T) {
		// The paused-and-resumed Cycle: the budget belongs to the Cycle, not to the
		// process, so a re-constructed Frontier must not double a Scope's share.
		ctx := t.Context()
		id := uuid.New()
		f1 := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(2))
		for i := 0; i < 2; i++ {
			raw := "http://acme.com/" + strconv.Itoa(i)
			if err := f1.AddURL(ctx, scopedURL("acme.com", raw, "acme.com", 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}

		f2 := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(2))
		if err := f2.AddURL(ctx, scopedURL("acme.com", "http://acme.com/resumed", "acme.com", 0)); !errors.Is(err, frontier.ErrScopeBudget) {
			t.Errorf("resumed AddURL err = %v, want ErrScopeBudget", err)
		}
	})

	t.Run("DeleteRun removes the budget state", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(2))
		prefix := "frontier:" + id.String() + ":"

		if err := f.AddURL(ctx, scopedURL("acme.com", "http://acme.com/1", "acme.com", 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		if got := client.Exists(ctx, prefix+"scope_spend").Val(); got != 1 {
			t.Fatalf("scope_spend exists = %d, want 1 before DeleteRun", got)
		}

		if err := redisfrontier.DeleteRun(ctx, client, id); err != nil {
			t.Fatalf("DeleteRun: %v", err)
		}
		if got := client.Exists(ctx, prefix+"scope_spend").Val(); got != 0 {
			t.Errorf("scope_spend exists = %d after DeleteRun, want 0", got)
		}
		if got := client.Exists(ctx, prefix+"visited").Val(); got != 0 {
			t.Errorf("visited exists = %d after DeleteRun, want 0", got)
		}
	})

	t.Run("concurrent adds never overshoot the budget", func(t *testing.T) {
		// Enforcement lives inside the atomic add script, so workers racing on one
		// Scope cannot collectively admit more than the budget.
		ctx := t.Context()
		id := uuid.New()
		const (
			budget     = 20
			goroutines = 8
			perRoutine = 25
		)
		f := redisfrontier.New(client, id, redisfrontier.WithScopeBudget(budget))
		prefix := "frontier:" + id.String() + ":"

		admitted := make([]int, goroutines)
		rejected := make([]int, goroutines)
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < perRoutine; i++ {
					raw := "http://acme.com/" + strconv.Itoa(g) + "/" + strconv.Itoa(i)
					err := f.AddURL(ctx, scopedURL("acme.com", raw, "acme.com", 0))
					switch {
					case err == nil:
						admitted[g]++
					case errors.Is(err, frontier.ErrScopeBudget):
						rejected[g]++
					default:
						t.Errorf("AddURL %s: %v", raw, err)
						return
					}
				}
			}(g)
		}
		wg.Wait()

		totalAdmitted, totalRejected := 0, 0
		for g := 0; g < goroutines; g++ {
			totalAdmitted += admitted[g]
			totalRejected += rejected[g]
		}
		if totalAdmitted != budget {
			t.Errorf("admitted = %d, want %d", totalAdmitted, budget)
		}
		if want := goroutines*perRoutine - budget; totalRejected != want {
			t.Errorf("rejected = %d, want %d", totalRejected, want)
		}
		if got := spend(t, client, prefix, "acme.com"); got != budget {
			t.Errorf("scope spend = %d, want %d", got, budget)
		}
		// Re-proves the mutate-nothing invariant under concurrency: the 180
		// rejections left no visited entries behind.
		if got := client.ZCard(ctx, prefix+"visited").Val(); got != budget {
			t.Errorf("visited ZCard = %d, want %d", got, budget)
		}
	})
}
