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
	"strconv"
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
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/nicholasbraun/job-crawler-poc/internal/importer"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmstream"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	myotel "github.com/nicholasbraun/job-crawler-poc/internal/otel"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
	discoveryprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/discovery_processor"
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

	// redisReadyTimeout bounds how long startup waits for Redis to answer a
	// Ping, and redisReadyPollInterval is how often it retries within that
	// window. After a restart Redis replies "LOADING" until it finishes loading
	// its persisted dataset (the prior run's frontier/visited state); that is a
	// transient not-ready state, so we poll through it rather than treating the
	// first failure as fatal.
	redisReadyTimeout      = 60 * time.Second
	redisReadyPollInterval = 500 * time.Millisecond

	// Crawl tuning defaults, previously sourced from config.json. The
	// defaultDiscoveryMaxDepth constant seeds a new discovery definition's field
	// when the request omits maxDepth (overridable per definition via the API);
	// Discovery reaches deep because it is the perpetual catalog-building crawl.
	// defaultMaxWorkers is the default size of the per-run worker pools,
	// overridable via CRAWL_MAX_WORKERS.
	defaultLogLevel          = "INFO"
	defaultDiscoveryMaxDepth = 10
	defaultMaxWorkers        = 50

	// llmMaxBacklog is the high-water cap on a per-run LLM stream's outstanding
	// entries. Past it, the crawl's Enqueue blocks until the classify/extract
	// consumer group catches up, so a crawl that outruns the model applies
	// backpressure instead of growing Redis without bound (each entry carries the
	// full page content). It is a safety valve sized well above steady-state
	// backlog, so normal operation never reaches it.
	llmMaxBacklog = 5000
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Best-effort .env load for local development (LLM_API_KEY, DATABASE_URL).
	// Missing file is fine if the vars are set in the environment.
	_ = godotenv.Load()

	// The classifier/extractor talk to any OpenAI-compatible chat API. Leave
	// LLM_BASE_URL/LLM_MODEL unset for OpenRouter's defaults, or point them at a
	// local server, e.g. LLM_BASE_URL=http://localhost:11434/v1/chat/completions
	// with LLM_MODEL=qwen2.5:3b for a local Ollama. Use a non-reasoning instruct
	// model: a reasoning model (e.g. qwen3.5) runs a hidden think phase the crawler
	// discards, a large latency tax for a one-line verdict.
	//
	// LLM_CLASSIFY_MAX_CHARS / LLM_EXTRACT_MAX_CHARS cap the page text (in runes)
	// sent to the classifier and extractor. The classify/extract signal sits near
	// the top of the page, so capping keeps a local model fast and avoids timeouts
	// on huge pages.
	//
	// LLM_TIMEOUT and LLM_MAX_WORKERS default to values tuned for a local model:
	// a long per-request timeout (a laptop model generates serially, so queued
	// requests wait) and few concurrent workers (matching that serialization
	// avoids a deep server-side queue that would blow the timeout). Raise both
	// for a fast, highly-parallel cloud API. LLM_MAX_WORKERS sizes the durable LLM
	// stage's consumer group (how many goroutines drain the per-run Redis Stream
	// in parallel), not an in-process pool.
	llmTimeout, err := time.ParseDuration(envOr("LLM_TIMEOUT", "5m"))
	if err != nil {
		log.Fatalf("error parsing LLM_TIMEOUT: %v", err)
	}
	llmMaxWorkers, err := strconv.Atoi(envOr("LLM_MAX_WORKERS", "2"))
	if err != nil || llmMaxWorkers < 1 {
		log.Fatalf("error parsing LLM_MAX_WORKERS: must be a positive integer, got %q", os.Getenv("LLM_MAX_WORKERS"))
	}
	llmClassifyMaxChars, err := strconv.Atoi(envOr("LLM_CLASSIFY_MAX_CHARS", "1500"))
	if err != nil || llmClassifyMaxChars < 1 {
		log.Fatalf("error parsing LLM_CLASSIFY_MAX_CHARS: must be a positive integer, got %q", os.Getenv("LLM_CLASSIFY_MAX_CHARS"))
	}
	llmExtractMaxChars, err := strconv.Atoi(envOr("LLM_EXTRACT_MAX_CHARS", "8000"))
	if err != nil || llmExtractMaxChars < 1 {
		log.Fatalf("error parsing LLM_EXTRACT_MAX_CHARS: must be a positive integer, got %q", os.Getenv("LLM_EXTRACT_MAX_CHARS"))
	}
	llmConfig := openrouter.Config{
		APIKey:           os.Getenv("LLM_API_KEY"),
		BaseURL:          os.Getenv("LLM_BASE_URL"),
		Model:            os.Getenv("LLM_MODEL"),
		Timeout:          llmTimeout,
		ClassifyMaxChars: llmClassifyMaxChars,
		ExtractMaxChars:  llmExtractMaxChars,
	}

	// CRAWL_MAX_WORKERS sizes the per-run discovery worker pool — how many pages
	// are downloaded and processed in parallel per run. Crawl workers are
	// I/O-bound (blocked on network downloads), so this
	// can be raised well past the default to lift throughput once the frontier is
	// no longer the bottleneck; the Postgres pool and outbound network are the next
	// caps to watch.
	crawlMaxWorkers, err := strconv.Atoi(envOr("CRAWL_MAX_WORKERS", strconv.Itoa(defaultMaxWorkers)))
	if err != nil || crawlMaxWorkers < 1 {
		log.Fatalf("error parsing CRAWL_MAX_WORKERS: must be a positive integer, got %q", os.Getenv("CRAWL_MAX_WORKERS"))
	}

	// CRAWL_VISITED_CAP bounds each run's visited ZSET (ADR-0027 / #75). Read once
	// and applied to every Frontier built for a run so the FIFO cap is consistent
	// regardless of which Frontier performs the AddURL.
	visitedCap, err := strconv.Atoi(envOr("CRAWL_VISITED_CAP", strconv.Itoa(redisfrontier.DefaultVisitedCap)))
	if err != nil || visitedCap < 1 {
		log.Fatalf("error parsing CRAWL_VISITED_CAP: must be a positive integer, got %q", os.Getenv("CRAWL_VISITED_CAP"))
	}

	// ROBOTS_CACHE_SIZE / ROBOTS_CACHE_TTL bound the shared robots.txt Rules cache
	// (ADR-0032): how many hosts' parsed rules are held and for how long before a
	// re-fetch, so the cache cannot grow without limit across a discovery crawl
	// that touches tens of thousands of hosts.
	robotsCacheSize, err := strconv.Atoi(envOr("ROBOTS_CACHE_SIZE", strconv.Itoa(robotstxt.DefaultCacheSize)))
	if err != nil || robotsCacheSize < 1 {
		log.Fatalf("error parsing ROBOTS_CACHE_SIZE: must be a positive integer, got %q", os.Getenv("ROBOTS_CACHE_SIZE"))
	}
	robotsCacheTTL, err := time.ParseDuration(envOr("ROBOTS_CACHE_TTL", robotstxt.DefaultCacheTTL.String()))
	if err != nil || robotsCacheTTL <= 0 {
		log.Fatalf("error parsing ROBOTS_CACHE_TTL: must be a positive duration, got %q", os.Getenv("ROBOTS_CACHE_TTL"))
	}

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
	redisClient := redis.NewClient(&redis.Options{
		Addr: redisAddr,
		// A finite read timeout is load-bearing for the Frontier's transient-error
		// retry (ADR-0024): it converts a stalled Redis read into the retryable i/o
		// timeout the retry loop rides out. A zero ("no timeout") read would hang
		// forever and the loop could never run. 3s == the go-redis default, pinned
		// here so it is never silently disabled.
		ReadTimeout: 3 * time.Second,
	})
	if err := waitForRedis(ctx, redisClient, redisReadyTimeout); err != nil {
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

	companyRepository := postgres.NewCompanyRepository(pgPool)
	careerPageRepository := postgres.NewCareerPageRepository(pgPool)
	defRepository := postgres.NewCrawlDefinitionRepository(pgPool)
	runRepository := postgres.NewCrawlRunRepository(pgPool)
	importJobRepository := postgres.NewImportJobRepository(pgPool)

	factory := newFactory(crawlMaxWorkers, visitedCap, robotsCacheTTL, robotsCacheSize, llmMaxWorkers, llmConfig, redisClient,
		companyRepository, careerPageRepository)
	crawlRunner := runner.New(runRepository, defRepository, factory,
		// One cleaner sweeps all of a run's transient Redis state on a terminal
		// status or factory error: the frontier keys and the LLM stage's streams
		// (both kinds + dead-letter, via the llmstream:{runID}:* glob). A paused run
		// (graceful shutdown) is not terminal, so its streams survive for a resumed
		// run to redeliver.
		runner.WithFrontierCleaner(func(ctx context.Context, runID uuid.UUID) error {
			ferr := redisfrontier.DeleteRun(ctx, redisClient, runID)
			serr := llmstream.DeleteRun(ctx, redisClient, runID)
			return errors.Join(ferr, serr)
		}),
	)

	// Adopt and resume any run a previous process left running/stopping: its
	// Redis frontier and Postgres counters survived the restart. Best-effort —
	// a reconcile failure must not stop the server from serving new crawls.
	if err := crawlRunner.Reconcile(ctx); err != nil {
		slog.Error("error reconciling interrupted runs", "err", err)
	}

	catalogImporter := importer.New(importJobRepository,
		importer.WithExecutor(importer.NewMergeExecutor(companyRepository, careerPageRepository)))
	// Fail any import a previous process left mid-flight; recovery is a re-upload
	// (ADR-0014). Best-effort — a sweep failure must not stop the server.
	if err := catalogImporter.Sweep(ctx); err != nil {
		slog.Error("error sweeping interrupted import jobs", "err", err)
	}

	apiHandler := api.New(api.Config{
		Runner:      crawlRunner,
		Runs:        runRepository,
		Definitions: defRepository,
		Companies:   companyRepository,
		CareerPages: careerPageRepository,
		Importer:    catalogImporter,
		ImportJobs:  importJobRepository,
		// Frontier size is a live Redis read, kept out of the api package so it
		// stays decoupled from Redis (mirrors runner.WithFrontierCleaner).
		FrontierSizer: func(ctx context.Context, runID uuid.UUID) (int64, error) {
			return redisfrontier.Len(ctx, redisClient, runID)
		},
		// Runtime Seed injection into a Discovery Crawl's live Frontier
		// (ADR-0018). Mirrors FrontierSizer to keep the api package off Redis: a
		// fresh redisfrontier for the run shares its Redis keys, so the depth-0
		// add lands in the same Frontier the orchestrator pops from.
		FrontierSeeder: func(ctx context.Context, runID uuid.UUID, u crawler.URL) error {
			return redisfrontier.New(redisClient, runID, redisfrontier.WithVisitedCap(visitedCap)).AddURL(ctx, u)
		},
		Defaults: api.Defaults{
			DiscoveryMaxDepth: defaultDiscoveryMaxDepth,
			DiscoverySeeds:    crawler.DefaultDiscoverySeeds(),
			URLFilter:         crawler.DefaultURLFilterConfig(),
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
	// Drain any in-flight import before the pool it writes to closes.
	catalogImporter.Shutdown(shutdownCtx)
	otelShutdown(shutdownCtx)
	pgPool.Close()
	if err := redisClient.Close(); err != nil {
		slog.Error("error closing redis client", "err", err)
	}

	slog.Info("shutdown complete")
}

// newFactory builds the per-run wiring closure. Stateless dependencies
// (HTTP client, parser, robots checker, classifier, filters) are built once
// here and shared across runs; per-run state (frontier, pools) is built inside.
func newFactory(
	maxWorkers int,
	visitedCap int,
	robotsCacheTTL time.Duration,
	robotsCacheSize int,
	llmMaxWorkers int,
	llmConfig openrouter.Config,
	redisClient *redis.Client,
	companyRepository crawler.CompanyRepository,
	careerPageRepository crawler.CareerPageRepository,
) runner.Factory {
	// One caching transport (DNS cache + bounded dial + connection pool) shared by
	// page downloads and robots.txt fetches: a host resolved for its robots.txt is
	// then a cache hit for its pages, and vice versa, halving DNS load per host.
	sharedTransport := downloader.NewCachingTransport()
	httpClient := downloader.NewClient(userAgent, downloader.WithTransport(sharedTransport))
	retryHTTPClient := downloader.NewRetryClient(httpClient)
	htmlParser := parser.NewHTMLParser()

	robotsTxtParser := temoto.NewRobotsTxtParser(userAgent)
	robotsTxtDownloader := robotstxt.NewRobotsTxtDownloader(userAgent, sharedTransport)
	robotsTxtChecker := robotstxt.NewChecker(robotsTxtParser, robotsTxtDownloader,
		robotstxt.WithCacheTTL(robotsCacheTTL), robotstxt.WithCacheSize(robotsCacheSize))

	careerPageConfirmer := openrouter.NewCareerPageClassifier(llmConfig)

	// LLM-stage observability (ADR-0007 step 1): the Prometheus instruments and
	// the Redis content-duplication probe are shared across runs; each run gets
	// its own Stats + Recorder below for the end-of-run summary log.
	llmMetrics := llmobs.NewMetrics()
	llmDupProbe := llmobs.NewDupProbe(redisClient)

	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything

	// Pre-LLM gate signals (ADR-0007 step 2), shared across runs: cheap URL-path
	// checks that resolve a page's classifier/extractor verdict without a model call.
	gateConfig := crawler.DefaultLLMGateConfig()

	return func(ctx context.Context, runID uuid.UUID, def crawler.CrawlDefinition, counters *runner.Counters, shouldStop func(context.Context) bool) (*runner.Engine, error) {
		llmStats := &llmobs.Stats{}
		llmRecorder := llmobs.NewRecorder(llmMetrics, llmDupProbe, llmStats, runID.String())
		uf := def.URLFilter
		urlFilter := filter.Chain[string](
			urlfilter.BlockInvalidURLs(),
			urlfilter.AllowedTLDs(uf.AllowedTLDs...),
			urlfilter.PassSubdomains(uf.PassSubdomains...),
			urlfilter.PassPathSegments(uf.PassPathSegments...),
			urlfilter.BlockSubdomains(uf.BlockedSubdomains...),
			urlfilter.BlockPathSegments(uf.BlockedPathSegments...),
			urlfilter.BlockFileExtensions(uf.BlockedFileExtensions...),
			urlfilter.BlockHostnames(uf.BlockedHostnames...),
		)

		// Discovery is the only live crawl kind; the Keyword Crawl lane was
		// retired (ADR-0038). A stale keyword run left over from before the
		// cutover surfaces here as an unsupported-kind error and fails to
		// resume, which is the intended clean cutover.
		if def.Kind != crawler.CrawlKindDiscovery {
			return nil, fmt.Errorf("unsupported crawl kind: %q", def.Kind)
		}

		// Perpetual mode: the run stays alive after the frontier drains,
		// waiting for URLs discovered later. It ends only on a desired-state
		// stop. The Catalog (company + career_page) is filled by the
		// career-page pool.
		discoveryFrontier := redisfrontier.New(redisClient, runID,
			redisfrontier.WithMaxDepth(def.MaxDepth),
			redisfrontier.WithMode(frontier.Perpetual),
			redisfrontier.WithVisitedCap(visitedCap),
		)

		// Durable LLM stage: gate-passing candidates are XADDed onto a per-run
		// Redis Stream and drained by a consumer group into the career-page
		// processor, so the crawl never blocks on the classifier and a crash or
		// restart redelivers rather than loses the candidate (ADR-0007 step 4).
		classifyStage := llmstream.NewStage(redisClient, runID, llmobs.KindClassify,
			func() processor.Processor[crawler.RawCareerPage] {
				return careerpageprocessor.NewProcessor(&careerpageprocessor.Config{
					CompanyRepository:    companyRepository,
					CareerPageRepository: careerPageRepository,
					Confirmer:            careerPageConfirmer,
					Recorder:             llmRecorder,
				})
			},
			llmstream.WithWorkers[crawler.RawCareerPage](llmMaxWorkers),
			llmstream.WithRecorder[crawler.RawCareerPage](llmRecorder),
			llmstream.WithMaxBacklog[crawler.RawCareerPage](llmMaxBacklog),
			// The first-reclaim window must exceed the whole Process: a single
			// Confirm is bounded by the http client's LLM_TIMEOUT, and the extra
			// minute absorbs the follow-on catalog upsert, so a slow but alive
			// worker still in flight is never reclaimed and double-called. Only a
			// truly dead worker sits idle longer. Retries of an already-failed
			// entry are paced by the shorter default reclaim interval.
			llmstream.WithMinIdle[crawler.RawCareerPage](llmConfig.Timeout+time.Minute),
		)
		if err := classifyStage.Start(ctx); err != nil {
			return nil, fmt.Errorf("starting classify stage: %w", err)
		}

		// Counter tap: a gate-passing page becomes a catalog candidate.
		// ListingsFound is reused as "catalog entries found" in Step 5. Counted
		// once here on enqueue (not per process) so a redelivery does not
		// double-count.
		onCareerPage := func(ctx context.Context, page *crawler.RawCareerPage) error {
			counters.ListingsFound.Add(1)
			return classifyStage.Enqueue(ctx, page)
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
					GateConfig:       gateConfig,
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
			// Discovery roams: seeds carry empty Scope/Owner provenance
			// (ADR-0021), so no fence and no Catalog attribution is applied.
			Seeds: crawler.SeedsFromURLs(def.SeedURLs),
			// Close order: discovery pool first (the producer feeding the
			// classify stage), then the stage. Closing the producer first means
			// no new task is XADDed mid-drain; a clean finish then drains the
			// stream to empty, while a stop/shutdown leaves the PEL for resume.
			Close: func() {
				discoveryWorkerPool.Close()
				classifyStage.Close()
				// All LLM calls for this run are done once the stage drains;
				// emit the ADR-0007 measurement summary (ADR-0007 step 1).
				slog.Info("runner: llm stage summary", append([]any{"run_id", runID}, llmStats.Summary()...)...)
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

// waitForRedis blocks until Redis answers a Ping or timeout elapses, retrying
// every redisReadyPollInterval. It tolerates the transient state right after a
// restart, when Redis replies "LOADING" while it loads its persisted dataset:
// go-redis retries LOADING internally but only within a sub-second backoff
// budget, too small for a sizable frontier/visited dump. On timeout (or if the
// parent ctx is cancelled) it returns the last Ping error.
func waitForRedis(ctx context.Context, client *redis.Client, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for {
		err := client.Ping(ctx).Err()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
		slog.Info("waiting for redis to become ready", "err", err)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(redisReadyPollInterval):
		}
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
