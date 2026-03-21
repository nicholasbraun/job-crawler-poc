package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
)

type URLRepository struct {
	db *sql.DB
}

var _ crawler.URLRepository = &URLRepository{}

func (ur *URLRepository) Save(ctx context.Context, url string) (bool, error) {
	res, err := ur.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO url (url) VALUES (?);
		`, url)
	if err != nil {
		return false, fmt.Errorf("error saving url %s: %w", url, err)
	}

	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (ur *URLRepository) Visited(ctx context.Context, url string) (bool, error) {
	var urlRow string

	row := ur.db.QueryRowContext(ctx, `
		SELECT url FROM url WHERE url = ?;
		`, url)

	err := row.Scan(&urlRow)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}

		return false, fmt.Errorf("error querying url table: %w", err)
	}

	return true, nil
}

func NewURLRepository(db *sql.DB) *URLRepository {
	return &URLRepository{
		db: db,
	}
}
