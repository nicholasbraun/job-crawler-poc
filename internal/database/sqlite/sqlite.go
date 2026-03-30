package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("error opening sqlite db with path: %s, %w", path, err)
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return nil, fmt.Errorf("error enabling WAL mode: %w", err)
	}

	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return nil, fmt.Errorf("error setting busy timeout: %w", err)
	}

	return db, nil
}

func Setup(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS url (
			url TEXT PRIMARY KEY
		)
		`)
	if err != nil {
		return fmt.Errorf("error creating url table %w", err)
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS job_listing (
			url TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT,
			company TEXT,
			location TEXT,
			remote INTEGER,
			tech_stack TEXT
		)
		`)
	if err != nil {
		return fmt.Errorf("error creating job_listing table %w", err)
	}

	return nil
}
