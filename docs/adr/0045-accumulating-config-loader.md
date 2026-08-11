# Configuration is read through one accumulating Loader; the binary owns the exit

Every knob in `cmd/server/main.go` was read as the same four-line triple: `env.EnvOr` into a
`strconv`/`time.ParseDuration`, a hand-written validity test, and a `log.Fatalf` naming the
variable a second time. Fifteen repetitions of it, and the shared helper underneath
(`EnvOr`) covered only the first line. The shape had three costs. The key was spelled twice
per knob, so the error message could name one variable and print another's value. The
message re-read the raw value with `os.Getenv` and so could never show the default it fell
back to. And `log.Fatalf` fires on the **first** bad value: the crawler is deployed as a
container whose environment is edited and redeployed as a unit, so three typos cost three
restarts to discover, one at a time.

`env.Loader` replaces the triple with one typed accessor per knob — `PositiveInt`,
`PositiveDuration`, `Bool`, `Fraction`, `String`, and `Text` for a destination that already
parses itself. Each records a failure and returns the fallback rather than exiting, so
loading always runs to completion and `Err` reports **every** malformed variable at once,
one per line, each naming the key, the offending value, and the default it took.

## The package never exits; the caller decides what a bad knob costs

`cmd/server` `log.Fatal`s, because it has not started serving yet. `cmd/llmbench` maps the
same error to exit 2 and must never `log.Fatal`. That difference is why the accessors
accumulate instead of exiting: an exiting helper would have been unusable by the CLI, which
is precisely why the CLI grew its own copy of `envOr` in the first place — the drift
ADR-0045's predecessor package claimed to prevent, reintroduced under a name (`pgenv`) too
narrow to look like anyone's shared home.

## Defaults live with the code they configure

`env` is domain-free. A default that carries domain meaning belongs to the package that owns
the behavior — `postgres.DefaultURL`, `robotstxt.DefaultCacheTTL`,
`redisfrontier.DefaultVisitedCap` — so a knob's default and the code reading it cannot drift.
The typed accessors take that default as a typed Go value, which also retires the
`strconv.Itoa(...)`-into-a-string-parsed-back-to-an-int round trip the old form needed.

The LLM knobs go further and are read **inside** `internal/openrouter`, not in either main.
`cmd/server` and `cmd/llmbench` must drive the model through identical settings or a bench
score stops describing the crawl it claims to measure — and in this repo gate changes are
settled by that score. Before this, the three defaults (`5m`, `1500`, `8000`) were spelled
out as literals in both mains *and* as constants in `openrouter`, three copies one edit away
from disagreeing silently.

## Consequences

- `cmd/server/main.go` reads its whole configuration in one block, gated by a single
  `ld.Err()` check, before it acts on any of it. `slog.SetDefault`, the Redis dial, and the
  Postgres dial all moved below that gate — nothing may act on a knob until every knob has
  been validated.
- The per-knob rationale comments — the kill-switch explanations and ADR citations — stay in
  `main.go` where they are the operator-facing documentation, now sitting directly above a
  one-line read instead of a four-line parse.
- `LLM_TIMEOUT=0` is now a startup failure. It previously parsed, then hit
  `Config.withDefaults`, which silently substituted 5m — an operator writing `0` for "no
  timeout" got a five-minute one and no warning. Every other knob keeps exactly the
  validation it had.
- This is a **hard failure on a malformed value**, not a warn-and-continue. A crawler that
  boots with a silently-defaulted kill switch is the failure mode these knobs exist to
  prevent; refusing to start is legible, running with the wrong dial is not.
