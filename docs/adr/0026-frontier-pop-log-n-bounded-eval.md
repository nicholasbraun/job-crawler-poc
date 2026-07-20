# Frontier pop is O(log N) via a non-empty-domains invariant and bounded scripts

## Context / Decision

The Redis Frontier's `Next` pop scanned every Politeness Domain ever seen on
each pop — a full ranked scan plus a per-domain queue-length probe — so a Crawl
Run's throughput decayed as it grew, and a single pop could stall Redis long
enough to trip its Lua time limit (the `BUSY` of #145). We restructured the pop
around an invariant: the Frontier's domain schedule holds *only* Politeness
Domains whose queue is non-empty. The earliest eligible domain is then found by
an indexed lookup (O(log N)) instead of a scan; a domain leaves the schedule the
moment its queue drains and re-enters when work returns.

To keep every atomic script within Redis's Lua time limit, both of the pop
script's loops are now bounded per call: at most a fixed number of expired
leases are reclaimed, and at most a fixed number of stale/empty domains are
pruned, with the remainder handled on subsequent pops. No unbounded loop remains
in any Frontier script, so this closes #145; the same bounded prune self-heals
the historical domain bloat on already-running Crawl Runs without a migration
step.

## Considered options

- **Persist a per-domain cooldown map** so a domain that drains and refills
  keeps its politeness delay across the gap. Rejected: it reintroduces exactly
  the never-pruned, monotonically-growing per-domain state this change exists to
  eliminate.

## Consequences

- **Cooldown is best-effort, enforced only while a domain has continuously
  queued work.** When a domain's queue drains to empty it leaves the schedule; a
  later URL for it re-enters as immediately eligible, so a drain-then-refill
  inside the cooldown window can re-hit the domain slightly early. Accepted as a
  benign politeness relaxation — the download itself usually outlasts the
  cooldown, and the alternative reintroduces unbounded state.
- Bounding the reclaim loop means a large batch of leases expired during a stall
  drains back into the queues over several pops rather than all at once;
  correctness is unaffected, since an expired lease is already-lost work being
  returned to its queue.
- **The `crawler.frontier.domains.size` gauge is labeled `run_id`**, a deliberate
  exception to the no-`run_id` metric rule: a gauge is last-value, so concurrent
  runs would clobber one unlabeled series. The cost is that the SDK's last-value
  aggregation never evicts an attribute set, so one series accrues per `run_id`
  the process has ever popped — the count grows with runs *seen* over the process
  lifetime, not just concurrently-active runs. Accepted as bounded for this
  crawler's shape (a perpetual Discovery plus occasional bounded Keyword Crawls);
  evicting a finished run's series in `DeleteRun` is a deferred hardening.
- The `visited`-set growth that dominates a perpetual run's footprint is
  untouched here; bounding it remains #75.
