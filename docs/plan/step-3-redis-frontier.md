# Step 3 — Redis frontier + visited set

**Prerequisites:** Step 2. Read `/CONTEXT.md`, ADR-0003, and
`internal/frontier/inmem/inmem.go` (the reference semantics you must preserve).

## Goal

Replace the in-mem frontier + SQLite visited set with a Redis-backed, **resumable,
crash-safe** frontier keyed per Crawl Run. This is the riskiest step — keep the in-mem impl
as the semantic reference.

## Scope (build this)

- `internal/frontier/redis/` implementing `frontier.Frontier`, keys namespaced
  `frontier:{run}:…`:
  - `q:{domain}` LIST — per-Politeness-Domain FIFO (LPUSH/RPOP).
  - `domains` ZSET — member=domain, score=next-eligible epoch-ms (per-domain cooldown).
  - `knowndomains` SET — **persistent** (never pruned), for the `maxDomains` cap (matches
    today's never-shrinking domain map).
  - `visited` SET — dedup; `SADD` return (1=new) replaces `URLRepository.Save` isNew.
  - `processing` ZSET — member=url, score=lease-expiry; replaces the in-flight counter and
    survives worker crashes.
- **`Next` and `AddURL` must each be a single Lua script** (or two workers double-pop /
  race the dedup). `Next`: reclaim expired `processing` leases → pick earliest ready domain
  (`ZRANGEBYSCORE domains -inf now LIMIT 0 1`) → RPOP its list → bump cooldown → add to
  `processing`. `AddURL`: maxDomains check-and-add → `visited` SADD-if-new → LPUSH (fuse
  SADD-if-new + enqueue so a dedup race can't drop URLs).
- `maxDepth` carried in the queued member (`{url, depth}`), rejected client-side.
- `ErrDone` iff (no domain list non-empty) AND (`ZCARD processing == 0`) — **bounded mode
  only**.
- **Interface change:** `frontier.MarkDone(ctx)` → `MarkDone(ctx, url)` (Redis needs the
  URL to clear its lease). Update the in-mem impl + `url_processor`
  (`defer w.frontier.MarkDone(ctx, nextURL.RawURL)`).
- Add a bounded/**perpetual** mode to the Frontier: in perpetual mode, on drain `Next`
  polls/blocks instead of returning `ErrDone`.
- Cross-process `Next` blocking: short-poll with computed sleep =
  `time.Until(min domain score)` (mirrors the in-mem `nextDeadline` logic); add jitter to
  avoid thundering herd. `go-redis` client; add Redis to `docker-compose.yml`.

## Interface changes

`MarkDone(ctx, url)`; Frontier bounded/perpetual mode.

## Out of scope

Discovery/Keyword kinds (perpetual mode is added here but first exercised in Step 5).

## Verify

Frontier unit tests mirroring in-mem semantics: per-domain cooldown ordering,
`maxDomains`/`maxDepth` caps, dedup via `visited`. **Crash-safety:** kill a worker
mid-lease → the URL is reclaimed by a later `Next`, with no loss or duplication. A bounded
run reaches `ErrDone`; a perpetual run does not. End-to-end: a Step-1-style crawl now uses
Redis; restarting the server mid-run resumes it. `go test -race ./...` green.
