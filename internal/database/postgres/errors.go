package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgUniqueViolation is the SQLSTATE Postgres raises for a unique_violation.
const pgUniqueViolation = "23505"

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation raised by the named index. Matching the index name (not just the
// SQLSTATE) keeps the translation precise: an unrelated unique violation -- e.g.
// a primary-key collision -- is not mistranslated into a domain conflict
// sentinel (ADR-0017). For a unique-index violation, PgError.ConstraintName
// carries the index name.
func isUniqueViolation(err error, index string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == pgUniqueViolation &&
		pgErr.ConstraintName == index
}
