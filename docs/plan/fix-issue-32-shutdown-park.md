# Fix #32 — shutdown-cancelled run marked `failed` and cannot be resumed

**Type:** bug fix · **Issue:** [#32](https://github.com/nicholasbraun/job-crawler-poc/issues/32) · **Branch:** `fix/issue-32-shutdown-park` (from `main`)

This doc is written to be executed by a **fresh session** with no prior context.
Everything you need is here; read it top to bottom before editing. All code
changes are confined to one package: `internal/runner/`.

---

## Symptom

Restart the server mid-crawl (SIGINT/SIGTERM, `docker compose restart`) and the
active run is left `failed` with error `frontier: next: i/o timeout`, and the new
process never resumes it. A graceful shutdown is *supposed* to **park** active
runs as `paused` (frontier kept, re-adopted by `Reconcile`).

## Root cause

The "resumable interruption" decision keys off the **error shape** instead of the
**run context**. Data-flow trace:

1. `Runner.Shutdown` (`internal/runner/runner.go`, ~L285) sets `draining = true`
   and cancels each run's `runCtx`.
2. The orchestrator loop is usually blocked inside `Frontier.Next` →
   `nextScript.Run(ctx, ...)` (`internal/frontier/redis/redis.go`, ~L349). When
   `runCtx` is cancelled **mid-read**, go-redis aborts the in-flight socket read
   by setting a past deadline. That surfaces as a net **i/o timeout**
   (`os.ErrDeadlineExceeded`), which `Next` wraps as `frontier: next: i/o timeout`
   (`redis.go`, ~L355) — **not** a wrapped `context.Canceled`.
   - Contrast: if the loop is cancelled while *sleeping* (`redis.go`, ~L414) it
     returns `context.Canceled`. That is why the bug only bites **busy** crawls
     (which spend their time inside the Redis read, not sleeping).
3. `orchestrator.Run` returns that raw error unchanged
   (`internal/orchestrator/orchestrator.go`, ~L85-88).
4. In `supervise`, the park branch requires `errors.Is(runErr, context.Canceled)`
   (`runner.go`, ~L394) → **false** for an i/o timeout → falls through to
   `terminalStatus` (~L470) whose default arm returns `RunStatusFailed`. The
   frontier is then **cleaned** (~L415), destroying resume state. `Reconcile`
   only re-adopts running/stopping/paused (~L174), so the `failed` run is lost.

**Second latent bug, same root cause:** a user **Stop** (`runner.go`, ~L254) also
cancels `runCtx`; if it lands mid-`Next`, the run is marked `failed` instead of
`stopped`.

### Not the cause: local-LLM timeout

Ruled out. LLM calls run in worker pools; `pool.process`
(`internal/pool/pool.go`, ~L106-108) logs-and-skips any `Process` error (see
`TestPoolContinuesAfterProcessError`). An LLM timeout can never mark a run
`failed` — it only explains why a crawl is long-running/mid-flight at restart.
(An `http.Client.Timeout` also reads as `context deadline exceeded (Client.Timeout
...)`, textually different from the Redis-socket `i/o timeout`.) Do **not** touch
the LLM or frontier packages for this fix.

---

## Scope

**In scope** — two edits, both in `internal/runner/runner.go`, keying the fate
decision off run-context cancellation rather than the net-timeout error shape:
the shutdown-park branch and `terminalStatus`.

**Explicitly out of scope (deferred, do NOT change behavior here):** a *transient*
frontier I/O error during **normal** operation / reconcile (no shutdown, no stop)
must still mark the run `failed` and clean the frontier. That unrecoverability is
a separate design change (own issue + ADR). Your `terminalStatus` change must
keep that path `failed` — see the guard note below. Also do **not** change the
go-redis client, add retries, or touch `Reconcile`'s adoption set.

---

## The changes (`internal/runner/runner.go`)

### 1. Park branch — key off the run context (~L394)

```go
// before
if r.draining && !ar.stopRequested && errors.Is(runErr, context.Canceled) {
// after
if r.draining && !ar.stopRequested && runErr != nil && errors.Is(runCtx.Err(), context.Canceled) {
```

- During a drain `runCtx` is always cancelled, so `runCtx.Err()` is a reliable
  signal regardless of how the frontier's underlying read failed.
- The `runErr != nil` guard preserves the natural-completion-during-shutdown
  case: a run that hit `ErrDone` (→ `nil`) just as shutdown fired must still be
  recorded `completed`, not resurrected as `paused`.
- Rewrite the comment block just above it (~L387-393). It currently claims
  "`errors.Is` covers the wrapped `context.Canceled` the redis frontier returns
  when Next is cancelled mid-wait" — which is exactly the false assumption behind
  the bug. Replace with the real reasoning: a mid-read cancellation surfaces as a
  net i/o timeout, not `context.Canceled`, so the resumable-interruption decision
  is keyed off the run context.

### 2. `terminalStatus` — cancellation-induced errors are `stopped` (~L470)

Thread the run-context error into `terminalStatus` and add a branch, so a user
Stop that unblocked a frontier op mid-read (net i/o timeout) is recorded
`stopped`, not `failed`.

Call site (~L405):

```go
status, errMsg := terminalStatus(runErr, runCtx.Err())
```

Function:

```go
func terminalStatus(err error, ctxErr error) (crawler.RunStatus, string) {
	switch {
	case err == nil:
		return crawler.RunStatusCompleted, ""
	case errors.Is(err, orchestrator.ErrStopRequested), errors.Is(err, context.Canceled):
		return crawler.RunStatusStopped, ""
	case ctxErr != nil: // run context cancelled → intentional stop, even if the
		// frontier surfaced a net i/o timeout instead of context.Canceled
		return crawler.RunStatusStopped, ""
	default:
		return crawler.RunStatusFailed, err.Error()
	}
}
```

Update the `terminalStatus` doc comment to mention the `ctxErr` parameter and why
a cancelled run context downgrades any error to `stopped`.

**Why this keeps the deferred case `failed`:** `ctxErr` (i.e. `runCtx.Err()`) is
nil unless the run was Stopped or Shutdown. A genuine transient Redis timeout
during normal operation happens with a live (non-cancelled) context, so it still
hits the `default` arm → `failed`. Confirm this with a test case (below).

---

## Tests (`internal/runner/runner_test.go`)

The existing `TestShutdownParksRunsForResume` uses `blockingFrontier`, whose
`Next` returns `ctx.Err()` (= `context.Canceled`) on cancel — so it passes even
with the bug present. Add coverage for the **real** failure shape.

1. **New test double** next to the others (~L110-141):

   ```go
   // timeoutFrontier models the redis frontier when Next is cancelled mid-read:
   // go-redis aborts the in-flight socket read via a deadline, so Next returns a
   // net i/o timeout (os.ErrDeadlineExceeded), NOT a wrapped context.Canceled.
   type timeoutFrontier struct{}

   func (timeoutFrontier) AddURL(ctx context.Context, url crawler.URL) error { return nil }
   func (timeoutFrontier) Next(ctx context.Context) (crawler.URL, error) {
       <-ctx.Done()
       return crawler.URL{}, fmt.Errorf("frontier: next: %w", os.ErrDeadlineExceeded)
   }
   func (timeoutFrontier) MarkDone(ctx context.Context, url string) error { return nil }
   ```

   (Add `"fmt"` and `"os"` to the test imports if not already present.)

2. **`TestShutdownParksRunDespiteFrontierTimeout`** — start a run backed by
   `timeoutFrontier`, `Shutdown`, then assert: status `paused`, `FinishedAt ==
   nil`, and the frontier cleaner was **not** invoked for it. Model the assertions
   on `TestShutdownParksRunsForResume`. **Fails before fix #1, passes after.**

3. **`TestStopClassifiesFrontierTimeoutAsStopped`** — start a run backed by
   `timeoutFrontier`, `Stop` it, wait for finish, assert terminal status
   `stopped` (not `failed`) and the frontier **was** cleaned. **Fails before fix
   #2, passes after.**

4. **Update `TestTerminalStatus`** (~L145) to the 2-arg signature and add cases:
   - `{err: i/o timeout, ctxErr: context.Canceled}` → `stopped`
   - `{err: i/o timeout, ctxErr: nil}` → `failed` (proves the deferred transient
     case stays `failed`)
   - existing cases pass `ctxErr: nil`.

5. Keep `TestShutdownParksRunsForResume` as-is (the `context.Canceled` path stays
   valid and must keep passing).

Reuse existing helpers: `newFakeRunRepo`, `fakeDefRepo`, `waitForFinish`,
`WithFrontierCleaner`, and the `blockingFrontier`/`doneFrontier` patterns.

---

## Verify

**Prove the regression first (optional but recommended):** write the two new
tests, run them against the **un-fixed** `runner.go` and watch them fail, then
apply the fix and watch them pass.

**Automated (must be green):**

```bash
go test ./internal/runner/... -race -v
go test ./... && go test -race ./...
gofmt -l . && goimports -l .    # no output = clean
```

Note: the full suite spins up Postgres/Redis via testcontainers, so a running
Docker daemon is required.

**Manual (hand back to the user with these steps):**

```bash
make docker-up     # Postgres + Redis + crawler
```

1. Start a crawl (dashboard or `POST /api/runs`) and let the page counter climb
   so the frontier is busy.
2. `docker compose restart crawler` (SIGTERM → `Shutdown`).
3. Expected **after fix**: shutdown logs `runner: paused run for resume`, the run
   row is `paused`; on restart `Reconcile` adopts it, flips it to `running`, and
   the page counter **continues** (frontier preserved) — no `failed`, no reset.
4. Optional Stop check: start a busy crawl, hit Stop mid-crawl, confirm it ends
   `stopped`, not `failed`.

---

## Wrap-up

- Commit style: Conventional Commits, e.g. `fix(runner): park shutdown-cancelled
  run instead of failing it`. **No `Co-Authored-By` trailer.**
- Include this doc in the branch/PR.
- Open the PR against `main` with a body that references the root cause and ends
  with `Closes #32`.
- Do **not** self-merge — a separate `/review` pass merges when clean.

## Follow-up (file as a new issue, do not implement here)

Transient frontier I/O error during normal operation / reconcile → run `failed` +
frontier cleaned = unrecoverable. Options to weigh in an ADR: a frontier-specific
`ReadTimeout` (or none) on the go-redis client, retry-classification inside
`Frontier.Next`, and/or letting `Reconcile` adopt `failed` runs. Relates to
ADR-0003 (redis-transient) and ADR-0002 (stateless-monolith).
