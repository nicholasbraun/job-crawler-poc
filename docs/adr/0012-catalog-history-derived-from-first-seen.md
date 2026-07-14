# Catalog History is derived read-only from first_seen, not persisted as snapshots

The Overview → Discovery panel's growth sparkline needs a real trend across
restarts, replacing a browser-session accumulator that reset on reload. Issue #71
assumed the fix was a snapshot table fed by a periodic ticker — a durable-state
mechanism governed by ADR-0003. We reject that: both Catalog entities already
persist a `FirstSeen` that `Upsert` preserves across re-crawls, so the cumulative
growth curve is **reconstructable read-only** from existing data —
`count(*) grouped by date_trunc('day', first_seen)`, cumulatively summed. No
migration, no new domain type or repository, and no background-job goroutine (a
pattern that exists nowhere outside the runner's own tickers) to wire into
graceful shutdown. The blast radius is a read-only query, a handler, a route, and
the deletion of the dead session sampler.

The load-bearing trade-off is that the **Catalog Doctor hard-deletes** (ADR-0011),
so a history derived from the surviving rows is **revisionist**: when the Doctor
removes false positives, those entries vanish from the *entire* reconstructed
curve rather than showing as a dip at the moment they were removed. We accept this
deliberately — the Doctor removes bad data, and a growth curve that excludes
corrected false positives is the more honest trend for a decorative widget than
one that memorializes them. A snapshot table would preserve that audit trail, but
paying ~8 new files plus a durable-state commitment to remember counts we never
want to show back is the wrong trade for a sparkline.

## Consequences

- There is **no audit trail of the Catalog's past sizes**. Because we never
  snapshot, pre-deletion counts are unrecoverable; if a future feature genuinely
  needs the ledger (not the growth curve), it must start snapshotting from that
  point forward and cannot backfill history. This is the reversal cost, and it is
  why this decision is recorded rather than left implicit.
- The headline count (`career pages catalogued`) and the sparkline's endpoint are
  drawn from the same query family, so they can never drift — unlike a snapshot
  that lags the live count between ticks.
- This **reverses issue #71's own framing.** The record exists so the next
  contributor does not "fix" the missing snapshot table, nor file the
  post-Doctor curve drop as a bug: both are the design working as intended.
- Gap-filling, cumulation, and downsampling live in one pure Go transform over the
  repo's raw per-day counts — SQL stays a plain `GROUP BY`, and the fiddly logic
  is unit-testable without a database.
