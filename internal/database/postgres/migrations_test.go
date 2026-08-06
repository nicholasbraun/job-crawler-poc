package postgres_test

import (
	"database/sql"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // register the "pgx" database/sql driver for goose
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
	"github.com/pressly/goose/v3"
)

// migrateTo applies migrations up to (and including) version against dsn, so a test
// can stage the schema as it stood BEFORE the migration under test and seed rows the
// migration then has to transform.
//
// It drives goose's Provider API rather than the package-level SetBaseFS/SetDialect
// helpers postgres.Migrate uses: those are process-global, and a test reaching for
// them would fight the production migrator running in the same binary. The migrations
// are read from the package directory (a Go test binary runs with its package
// directory as the working directory), which is the same tree embedded by migrate.go.
func migrateTo(t *testing.T, dsn string, version int64) {
	t.Helper()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("opening migration db: %v", err)
	}
	defer func() { _ = db.Close() }()

	p, err := goose.NewProvider(goose.DialectPostgres, db, os.DirFS("migrations"))
	if err != nil {
		t.Fatalf("building goose provider: %v", err)
	}
	if _, err := p.UpTo(t.Context(), version); err != nil {
		t.Fatalf("migrating up to %d: %v", version, err)
	}
}

// groupCount runs a "SELECT <col>, count(*) ... GROUP BY 1" query and returns the
// result as a map, giving the operator-audit assertions a comparable shape.
func groupCount(t *testing.T, pool *pgxpool.Pool, query string) map[string]int {
	t.Helper()

	rows, err := pool.Query(t.Context(), query)
	if err != nil {
		t.Fatalf("running %q: %v", query, err)
	}
	defer rows.Close()

	got := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			t.Fatalf("scanning %q: %v", query, err)
		}
		got[key] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading %q: %v", query, err)
	}
	return got
}

// scalar reads a single text value, used to snapshot catalog facts (the table's
// filenode, the generated search_tsv expression) either side of a migration.
func scalar(t *testing.T, pool *pgxpool.Pool, query string) string {
	t.Helper()

	var v string
	if err := pool.QueryRow(t.Context(), query).Scan(&v); err != nil {
		t.Fatalf("running %q: %v", query, err)
	}
	return v
}

// searchTsvExprQuery reads the stored expression of the generated search_tsv column.
// Migration 0026 must leave it untouched: adding description_source to it would mean
// dropping and recreating the STORED column, the ACCESS EXCLUSIVE table rewrite
// migration 0025 had to pay and this one must not.
const searchTsvExprQuery = `
	SELECT pg_get_expr(d.adbin, d.adrelid)
	FROM pg_attrdef d
	JOIN pg_attribute a ON a.attrelid = d.adrelid AND a.attnum = d.adnum
	WHERE d.adrelid = 'job_listing'::regclass AND a.attname = 'search_tsv'`

