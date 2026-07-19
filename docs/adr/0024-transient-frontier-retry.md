# A transient Frontier Redis error rides out in place; the Crawl Run stays running

## Context

The Frontier's per-run state lives in Redis (ADR-0003). A momentary Redis
disruption during normal operation — a failover, a restart, a dropped
connection, a read timing out — surfaces from `Frontier.Next` as an I/O error
under a live (non-cancelled) run context. Previously the runner mapped that to
`failed` and then deleted the run's Redis Frontier keys, so a recoverable blip
permanently killed an in-flight Crawl Run and discarded all its progress. This
was deliberate at the time (#38, following #32), locked in by a
`{i/o timeout, live ctx} → failed` test.

The reframing that changes the decision: a transient blip does not restart the
process, but `Reconcile` — the only thing that re-adopts an interrupted run —
runs only at boot. So letting the run fail and hoping a later `Reconcile`
resurrects it (issue #39's Option 3) does nothing for a live process; the run
just sits `failed` forever.

## Decision

The Frontier absorbs transient Redis errors itself: `Next`, `AddURL`, and
`MarkDone` retry through a shared helper with capped backoff, unbounded for as
long as the run context is live. The Crawl Run stays `running` and continues the
moment Redis heals. The run context remains the only bound — a Stop, Pause, or
Shutdown cancels it and breaks the retry loop promptly.

"Transient" is an allowlist: network-shaped errors (connection refused/reset,
EOF, i/o timeout, pool timeout) and the known temporarily-unavailable server
replies (LOADING, READONLY, CLUSTERDOWN, MASTERDOWN, TRYAGAIN). Any unrecognized
Redis *reply* is treated as fatal and still Fails the run. A cancelled context
wins over any error shape, so an intentional Stop/Shutdown that surfaces as an
i/o timeout (#32) is still classified as stopped, not retried.

## Considered options

- **Let `Reconcile` adopt `failed` runs and skip the Frontier cleanup (#39
  Option 3).** Rejected as the primary mechanism: it only helps if the process
  also restarts during the outage, which a transient blip does not cause. It
  also reintroduces Frontier-key leaks and the risk of resurrecting
  genuinely-broken runs. Retrying in place makes it unnecessary — a genuine
  *crash* still lands on the existing `running` → `Reconcile` path with the keys
  intact.
- **Bounded retry that gives up into a terminal state.** Rejected: with a live,
  cancellable context, giving up can only produce a worse state (a `failed` or
  zombie run) than "still trying," and any fixed bound will fail runs that would
  have recovered one interval later.
- **Remove/disable the client read timeout ("or none").** Rejected: a *finite*
  `ReadTimeout` is load-bearing — it is what turns a stall into the retryable
  i/o error the loop rides out; with no timeout a read hangs forever and the
  retry loop never runs.

## Consequences

Supersedes the deliberate `{i/o timeout, live ctx} → failed` stance from
#38/#32: that error class is now absorbed in the Frontier and never reaches the
runner's terminal classification, whose failing arm is now reached only by
genuinely fatal errors. A new operational state-of-being appears: a Crawl Run
can be `running` yet stalled — alive and retrying while Redis is unreachable,
making no progress. A permanently dead Redis therefore shows as stalled
`running` runs rather than a per-run `failed` signal; a per-retry warning log
and a `crawler.frontier.transient_retries` counter make the stall visible and
alertable. The LLM stage's own Redis stream (`llmstream`) is a separate touch
not covered by this retry and may warrant the same treatment later.
