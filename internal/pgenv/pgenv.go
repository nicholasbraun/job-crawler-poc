// Package pgenv holds the small pieces of Postgres configuration shared by the
// crawler server (cmd/server) and the Catalog Doctor CLI (cmd/doctor): the
// default local DSN and the environment-lookup helper both use to resolve
// DATABASE_URL. Extracted so the two mains cannot drift on the default DSN or on
// the "unset or empty falls back" lookup semantics.
package pgenv

import "os"

// DefaultDatabaseURL is the local Postgres DSN both binaries fall back to when
// DATABASE_URL is unset or empty, so the Doctor targets the same Catalog the
// server migrates without extra configuration.
const DefaultDatabaseURL = "postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable"

// EnvOr returns the value of environment variable key, or fallback if it is
// unset or empty.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