// TestDescriptionSourceBackfill asserts migration 0026 gives every pre-existing row a
// TRUTHFUL Description Source (ADR-0041): an ATS-lane row already carries the board
// API's own text, a crawl-lane row carries a legacy model-authored summary, and that
// holds for Closed rows too. Marking ATS rows legacy would let the later refetch heal
// overwrite good board text with scraped page furniture, since the refetch lane
// iterates every Open listing under a crawl seed with no source-lane filter.
//
// It also pins what the migration must NOT do: no stored description may change, the
// table must not be rewritten, and the search index expression must stay exactly as
// migration 0025 left it.
//
// Staged at version 25 rather than via newTestPool, which applies every migration
// before any row exists — there would be nothing for the backfill to touch.
func TestDescriptionSourceBackfill(t *testing.T) {
	dsn := startPostgres(t)
	migrateTo(t, dsn, 25)
	pool := openTestPool(t, dsn)

	// Board-shaped text with punctuation and markup, so "byte-identical" means
	// something; a terse summary for the crawl rows.
	const boardText = "<p>Build &amp; run our platform.</p><ul><li>Go, 5+ yrs</li></ul>"
	seeded := map[string]string{
		"greenhouse:acme:1":     boardText,
		"https://ex.com/j/open": "Engineering role at Acme; backend focus.",
		"https://ex.com/j/gone": "Closed role, still model-authored.",
	}
	if _, err := pool.Exec(t.Context(), `
		INSERT INTO job_listing (canonical_url, url, source, source_id, title, description, closed_at)
		VALUES
			($1, 'https://boards.greenhouse.io/acme/jobs/1', 'ats',   '1', 'Platform Engineer', $4, NULL),
			($2, $2,                                        'crawl', '',  'Backend Engineer',  $5, NULL),
			($3, $3,                                        'crawl', '',  'Retired Engineer',  $6, now())`,
		"greenhouse:acme:1", "https://ex.com/j/open", "https://ex.com/j/gone",
		seeded["greenhouse:acme:1"], seeded["https://ex.com/j/open"], seeded["https://ex.com/j/gone"],
	); err != nil {
		t.Fatalf("seeding pre-migration listings: %v", err)
	}

	filenodeBefore := scalar(t, pool, `SELECT pg_relation_filenode('job_listing'::regclass)::text`)
	searchExprBefore := scalar(t, pool, searchTsvExprQuery)

	if err := postgres.Migrate(t.Context(), dsn); err != nil {
		t.Fatalf("applying migration 0026: %v", err)
	}

	t.Run("each lane is backfilled with its own truthful marker", func(t *testing.T) {
		want := map[string]string{
			"greenhouse:acme:1":     "ats_board",
			"https://ex.com/j/open": "llm_summary",
			"https://ex.com/j/gone": "llm_summary", // Closed rows are backfilled too
		}
		for canonical, wantSource := range want {
			var got string
			if err := pool.QueryRow(t.Context(),
				`SELECT description_source FROM job_listing WHERE canonical_url = $1`, canonical,
			).Scan(&got); err != nil {
				t.Fatalf("reading description_source for %q: %v", canonical, err)
			}
			if got != wantSource {
				t.Errorf("description_source for %q = %q, want %q", canonical, got, wantSource)
			}
		}
	})

	t.Run("stored descriptions are byte-identical", func(t *testing.T) {
		for canonical, wantDescription := range seeded {
			var got string
			if err := pool.QueryRow(t.Context(),
				`SELECT description FROM job_listing WHERE canonical_url = $1`, canonical,
			).Scan(&got); err != nil {
				t.Fatalf("reading description for %q: %v", canonical, err)
			}
			if got != wantDescription {
				t.Errorf("description for %q changed:\n got %q\nwant %q", canonical, got, wantDescription)
			}
		}
	})

	t.Run("the table is not rewritten", func(t *testing.T) {
		// A rewrite (an ADD COLUMN with a volatile default, or a DROP/ADD of the STORED
		// generated column) allocates a new filenode; a metadata-only ADD COLUMN, the
		// row-level backfill UPDATE, and a CHECK validation scan all leave it alone.
		if after := scalar(t, pool, `SELECT pg_relation_filenode('job_listing'::regclass)::text`); after != filenodeBefore {
			t.Errorf("job_listing was rewritten: filenode %s -> %s; keep the constant DEFAULT and add the CHECK as its own statement (NOT VALID + VALIDATE if needed)", filenodeBefore, after)
		}
	})

	t.Run("the search index expression is untouched", func(t *testing.T) {
		if after := scalar(t, pool, searchTsvExprQuery); after != searchExprBefore {
			t.Errorf("search_tsv expression changed:\n got %s\nwant %s", after, searchExprBefore)
		}
	})

	t.Run("open listings audit by Description Source and reconcile with the lane split", func(t *testing.T) {
		bySource := groupCount(t, pool, `
			SELECT description_source, count(*) FROM job_listing
			WHERE closed_at IS NULL GROUP BY 1`)
		wantBySource := map[string]int{"ats_board": 1, "llm_summary": 1}
		if len(bySource) != len(wantBySource) {
			t.Fatalf("open listings by description_source = %v, want %v", bySource, wantBySource)
		}
		for k, want := range wantBySource {
			if bySource[k] != want {
				t.Errorf("open listings with description_source %q = %d, want %d", k, bySource[k], want)
			}
		}

		byLane := groupCount(t, pool, `
			SELECT source, count(*) FROM job_listing
			WHERE closed_at IS NULL GROUP BY 1`)
		if byLane["ats"] != bySource["ats_board"] {
			t.Errorf("ats lane has %d open listings but %d marked ats_board", byLane["ats"], bySource["ats_board"])
		}
		if byLane["crawl"] != bySource["llm_summary"] {
			t.Errorf("crawl lane has %d open listings but %d marked llm_summary", byLane["crawl"], bySource["llm_summary"])
		}
	})
}
