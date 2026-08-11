// Package env resolves process configuration from the environment for the
// crawler's binaries (cmd/server, cmd/doctor, cmd/llmbench). It is deliberately
// domain-free: it knows how to look a variable up, parse it, and fall back, and
// nothing about what any particular knob means. Defaults that carry domain
// meaning live with the package that owns them (e.g. postgres.DefaultURL,
// robotstxt.DefaultCacheTTL), so a knob's default and the code it configures
// cannot drift apart.
//
// Loader is the entry point for a binary reading its whole configuration: it
// gathers every malformed value and reports them together (ADR-0045). EnvOr is
// the bare lookup, for a single knob that needs no parsing.
package env

// EnvOr returns the value of environment variable key, or fallback if it is
// unset or empty. Empty falls back rather than resolving to "" so a variable
// blanked in a compose file or CI environment behaves the same as an absent
// one -- the mistake it usually is.
func EnvOr(key, fallback string) string {
	if v, set := lookup(key); set {
		return v
	}
	return fallback
}
