# A Retry-After we will not honour ends the fetch, rather than shrinking to the ceiling

## Context

`RetryClient` honours a server's `Retry-After` hint in place of its exponential
backoff, then caps the result at `maxBackoff` (2m by default, over 5 tries). A
hint longer than the ceiling is therefore shrunk to it, and the remaining
attempts are spent anyway.

Measured on live traffic once a301ef0 made the hint visible (#325, data in #321):

- `barn2.com` sent `Retry-After` on **100%** of its 429s — 3,222 with a hint, 0
  without.
- The hint is a countdown to a **fixed hourly window**, not a sliding one: those
  3,222 responses collapse onto 14 reset instants on a clean **+60.1 min**
  ladder, and within a window the reset instant is stable across ~370 requests.
- Values ran 0–3600s against a 120s ceiling. `www.bauernhofurlaub.de` sent 503s
  with 43200s (12h).

So once a host hands over a hint longer than the ceiling, every remaining
attempt lands inside a window the host has just said has not reset. They are
guaranteed failures, known in advance, from a header already parsed — costing 5
requests and ~480s of held worker per URL to reach an end state that one request
reaches. Over ~11h there were 5,027 throttle exhaustions, 3,251 of them carrying
a hint.

## Decision

A retryable failure whose `RetryAfter` exceeds `maxBackoff` ends the attempt
sequence immediately, returning the `*StatusError` rather than sleeping to the
ceiling and retrying.

- **Keyed on the hint alone, never on the computed wait.** An exponential
  backoff that overruns the ceiling is *our* escalation and says nothing about
  when the server will serve us; it is still capped and still retried. The two
  were previously capped by one shared line, and conflating them here would
  abandon hint-less 5xx failures as soon as the doubling passed the ceiling —
  invisible under the shipped defaults, one `WithMaxTries` away from biting.
- **Below the final-attempt break, not above it.** On the last attempt there is
  nothing left to abandon, so that case stays a plain exhaustion. Every recorded
  abandonment therefore saved at least one request, which is what lets the
  counter be read as a saving rather than a rename.
- **No ceiling means no abandonment.** `maxBackoff <= 0` already documents an
  unbounded hint; with no ceiling there is no such thing as a hint we refuse to
  honour, so it is waited out.
- The terminal error keeps the URL — `pool.go` logs no URL attribute, so that
  message is the only place it survives to the log — and names the ceiling,
  leaving the wrapped `*StatusError` to render what the server asked for.
- A `crawler.http-client.retries.abandoned` counter, labelled by `status` only,
  makes the event countable without making it catchable.

It is outcome-neutral in all three lanes: an exhausted-retry error is logged and
skipped in the discovery and walk lanes, and `classifyStatus` already maps a 429
to `ProbeInconclusive`, so a throttled Career Page rides the staleness backstop
exactly as before and a throttling host still cannot be mistaken for a dead one.

## Considered options

- **Test the remaining budget instead: `hint > (maxTries - i) × maxBackoff`.**
  Strictly more accurate. Because the hint is a countdown, successive capped
  waits telescope, so a 400s hint is reachable within four 120s sleeps and
  recovers today; the simple test abandons it. Rejected anyway: in that band the
  budget test spends four more requests on a host that explicitly asked for
  silence, which is the impoliteness #321 exists to fix; a walk URL is re-seeded
  from the Catalog next Cycle, so the cost is delay rather than data; and on a
  host whose hint is a *sliding* window the budget test reproduces exactly the
  waste this ADR removes, while the simple test is right on both shapes.
- **Raise or remove the ceiling and honour long hints in full.** #321's option,
  and the one this deliberately does not take: it pays the longer wait in pinned
  workers, which is the trade-off that makes #321 hard. See the consequences.
- **An exported sentinel, and a quieter log level for throttles.** These are one
  decision, not two: `pool.go` sees only an `error`, so demoting the line
  requires the sentinel. Both are left to #321, which should settle the level for
  hint-carrying and hint-less throttles together — the 1,776 hint-less
  exhaustions would otherwise keep shouting while abandoned ones went quiet.
- **A kill switch**, per the convention for paths that can go wrong silently and
  at scale. Rejected: this one cannot go wrong *silently* — the counter sits on
  the same dashboard row as the 429 rate — it writes nothing to the Corpus or
  Catalog, and `maxBackoff` is already the dial that turns it off.
- **A second counter for requests saved.** Rejected as near-degenerate: a host
  handing out an over-ceiling hint hands one out on the first 429 too, so
  abandonments sit at attempt 1. The saving is already measured directly by the
  429 rate on `crawler.http-client.downloads.time`; the new counter's only job is
  to attribute a drop there to this decision rather than to the host relenting.

## Consequences

- **`maxBackoff` now does double duty**: the longest single sleep, *and* the
  boundary of a hint we take seriously. #321's ceiling-raising option therefore
  moves both at once — raising it to 10m does not merely permit a 10m wait, it
  converts today's abandonments back into 10-minute pinned workers. The two
  levers are no longer separable without a second knob.
- **Over-abandonment in `(maxBackoff, (maxTries-1) × maxBackoff]` is accepted**
  — `(120s, 480s]` on the first attempt with the shipped defaults, which under
  barn2's near-uniform 0–3600s countdown is on the order of a tenth of hinted
  429s that recover today. It is not measurable from the exhaustion logs, which
  render only the last attempt's hint. If it bites, the repair is the budget test
  above, not raising the ceiling.
- **The trailing cap is now unreachable for a hint.** A hint is either at or
  below the ceiling and waited in full, or above it and abandoned, so
  `wait > maxBackoff` can only bite the exponential escalation. The line stays,
  with a comment saying so.
- **The ERROR volume is unchanged** — one line per URL before and after. This
  makes each line cheaper to produce, not rarer, so an unchanged log volume after
  deploy must not be read as the change not working.
- The existing `Caps an over-long Retry-After hint at maxBackoff` test pins the
  behaviour being removed and is rewritten rather than repaired.
- `cmd/llmbench`'s gold-set refetcher gains a 120s saving per throttled row,
  consistent with the intent its `WithMaxTries(2)` already states.
- The ATS Fetch lane is untouched: every fetcher in `internal/ats` carries its
  own `*http.Client`, so Absence-from-Board and its completeness rule cannot
  move.
