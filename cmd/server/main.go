// Package main is the long-running crawler server. It serves the REST API and
// the embedded React dashboard on :8080, and manages crawl runs via the runner.
// It never log.Fatal/os.Exit after it starts serving: SIGINT drains active runs
// (desired-state stop) before the process exits.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	joblistingfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/job_listing_filter"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	myotel "github.com/nicholasbraun/job-crawler-poc/internal/otel"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
	discoveryprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/discovery_processor"
	joblistingprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/job_listing_processor"
	urlprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/url_processor"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt/temoto"
	"github.com/nicholasbraun/job-crawler-poc/internal/runner"
	"github.com/nicholasbraun/job-crawler-poc/web"
	"github.com/redis/go-redis/v9"
)

const (
	userAgent          = "JobCrawlerBot/0.1 (+https://github.com/nicholasbraun/job-crawler-poc)"
	serverAddr         = ":8080"
	defaultDatabaseURL = "postgres://crawler:crawler@localhost:5432/crawler?sslmode=disable"
	defaultRedisAddr   = "localhost:6379"
	shutdownTimeout    = 30 * time.Second

	// Crawl tuning defaults, previously sourced from config.json. maxDepth and
	// maxDomains seed a new crawl definition's fields (overridable per
	// definition via the API); maxWorkers sizes the per-run worker pools.
	defaultLogLevel   = "INFO"
	defaultMaxDepth   = 4
	defaultMaxWorkers = 50
	defaultMaxDomains = 10000
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Best-effort .env load for local development (OPENROUTER_API_KEY,
	// DATABASE_URL). Missing file is fine if the vars are set in the environment.
	_ = godotenv.Load()
	openrouterAPIKey := os.Getenv("OPENROUTER_API_KEY")

	var logLevel slog.LevelVar
	if err := logLevel.UnmarshalText([]byte(envOr("LOG_LEVEL", defaultLogLevel))); err != nil {
		log.Fatalf("error parsing LOG_LEVEL: %v", err)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &logLevel})))

	otelShutdown, err := myotel.Setup(ctx)
	if err != nil {
		log.Fatalf("error setting up otel: %v", err)
	}

	// Redis holds transient per-run crawl state: the frontier queues, the
	// visited set, and in-flight leases (keyed per run, so runs are resumable).
	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = defaultRedisAddr
	}
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Fatalf("error connecting to redis at %s: %v", redisAddr, err)
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

	jobListingRepository := postgres.NewJobListingRepository(pgPool)
	companyRepository := postgres.NewCompanyRepository(pgPool)
	careerPageRepository := postgres.NewCareerPageRepository(pgPool)
	defRepository := postgres.NewCrawlDefinitionRepository(pgPool)
	runRepository := postgres.NewCrawlRunRepository(pgPool)

	factory := newFactory(defaultMaxWorkers, openrouterAPIKey, redisClient,
		jobListingRepository, companyRepository, careerPageRepository)
	crawlRunner := runner.New(runRepository, defRepository, factory,
		runner.WithFrontierCleaner(func(ctx context.Context, runID uuid.UUID) error {
			return redisfrontier.DeleteRun(ctx, redisClient, runID)
		}),
	)

	// Adopt and resume any run a previous process left running/stopping: its
	// Redis frontier and Postgres counters survived the restart. Best-effort —
	// a reconcile failure must not stop the server from serving new crawls.
	if err := crawlRunner.Reconcile(ctx); err != nil {
		slog.Error("error reconciling interrupted runs", "err", err)
	}

	apiHandler := api.New(api.Config{
		Runner:      crawlRunner,
		Runs:        runRepository,
		Definitions: defRepository,
		Companies:   companyRepository,
		CareerPages: careerPageRepository,
		Listings:    jobListingRepository,
		// Frontier size is a live Redis read, kept out of the api package so it
		// stays decoupled from Redis (mirrors runner.WithFrontierCleaner).
		FrontierSizer: func(ctx context.Context, runID uuid.UUID) (int64, error) {
			return redisfrontier.Len(ctx, redisClient, runID)
		},
		Defaults: api.Defaults{
			MaxDepth:   defaultMaxDepth,
			MaxDomains: defaultMaxDomains,
			URLFilter:  crawler.DefaultURLFilterConfig(),
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

	// Stop accepting new HTTP requests (and wait for in-flight handlers) before
	// draining runs, so no run can be created mid-drain and race wg.Add against
	// the in-progress wg.Wait inside crawlRunner.Shutdown.
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("error shutting down http server", "err", err)
	}
	crawlRunner.Shutdown(shutdownCtx)
	otelShutdown(shutdownCtx)
	pgPool.Close()
	if err := redisClient.Close(); err != nil {
		slog.Error("error closing redis client", "err", err)
	}

	slog.Info("shutdown complete")
}

// newFactory builds the per-run wiring closure. Stateless dependencies
// (HTTP client, parser, robots checker, extractor, filters) are built once
// here and shared across runs; per-run state (frontier, pools) is built inside.
func newFactory(
	maxWorkers int,
	openrouterAPIKey string,
	redisClient *redis.Client,
	jobListingRepository crawler.JobListingRepository,
	companyRepository crawler.CompanyRepository,
	careerPageRepository crawler.CareerPageRepository,
) runner.Factory {
	httpClient := downloader.NewClient(userAgent)
	retryHTTPClient := downloader.NewRetryClient(httpClient)
	htmlParser := parser.NewHTMLParser()

	robotsTxtParser := temoto.NewRobotsTxtParser(userAgent)
	robotsTxtDownloader := robotstxt.NewRobotsTxtDownloader(userAgent)
	robotsTxtChecker := robotstxt.NewChecker(robotsTxtParser, robotsTxtDownloader)

	jobListingExtractor := openrouter.NewJobListingExtractor(openrouterAPIKey)
	careerPageConfirmer := openrouter.NewCareerPageClassifier(openrouterAPIKey)

	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything

	return func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *runner.Counters, shouldStop func(context.Context) bool) (*runner.Engine, error) {
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

		if def.Kind == crawler.CrawlKindDiscovery {
			// Perpetual mode: the run stays alive after the frontier drains,
			// waiting for URLs discovered later. It ends only on a desired-state
			// stop. The Catalog (company + career_page) is filled by the
			// career-page pool.
			discoveryFrontier := redisfrontier.New(redisClient, runID,
				redisfrontier.WithMaxDomains(def.MaxDomains),
				redisfrontier.WithMaxDepth(def.MaxDepth),
				redisfrontier.WithMode(frontier.Perpetual),
			)

			careerPageWorkerPool := pool.NewPool(
				ctx, "career_page_worker_pool", func() processor.Processor[crawler.RawCareerPage] {
					return careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
						CompanyRepository:    companyRepository,
						CareerPageRepository: careerPageRepository,
						Confirmer:            careerPageConfirmer,
					})
				})

			// Counter tap: a gate-passing page becomes a catalog candidate.
			// ListingsFound is reused as "catalog entries found" in Step 5.
			onCareerPage := func(ctx context.Context, page *crawler.RawCareerPage) error {
				counters.ListingsFound.Add(1)
				return careerPageWorkerPool.Enqueue(ctx, page)
			}

			discoveryWorkerPool := pool.NewPool(
				ctx, "discovery_worker_pool", func() processor.Processor[crawler.URL] {
					return discoveryprocessor.NewProcessor(&discoveryprocessor.Config{
						Frontier:         discoveryFrontier,
						Downloader:       retryHTTPClient,
						Parser:           htmlParser,
						ContentFilter:    contentFilter,
						URLFilter:        urlFilter,
						RobotsTxtChecker: robotsTxtChecker,
						OnCareerPage:     onCareerPage,
					})
				}, pool.WithMaxWorkers[crawler.URL](maxWorkers))

			onNextURL := func(ctx context.Context, u *crawler.URL) error {
				counters.PagesCrawled.Add(1)
				return discoveryWorkerPool.Enqueue(ctx, u)
			}

			o := orchestrator.NewOrchestrator(orchestrator.Config{
				Frontier:   discoveryFrontier,
				OnNextURL:  onNextURL,
				ShouldStop: shouldStop,
			})

			return &runner.Engine{
				Orchestrator: o,
				SeedURLs:     def.SeedURLs,
				// Close order: discovery pool first (its workers feed the
				// career-page pool), then the career-page pool. Reversing loses
				// in-flight candidates.
				Close: func() {
					discoveryWorkerPool.Close()
					careerPageWorkerPool.Close()
				},
			}, nil
		}

		// Keyword: bounded run that finishes when the frontier drains,
		// extracting Job Listings via the LLM. Unknown kinds are rejected at the
		// API, so this is the only other real kind.
		if def.Kind != crawler.CrawlKindKeyword {
			return nil, fmt.Errorf("unsupported crawl kind: %q", def.Kind)
		}

		// Seed a Keyword Crawl from the Catalog: every Career Page the Discovery
		// Crawl catalogued. On re-adoption (Reconcile) re-seeding is a no-op --
		// the per-run Redis visited set survived the restart and dedups.
		seedURLs, err := careerPageRepository.ListURLs(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving keyword crawl seeds from catalog: %w", err)
		}

		// Multi-keyword OR relevance filter built from the Definition: a page is
		// relevant if its title OR main content contains any keyword. Prunes
		// pages before the expensive LLM extraction (ADR-0004). A title match
		// short-circuits via ErrPass; else fall through to the content check;
		// else Reject.
		keywordFilter := filter.Chain(
			joblistingfilter.TitleContains(filter.Contains(def.Keywords...)),
			joblistingfilter.MainContentContains(filter.Contains(def.Keywords...)),
			filter.Reject[*crawler.Content](),
		)

		boundedFrontier := redisfrontier.New(redisClient, runID,
			redisfrontier.WithMaxDomains(def.MaxDomains),
			redisfrontier.WithMaxDepth(def.MaxDepth),
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
					Frontier:         boundedFrontier,
					Downloader:       retryHTTPClient,
					Parser:           htmlParser,
					ContentFilter:    contentFilter,
					URLFilter:        urlFilter,
					RobotsTxtChecker: robotsTxtChecker,
					RelevanceFilter:  keywordFilter,
					OnJobListing:     onJobListing,
				})
			}, pool.WithMaxWorkers[crawler.URL](maxWorkers))

		// Counter tap: a URL pulled from the frontier and dispatched to a worker.
		onNextURL := func(ctx context.Context, u *crawler.URL) error {
			counters.PagesCrawled.Add(1)
			return urlWorkerPool.Enqueue(ctx, u)
		}

		o := orchestrator.NewOrchestrator(orchestrator.Config{
			Frontier:   boundedFrontier,
			OnNextURL:  onNextURL,
			ShouldStop: shouldStop,
		})

		return &runner.Engine{
			Orchestrator: o,
			SeedURLs:     seedURLs,
			// Close order: url pool first (its workers feed the job_listing
			// pool), then the job_listing pool. Reversing loses listings.
			Close: func() {
				urlWorkerPool.Close()
				jobListingWorkerPool.Close()
			},
		}, nil
	}
}

// envOr returns the value of environment variable key, or fallback if it is
// unset or empty.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
