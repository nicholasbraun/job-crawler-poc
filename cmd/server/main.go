// Package main is the long-running crawler server. It serves the REST API and
// the embedded React dashboard on :8080, and manages crawl runs via the runner.
// Unlike cmd/cli, it never log.Fatal/os.Exit after it starts serving: SIGINT
// drains active runs (desired-state stop) before the process exits.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
	"github.com/nicholasbraun/job-crawler-poc/internal/config"
	jsonloader "github.com/nicholasbraun/job-crawler-poc/internal/config/json_loader"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/sqlite"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	joblistingfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/job_listing_filter"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier/inmem"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	myotel "github.com/nicholasbraun/job-crawler-poc/internal/otel"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	joblistingprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/job_listing_processor"
	urlprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/url_processor"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt/temoto"
	"github.com/nicholasbraun/job-crawler-poc/internal/runner"
	"github.com/nicholasbraun/job-crawler-poc/web"
)

const (
	userAgent          = "JobCrawlerBot/0.1 (+https://github.com/nicholasbraun/job-crawler-poc)"
	serverAddr         = ":8080"
	defaultDatabaseURL = "postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable"
	shutdownTimeout    = 30 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Best-effort .env load for local development (OPENROUTER_API_KEY,
	// DATABASE_URL). Missing file is fine if the vars are set in the environment.
	_ = godotenv.Load()
	openrouterAPIKey := os.Getenv("OPENROUTER_API_KEY")

	jsonConfigLoader := jsonloader.NewJSONLoader("config.json")
	cfg, err := jsonConfigLoader.Load(ctx)
	if err != nil {
		log.Fatalf("error loading config: %v", err)
	}

	var logLevel slog.LevelVar
	if err := logLevel.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		log.Fatalf("error parsing logLevel from config: %v", err)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &logLevel})))

	otelShutdown, err := myotel.Setup(ctx)
	if err != nil {
		log.Fatalf("error setting up otel: %v", err)
	}

	// SQLite still backs the visited-URL set and extracted listings (verbatim).
	db, err := sqlite.Open("./data/database.db")
	if err != nil {
		log.Fatalf("error opening sqlite db: %v", err)
	}
	if err := sqlite.Setup(ctx, db); err != nil {
		log.Fatalf("error setting up sqlite db: %v", err)
	}

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultDatabaseURL
	}
	if err := postgres.Migrate(ctx, databaseURL); err != nil {
		log.Fatalf("error applying postgres migrations: %v", err)
	}
	pgPool, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		log.Fatalf("error opening postgres: %v", err)
	}

	urlRepository := sqlite.NewURLRepository(db)
	jobListingRepository := postgres.NewJobListingRepository(pgPool)
	defRepository := postgres.NewCrawlDefinitionRepository(pgPool)
	runRepository := postgres.NewCrawlRunRepository(pgPool)

	factory := newFactory(cfg, openrouterAPIKey, urlRepository, jobListingRepository)
	crawlRunner := runner.New(runRepository, defRepository, factory)

	apiHandler := api.New(crawlRunner, runRepository, defRepository, api.Defaults{
		MaxDepth:   cfg.MaxDepth,
		MaxDomains: cfg.MaxDomains,
		URLFilter: crawler.URLFilterConfig{
			AllowedTLDs:         cfg.AllowedTLDs,
			BlockedSubdomains:   cfg.BlockedSubdomains,
			BlockedPathSegments: cfg.BlockedPathSegments,
			BlockedHostnames:    cfg.BlockedHostnames,
			PassSubdomains:      cfg.PassSubdomains,
			PassPathSegments:    cfg.PassPathSegments,
		},
	})

	handler, err := spaHandler(apiHandler.Routes())
	if err != nil {
		log.Fatalf("error building web handler: %v", err)
	}

	srv := &http.Server{Addr: serverAddr, Handler: handler}
	go func() {
		slog.Info("serving api + dashboard", "addr", serverAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server error", "err", err)
			stop() // trigger graceful shutdown
		}
	}()

	// Block until a signal (or a fatal server error) cancels the context. From
	// here on, no os.Exit/log.Fatal: drain runs, then close resources.
	<-ctx.Done()
	slog.Info("shutdown signal received, draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	crawlRunner.Shutdown(shutdownCtx)
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("error shutting down http server", "err", err)
	}
	otelShutdown(shutdownCtx)
	pgPool.Close()
	if err := db.Close(); err != nil {
		slog.Error("error closing sqlite db", "err", err)
	}

	slog.Info("shutdown complete")
}

