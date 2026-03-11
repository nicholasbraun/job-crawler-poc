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

	return nil
}
