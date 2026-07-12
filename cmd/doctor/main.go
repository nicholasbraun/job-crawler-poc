// Package main is doctor: the Catalog Doctor CLI (ADR-0011). It replays the
// current URL-structural rules over the stored Catalog and, by default, prints a
// dry-run report of the rows the rules now reject (delete / re-attribute / merge)
// plus the Companies left orphaned. With --apply it executes that plan against
// the Postgres Catalog. The rule engine lives in internal/catalogdoctor; this
// command is the thin driver -- it lists the Catalog, calls Plan, prints the
// report, and (when asked) runs Apply through a Postgres-backed Store adapter.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
)

// defaultDatabaseURL matches cmd/server's default DSN so the Doctor targets the
// same local Catalog without extra configuration.
const defaultDatabaseURL = "postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable"

func main() {
	apply := flag.Bool("apply", false, "execute the plan; default is a dry-run report")
	flag.Parse()

	// Best-effort .env load for local development (DATABASE_URL).
	_ = godotenv.Load()

	ctx := context.Background()

	dsn := envOr("DATABASE_URL", defaultDatabaseURL)
	// The Catalog is assumed already migrated (by the server); the Doctor never
	// migrates, it only reads and repairs.
	pool, err := postgres.Open(ctx, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}
	defer pool.Close()

	companies := postgres.NewCompanyRepository(pool)
	pages := postgres.NewCareerPageRepository(pool)

	pageList, err := pages.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}
	companyList, err := companies.List(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}

	result := catalogdoctor.Plan(pageList, companyList)
	printReport(os.Stdout, len(pageList), result)

	if !*apply {
		fmt.Fprintln(os.Stdout, "dry-run: re-run with --apply to execute")
		return
	}

	store := pgStore{companies: companies, pages: pages}
	if err := catalogdoctor.Apply(ctx, store, result); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "applied")
}

// printReport writes the plan to w: a per-action summary, then one line per
// non-Keep page disposition, then the orphaned Companies.
func printReport(w *os.File, total int, result catalogdoctor.Result) {
	counts := map[catalogdoctor.Action]int{}
	for _, d := range result.Pages {
		counts[d.Action]++
	}

	fmt.Fprintln(w, "catalog doctor report")
	fmt.Fprintf(w, "  career pages      %d\n", total)
	fmt.Fprintf(w, "  keep              %d\n", counts[catalogdoctor.Keep])
	fmt.Fprintf(w, "  delete            %d\n", counts[catalogdoctor.Delete])
	fmt.Fprintf(w, "  reattribute       %d\n", counts[catalogdoctor.Reattribute])
	fmt.Fprintf(w, "  merge             %d\n", counts[catalogdoctor.Merge])
	fmt.Fprintf(w, "  orphan companies  %d\n", len(result.Orphans))

	for _, d := range result.Pages {
		if d.Action == catalogdoctor.Keep {
			continue
		}
		line := fmt.Sprintf("  %-12s %s  (%s)", d.Action, d.Page.URL, d.Reason)
		if d.Action == catalogdoctor.Reattribute && d.Target != nil {
			line += "  -> " + d.Target.CompanyKey
		}
		fmt.Fprintln(w, line)
	}
	for _, c := range result.Orphans {
		fmt.Fprintf(w, "  orphan company   %s\n", c.CompanyKey)
	}
}

// pgStore adapts the separate Postgres Company and CareerPage repositories to the
// catalogdoctor.Store port.
type pgStore struct {
	companies *postgres.CompanyRepository
	pages     *postgres.CareerPageRepository
}

var _ catalogdoctor.Store = pgStore{}

func (s pgStore) UpsertCompany(ctx context.Context, c *crawler.Company) error {
	return s.companies.Upsert(ctx, c)
}

func (s pgStore) DeleteCompany(ctx context.Context, id uuid.UUID) error {
	return s.companies.Delete(ctx, id)
}

func (s pgStore) DeleteCareerPage(ctx context.Context, id uuid.UUID) error {
	return s.pages.Delete(ctx, id)
}

func (s pgStore) ReattributeCareerPage(ctx context.Context, id, companyID uuid.UUID) error {
	return s.pages.Reattribute(ctx, id, companyID)
}

// envOr returns the value of environment variable key, or fallback if it is
// unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