// newFactory builds the per-run wiring closure. Stateless dependencies
// (HTTP client, parser, robots checker, extractor, filters) are built once
// here and shared across runs; per-run state (frontier, pools) is built inside.
func newFactory(
	cfg *config.Config,
	openrouterAPIKey string,
	urlRepository crawler.URLRepository,
	jobListingRepository crawler.JobListingRepository,
) runner.Factory {
	httpClient := downloader.NewClient(userAgent)
	retryHTTPClient := downloader.NewRetryClient(httpClient)
	htmlParser := parser.NewHTMLParser()

	robotsTxtParser := temoto.NewRobotsTxtParser(userAgent)
	robotsTxtDownloader := robotstxt.NewRobotsTxtDownloader(userAgent)
	robotsTxtChecker := robotstxt.NewChecker(robotsTxtParser, robotsTxtDownloader)

	jobListingExtractor := openrouter.NewJobListingExtractor(openrouterAPIKey)

	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything
	relevanceFilter := filter.Chain(
		filter.Every(joblistingfilter.TitleContains(
			filter.Contains("developer", "engineer", "entwickler"),
			filter.Contains("golang", "go", "backend", "software"),
		),
			joblistingfilter.MainContentContains(
				filter.Contains("apply", "bewerben"),
				filter.Contains("golang", "go"),
				filter.Contains("experience", "erfahrung", "years", "jahre"),
			),
		),
		filter.Reject[*crawler.Content](),
	)

	return func(ctx context.Context, def crawler.CrawlDefinition, counters *runner.Counters, shouldStop func(context.Context) bool) (*runner.Engine, error) {
		frontier := inmem.NewFrontier(
			inmem.WithMaxDomains(def.MaxDomains),
			inmem.WithMaxDepth(def.MaxDepth),
		)

		uf := def.URLFilter
		urlFilter := filter.Chain[string](
			urlfilter.BlockInvalidURLs(),
			urlfilter.AllowedTLDs(uf.AllowedTLDs...),
			urlfilter.PassSubdomains(uf.PassSubdomains...),
			urlfilter.PassPathSegments(uf.PassPathSegments...),
			urlfilter.BlockSubdomains(uf.BlockedSubdomains...),
			urlfilter.BlockPathSegments(uf.BlockedPathSegments...),
			urlfilter.BlockHostnames(uf.BlockedHostnames...),
		)

		jobListingWorkerPool := pool.NewPool(
			ctx, "job_listing_worker_pool", func() processor.Processor[crawler.RawJobListing] {
				return joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
					JobListingRepository: jobListingRepository,
					JobListingExtractor:  jobListingExtractor,
					DefinitionID:         def.ID,
				})
			})

		// Counter tap: a matching page found becomes a job listing.
		onJobListing := func(ctx context.Context, jl *crawler.RawJobListing) error {
			counters.ListingsFound.Add(1)
			return jobListingWorkerPool.Enqueue(ctx, jl)
		}

		urlWorkerPool := pool.NewPool(
			ctx, "url_worker_pool", func() processor.Processor[crawler.URL] {
				return urlprocessor.NewProcessor(&urlprocessor.Config{
					Frontier:         frontier,
					Downloader:       retryHTTPClient,
					Parser:           htmlParser,
					URLRepository:    urlRepository,
					ContentFilter:    contentFilter,
					URLFilter:        urlFilter,
					RobotsTxtChecker: robotsTxtChecker,
					RelevanceFilter:  relevanceFilter,
					OnJobListing:     onJobListing,
				})
			}, pool.WithMaxWorkers[crawler.URL](cfg.MaxWorkers))

		// Counter tap: a URL pulled from the frontier and dispatched to a worker.
		onNextURL := func(ctx context.Context, u *crawler.URL) error {
			counters.PagesCrawled.Add(1)
			return urlWorkerPool.Enqueue(ctx, u)
		}

		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:      frontier,
			URLRepository: urlRepository,
			OnNextURL:     onNextURL,
			ShouldStop:    shouldStop,
		})

		return &runner.Engine{
			Orchestrator: o,
			SeedURLs:     def.SeedURLs,
			// Close order: url pool first (its workers feed the job_listing
			// pool), then the job_listing pool. Reversing loses listings.
			Close: func() {
				urlWorkerPool.Close()
				jobListingWorkerPool.Close()
			},
		}, nil
	}
}

// spaHandler serves the API under /api and the embedded SPA everywhere else,
// falling back to index.html for client-side routes.
func spaHandler(apiHandler http.Handler) (http.Handler, error) {
	dist, err := fs.Sub(web.DistFS, "dist")
	if err != nil {
		return nil, err
	}
	fileServer := http.FileServer(http.FS(dist))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			apiHandler.ServeHTTP(w, r)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err != nil {
			// Unknown path → let the SPA router handle it via index.html.
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	}), nil
}
