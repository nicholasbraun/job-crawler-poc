package env

import (
	"encoding"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Loader resolves a binary's knobs and accumulates every failure instead of
// stopping at the first, so a process started with several malformed variables
// reports all of them in one shot (ADR-0045). The crawler is deployed as a
// container whose environment is edited and redeployed as a unit; failing one
// variable at a time costs one restart per mistake.
//
// A failed knob yields its fallback, so loading always continues with a usable
// value and the caller can finish reading the rest of the configuration before
// deciding what to do. The zero Loader is ready to use.
//
// Callers own the consequence of Err: cmd/server log.Fatals before it starts
// serving, cmd/llmbench maps it to exit 2. This package never exits.
type Loader struct {
	errs []error
}

// String resolves key, or fallback when unset or empty. It cannot fail; it is a
// method for symmetry with the parsing accessors.
func (l *Loader) String(key, fallback string) string {
	return EnvOr(key, fallback)
}

// Duration resolves key as a Go duration (e.g. "90s", "5m", "24h"), or fallback
// when unset or empty. It does not require a positive value -- see
// PositiveDuration for knobs where zero would disable a limit.
func (l *Loader) Duration(key string, fallback time.Duration) time.Duration {
	raw, set := lookup(key)
	if !set {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.reject(key, raw, "must be a Go duration (e.g. 90s, 5m, 24h)", fallback)
		return fallback
	}
	return v
}

// PositiveDuration resolves key as a Go duration strictly greater than zero, or
// fallback when unset or empty. Zero is rejected rather than accepted as
// "unbounded": every caller uses it as an interval or a TTL, where zero would
// mean a hot loop or an instantly-stale cache.
func (l *Loader) PositiveDuration(key string, fallback time.Duration) time.Duration {
	raw, set := lookup(key)
	if !set {
		return fallback
	}
	v, err := time.ParseDuration(raw)
	if err != nil || v <= 0 {
		l.reject(key, raw, "must be a positive Go duration (e.g. 90s, 5m, 24h)", fallback)
		return fallback
	}
	return v
}

// PositiveInt resolves key as an integer of at least 1, or fallback when unset
// or empty. Every integer knob in the crawler sizes a pool, a cache, or a cap,
// where zero or negative is never meaningful.
func (l *Loader) PositiveInt(key string, fallback int) int {
	raw, set := lookup(key)
	if !set {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		l.reject(key, raw, "must be a positive integer", fallback)
		return fallback
	}
	return v
}

// Bool resolves key as a boolean accepted by strconv.ParseBool ("true", "false",
// "1", "0", ...), or fallback when unset or empty. Kill switches are read
// through it, so a typo like "yes" is a startup failure rather than a silently
// disabled switch.
func (l *Loader) Bool(key string, fallback bool) bool {
	raw, set := lookup(key)
	if !set {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.reject(key, raw, `must be a boolean ("true" or "false")`, fallback)
		return fallback
	}
	return v
}

// Fraction resolves key as a float in the closed interval [0,1], or fallback
// when unset or empty. Both ends are inclusive: 0 switches a sampled mechanism
// off entirely and 1 applies it to everything.
func (l *Loader) Fraction(key string, fallback float64) float64 {
	raw, set := lookup(key)
	if !set {
		return fallback
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 || v > 1 {
		l.reject(key, raw, "must be a fraction in [0,1]", fallback)
		return fallback
	}
	return v
}

// Text resolves key through into's UnmarshalText, applying fallback when the
// variable is unset or empty. It is the escape hatch for a type that already
// parses itself (slog.LevelVar and the like) -- prefer it over growing a Loader
// method per destination type. A fallback that into itself rejects is a
// programming error and is reported as such.
func (l *Loader) Text(key string, into encoding.TextUnmarshaler, fallback string) {
	raw, set := lookup(key)
	if !set {
		if err := into.UnmarshalText([]byte(fallback)); err != nil {
			l.errs = append(l.errs, fmt.Errorf("%s: invalid built-in default %q: %w", key, fallback, err))
		}
		return
	}
	if err := into.UnmarshalText([]byte(raw)); err != nil {
		// The destination's own error already quotes the offending value, so this
		// adds only the key and the default rather than printing the value twice.
		l.errs = append(l.errs, fmt.Errorf("%s: %w (default %q)", key, err, fallback))
	}
}

// Err reports every knob that failed to parse, joined one per line, or nil when
// the whole configuration resolved.
func (l *Loader) Err() error {
	return errors.Join(l.errs...)
}

// reject records a malformed knob. The message names the variable, what was
// wanted, what arrived, and the default it fell back to -- everything needed to
// fix a container's environment without reading this source.
func (l *Loader) reject(key, raw, want string, fallback any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s, got %q (default %v)", key, want, raw, fallback))
}

// lookup reports the raw value of key and whether it was set to anything
// non-empty. An empty value is treated as unset throughout this package, so a
// variable blanked in a compose file behaves like an absent one.
func lookup(key string) (string, bool) {
	v := os.Getenv(key)
	return v, v != ""
}
