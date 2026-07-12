# User-initiated pause/resume; `paused` means human-parked

A user can Pause a running Crawl Run from the dashboard and Resume it later, without
stopping the container. Pause is a *hard* park-and-relaunch that reuses the existing
graceful-shutdown / `Reconcile` path: the run drains its in-flight work, its Frontier is
left intact in Redis, and it parks as `paused`; Resume rebuilds the engine from the
preserved Frontier and persisted counters and continues where it left off. A new transient
`pausing` desired-state mirrors `stopping` — written to Postgres and honored by the run's
status watcher, so a crash mid-drain is re-derived into a park rather than lost. We reused
the park/relaunch machinery instead of freezing the loop in place so a paused run holds no
goroutines or connections and is crash-safe by construction.

Crucially, this **redefines `paused`**: it is now *exclusively* a human-initiated park that
survives restarts. `Reconcile` no longer auto-resumes `paused` runs — they stay parked until
a human Resumes them. Graceful shutdown, which previously parked runs as `paused`, now leaves
them `running` so `Reconcile` auto-resumes them like a crash. Stopping a paused run is allowed
and terminal: it transitions straight to `stopped` and cleans up the preserved Frontier.

## Considered options

- **Soft freeze** — block the loop in place, keeping the engine alive. Instant resume, but
  pins idle goroutines / Redis connections / LLM-stream consumers indefinitely and re-invents
  crash safety. Rejected.
- **Reuse `paused` for both intents** — let shutdown-park and user-pause share one meaning.
  Then either a restart resurrects a user-paused run (violating the human's intent), or
  shutdown-parked runs need a manual resume after every deploy. Rejected in favor of
  distinguishing the two: automatic parking (shutdown/crash) auto-resumes; only an explicit
  human Pause persists across restarts.

## Consequences

Refines the incidental "a server restart pauses in-flight runs" wording of ADR-0002: a
restart now leaves runs `running` and `Reconcile` resumes them, and the `paused` status is
reserved for the user-facing action. A run gracefully parked by shutdown momentarily reads as
`running` in Postgres during the restart window even though nothing is executing.
