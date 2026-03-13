// Package main is responsible for:
//
// - parsng args (seed urls)
// - running the crawl orchestrator
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/sqlite"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier/inmem"
	"github.com/nicholasbraun/job-crawler-poc/internal/http"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// seed URLs from command line args
	seedURLsPtr := flag.String("seedURLs", "https://news.ycombinator.com/jobs", "comma separated list of seedURLs")
	dbPath := flag.String("db", "./data/database.db", "path to sqlite database")
	maxDepth := flag.Int("maxDepth", 2, "maximum depth a URL should be crawled")
	flag.Parse()

	seedURLs := strings.Split(*seedURLsPtr, ",")

	// create SQLite DB + setup
	db, err := sqlite.Open(*dbPath)
	if err != nil {
		log.Fatalf("error opening db with path: %s. %v", *dbPath, err)
	}
	err = sqlite.Setup(ctx, db)
	if err != nil {
		log.Fatalf("error setting up db: %v", err)
	}

	// create repositories
	urlRepository := sqlite.NewURLRepository(db)
	jobRepository := sqlite.NewJobRepository(db)

	// create frontier
	frontier := inmem.NewFrontier(inmem.WithMaxDomains(10))

	// create HTTP client + retry wrapper
	httpClient := http.NewClient()
	retryHTTPClient := http.NewRetryClient(httpClient)

	// create parser
	parser := parser.NewHTMLParser()

	// create filter chains (start with pass-through filters)
	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything
	urlFilter := filter.Chain[*crawler.URL]()
	relevanceFilter := filter.Chain[*crawler.Content]()

	// create orchestrator
	cfg := orchestrator.Config{
		Frontier:        frontier,
		Downloader:      retryHTTPClient,
		Parser:          parser,
		URLRepository:   urlRepository,
		JobRepository:   jobRepository,
		ContentFilter:   contentFilter,
		URLFilter:       urlFilter,
		RelevanceFilter: relevanceFilter,
		MaxDepth:        *maxDepth,
	}
	o := orchestrator.NewOrchestrator(cfg)

	// run
	err = o.Run(ctx, seedURLs)
	if err != nil {
		log.Fatalf("crawl failed: %v", err)
	}
	slog.Info("crawl complete")
}
