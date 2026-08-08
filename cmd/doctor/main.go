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
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalogdoctor"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
	"github.com/nicholasbraun/job-crawler-poc/internal/pgenv"
)

func main() {
	apply := flag.Bool("apply", false, "execute the plan; default is a dry-run report")
	flag.Parse()

	// Best-effort .env load for local development (DATABASE_URL).
	_ = godotenv.Load()

	// Cancel in-flight DB work on Ctrl-C / SIGTERM so a long --apply against the
	// live Catalog can be interrupted without leaving a stuck connection. Apply is
	// idempotent and FK-ordered, so an interrupted run converges on a clean re-run.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dsn := pgenv.EnvOr("DATABASE_URL", pgenv.DefaultDatabaseURL)
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
	corpus := postgres.NewCorpusRepository(pool)

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

	// Count the Job Listings riding on each page the plan touches. Plan is pure, so
	// this read lives here in the driver -- and it is worth the query: a plan that
	// deletes a page owning listings is exactly the one to inspect before --apply.
	affected := []uuid.UUID{}
	for _, d := range result.Pages {
		if d.Action != catalogdoctor.Keep {
			affected = append(affected, d.Page.ID)
		}
	}
	listingCounts, err := corpus.CountByCareerPages(ctx, affected)
	if err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}
	printReport(os.Stdout, len(pageList), result, listingCounts)

	if !*apply {
		fmt.Fprintln(os.Stdout, "dry-run: re-run with --apply to execute")
		return
	}

	store := postgres.NewCatalogDoctorStore(companies, pages, corpus)
	if err := catalogdoctor.Apply(ctx, store, result); err != nil {
		fmt.Fprintf(os.Stderr, "doctor: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "applied")
}

// printReport writes the plan to w: a per-action summary, then one line per
// non-Keep page disposition, then the orphaned Companies. A disposition that
// carries Job Listings is annotated with how many and what becomes of them, so the
// Corpus cost of a plan is legible before --apply rather than discovered after.
// listingCounts is keyed by Career Page id; a page absent from it owns none.
func printReport(w io.Writer, total int, result catalogdoctor.Result, listingCounts map[uuid.UUID]int) {
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
		if n := listingCounts[d.Page.ID]; n > 0 {
			switch d.Action {
			case catalogdoctor.Delete:
				line += fmt.Sprintf("  [DELETES %d listings]", n)
			case catalogdoctor.Merge:
				line += fmt.Sprintf("  [moves %d listings]", n)
			default:
				line += fmt.Sprintf("  [%d listings]", n)
			}
		}
		fmt.Fprintln(w, line)
	}
	for _, c := range result.Orphans {
		fmt.Fprintf(w, "  orphan company   %s\n", c.CompanyKey)
	}
}
