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
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/api"
	"github.com/nicholasbraun/job-crawler-poc/internal/ats"
	"github.com/nicholasbraun/job-crawler-poc/internal/atsingest"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/collection"
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

	// ATS Fetch lane tuning (ADR-0022), shared by every Collection Cycle:
	// defaultATSMaxWorkers sizes the ingest pool (how many tenants are fetched in
	// parallel across providers), and defaultATSRateInterval is the minimum spacing
	// between board-API calls to one provider (the per-provider HostLimiter).
	defaultATSMaxWorkers   = 8
	defaultATSRateInterval = 250 * time.Millisecond

	// llmMaxBacklog is the high-water cap on a per-run LLM stream's outstanding
	// entries. Past it, the crawl's Enqueue blocks until the classify/extract
	// consumer group catches up, so a crawl that outruns the model applies
	// backpressure instead of growing Redis without bound (each entry carries the
	// full page content). It is a safety valve sized well above steady-state
	// backlog, so normal operation never reaches it.
	llmMaxBacklog = 5000
)

// The collection scheduler's narrow ports are satisfied by the concrete runner
// and Postgres run repository wired below (ADR-0036).
var (
	_ collection.Starter         = (*runner.Runner)(nil)
	_ collection.LatestRunLookup = (*postgres.CrawlRunRepository)(nil)
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

	// COLLECTION_INTERVAL is the Collection Cycle cadence (ADR-0036): the minimum
	// time between Cycle starts. Default daily. COLLECTION_ENABLED (default true)
	// is the disable flag -- set it false to stop the scheduler from starting
	// Cycles (manual starts via the API still work).
	collectionInterval, err := time.ParseDuration(envOr("COLLECTION_INTERVAL", "24h"))
	if err != nil || collectionInterval <= 0 {
		log.Fatalf("error parsing COLLECTION_INTERVAL: must be a positive duration, got %q", os.Getenv("COLLECTION_INTERVAL"))
	}
	collectionEnabled, err := strconv.ParseBool(envOr("COLLECTION_ENABLED", "true"))
	if err != nil {
		log.Fatalf("error parsing COLLECTION_ENABLED: must be a boolean, got %q", os.Getenv("COLLECTION_ENABLED"))
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
	corpusRepository := postgres.NewCorpusRepository(pgPool)
	defRepository := postgres.NewCrawlDefinitionRepository(pgPool)
	runRepository := postgres.NewCrawlRunRepository(pgPool)
	importJobRepository := postgres.NewImportJobRepository(pgPool)
	savedSearchRepository := postgres.NewSavedSearchRepository(pgPool)

	factory := newFactory(crawlMaxWorkers, visitedCap, robotsCacheTTL, robotsCacheSize, llmMaxWorkers, llmConfig, redisClient,
		companyRepository, careerPageRepository, corpusRepository)
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

	// Poll-based Collection scheduler (ADR-0036): starts a whole-Catalog Cycle on
	// a cadence, deriving due-state from persisted run rows so it survives
	// restarts. Overlap is prevented by the one-active-run invariant
	// (ErrActiveRunExists). The ctx from signal.NotifyContext stops it on
	// shutdown; running Cycles are drained by crawlRunner.Shutdown, so the loop
	// needs no separate drain.
	if collectionEnabled {
		scheduler := collection.NewScheduler(collection.Config{
			Runs:         runRepository,
			Starter:      crawlRunner,
			DefinitionID: crawler.CollectionDefinitionID,
			Interval:     collectionInterval,
		})
		go scheduler.Run(ctx)
		slog.Info("collection scheduler started", "interval", collectionInterval)
	} else {
		slog.Info("collection scheduler disabled (COLLECTION_ENABLED=false)")
	}

	apiHandler := api.New(api.Config{
		Runner:      crawlRunner,
		Runs:        runRepository,
		Definitions: defRepository,
		Companies:   companyRepository,
		CareerPages: careerPageRepository,
		Importer:    catalogImporter,
		ImportJobs:  importJobRepository,
		// SavedSearches CRUD + their Corpus results (ADR-0037). The corpus repository
		// already satisfies CorpusSearchRepository (see its interface assertions).
		SavedSearches: savedSearchRepository,
		Search:        corpusRepository,
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
// (HTTP client, parser, robots checker, classifier, extractor, ATS registry,
// filters) are built once here and shared across runs; per-run state (frontier,
// pools) is built inside.
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
	corpusRepository *postgres.CorpusRepository,
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
	jobListingExtractor := openrouter.NewJobListingExtractor(llmConfig)

	// ATS Fetch lane (ADR-0022): the provider→board-API-client registry, shared
	// across Collection Cycles. Its clients use their own board-API HTTP client
	// (separate from the crawl downloader); the per-run lane paces them via a
	// HostLimiter.
	atsRegistry := ats.NewDefaultRegistry()

	// Collection Crawl instruments (ADR-0036), shared across Cycles: found /
	// refreshed / closed listings, boards fetched / incomplete, cycle duration.
	collectionMetrics := collection.NewMetrics()

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

		// Two live crawl kinds: discovery (perpetual catalog walk) and collection
		// (periodic whole-Catalog Corpus fill + liveness, ADR-0036). Any other kind
		// — e.g. a stale keyword run left from before the cutover (ADR-0038) —
		// surfaces here as an unsupported-kind error and fails to resume, the
		// intended clean cutover.
		switch def.Kind {
		case crawler.CrawlKindDiscovery:
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

		case crawler.CrawlKindCollection:
			// One Collection Cycle (ADR-0035/0036): a bounded run that seeds from the
			// whole Catalog, fills the Corpus via two lanes (ATS fetch + crawl walk),
			// and keeps listings live (per-listing refetch + ATS absence-sweep +
			// Career-Page dormancy). It reuses the CrawlRun machinery verbatim; only
			// the engine wiring differs. Started via the existing startRun path against
			// CollectionDefinitionID; pause/resume/reconcile are kind-agnostic.

			// Seed from the Catalog: every non-dormant Career Page (carrying its
			// career_page.id) plus each Pageless Company's Website (no page, Nil id).
			pageSeeds, err := careerPageRepository.ListCollectionSeeds(ctx, crawler.DefaultPageDormancyThreshold)
			if err != nil {
				return nil, fmt.Errorf("resolving collection career-page seeds: %w", err)
			}
			pagelessSeeds, err := companyRepository.ListPagelessSeeds(ctx)
			if err != nil {
				return nil, fmt.Errorf("resolving collection pageless seeds: %w", err)
			}
			allSeeds := make([]crawler.CollectionSeed, 0, len(pageSeeds)+len(pagelessSeeds))
			allSeeds = append(allSeeds, pageSeeds...)
			for _, s := range pagelessSeeds {
				allSeeds = append(allSeeds, crawler.CollectionSeed{URL: s.URL, CompanyKey: s.CompanyKey})
			}

			// Route into the three lanes: crawl seeds (walk), ATS FetchTasks (direct
			// board pull, carrying career_page_id), and the crawled Career Pages the
			// refetch + dormancy lane owns.
			hasATSFetcher := func(provider string) bool {
				_, ok := atsRegistry.Fetcher(provider)
				return ok
			}
			crawlSeeds, atsTasks, refetchPages := collection.RouteSeeds(allSeeds, hasATSFetcher)

			// Per-run attribution snapshots (ADR-0021/0035): the CompanyKey → name map
			// for Owner attribution, and the Owner → best-match Career Page attributor
			// that stamps career_page_id onto crawled postings.
			companies, err := companyRepository.List(ctx)
			if err != nil {
				return nil, fmt.Errorf("resolving collection company snapshot: %w", err)
			}
			companySnapshot := catalog.NewCompanySnapshot(companies)
			companyKeyByID := make(map[uuid.UUID]string, len(companies))
			for _, c := range companies {
				companyKeyByID[c.ID] = c.CompanyKey
			}
			pages, err := careerPageRepository.List(ctx)
			if err != nil {
				return nil, fmt.Errorf("resolving collection career-page snapshot: %w", err)
			}
			attributor := collection.NewAttributor(pages, companyKeyByID)

			// Bounded frontier: a Cycle finishes when the walk drains (unlike the
			// perpetual Discovery frontier).
			boundedFrontier := redisfrontier.New(redisClient, runID,
				redisfrontier.WithMaxDepth(def.MaxDepth),
				redisfrontier.WithVisitedCap(visitedCap),
			)

			// Visited pre-pass (ADR-0035): seed every known-open posting URL into the
			// run's visited set so the walk surfaces only NEW postings; the refetch lane
			// owns liveness of the known ones. Idempotent, so a resumed Cycle re-runs it
			// harmlessly. Best-effort: a seeding error is logged, never fatal.
			if err := collection.SeedVisited(ctx, boundedFrontier, corpusRepository, refetchPages); err != nil {
				slog.Error("collection: seeding visited set", "err", err, "run_id", runID)
			}

			cycleStart := time.Now()

			// Durable extract stage: changed/discovered pages are XADDed onto a per-run
			// Redis Stream and drained into the job-listing extractor, so the Cycle never
			// blocks on the model and a restart redelivers (ADR-0007 step 4).
			extractStage := llmstream.NewStage(redisClient, runID, llmobs.KindExtract,
				func() processor.Processor[crawler.RawJobListing] {
					return joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
						Corpus:              corpusRepository,
						JobListingExtractor: jobListingExtractor,
						Recorder:            llmRecorder,
						CompanyNames:        companySnapshot,
						AttributeCareerPage: attributor,
						// A saved listing (found or refreshed) increments the reused
						// ListingsFound run counter and the collection.found metric.
						OnSaved: func(ctx context.Context) {
							counters.ListingsFound.Add(1)
							collectionMetrics.Found(ctx)
						},
					})
				},
				llmstream.WithWorkers[crawler.RawJobListing](llmMaxWorkers),
				llmstream.WithRecorder[crawler.RawJobListing](llmRecorder),
				llmstream.WithMaxBacklog[crawler.RawJobListing](llmMaxBacklog),
				llmstream.WithMinIdle[crawler.RawJobListing](llmConfig.Timeout+time.Minute),
			)
			if err := extractStage.Start(ctx); err != nil {
				return nil, fmt.Errorf("starting collection extract stage: %w", err)
			}

			// ATS Fetch lane (ADR-0022/0035): LLM-free board pulls that save presence,
			// run the absence-sweep on a complete fetch, and fold board reach into
			// Career-Page dormancy. Built after the last fallible setup so an early error
			// never leaks its worker goroutines.
			atsLimiter := atsingest.NewHostLimiter(defaultATSRateInterval)
			atsLane := atsingest.NewLane(ctx, atsingest.Config{
				MaxWorkers: defaultATSMaxWorkers,
				NewWorker: func() processor.Processor[atsingest.FetchTask] {
					return atsingest.NewProcessor(&atsingest.ProcessorConfig{
						ResolveFetcher:    atsRegistry.Fetcher,
						Repository:        corpusRepository,
						Liveness:          corpusRepository,
						Dormancy:          careerPageRepository,
						DormancyThreshold: crawler.DefaultPageDormancyThreshold,
						CycleStart:        cycleStart,
						CompanyNames:      companySnapshot,
						RateLimiter:       atsLimiter,
						OnSaved: func(ctx context.Context) {
							counters.ListingsFound.Add(1)
							collectionMetrics.Found(ctx)
						},
						OnBoardFetched:    collectionMetrics.BoardFetched,
						OnBoardIncomplete: collectionMetrics.BoardIncomplete,
						OnClosed:          collectionMetrics.Closed,
					})
				},
			})

			// Refetch + dormancy lane (ADR-0035): probes each crawled Career Page and
			// refetches its known-open postings for liveness, re-enqueueing changed pages
			// onto the extract stage.
			refetchLane := pool.NewPool(ctx, "collection_refetch_pool",
				func() processor.Processor[crawler.CollectionSeed] {
					return collection.NewRefetchProcessor(&collection.RefetchConfig{
						Downloader:        retryHTTPClient,
						Parser:            htmlParser,
						Liveness:          corpusRepository,
						Dormancy:          careerPageRepository,
						SourceHash:        func(mc string) string { return openrouter.SourceHash(mc, llmConfig.ExtractMaxChars) },
						EnqueueExtract:    extractStage.Enqueue,
						StaleThreshold:    crawler.DefaultCrawlStaleThreshold,
						DormancyThreshold: crawler.DefaultPageDormancyThreshold,
						OnRefreshed:       collectionMetrics.Refreshed,
						OnClosed:          collectionMetrics.Closed,
					})
				}, pool.WithMaxWorkers[crawler.CollectionSeed](maxWorkers))

			onJobListing := func(ctx context.Context, jl *crawler.RawJobListing) error {
				return extractStage.Enqueue(ctx, jl)
			}

			urlWorkerPool := pool.NewPool(ctx, "collection_url_pool",
				func() processor.Processor[crawler.URL] {
					return urlprocessor.NewProcessor(&urlprocessor.Config{
						Frontier:         boundedFrontier,
						Downloader:       retryHTTPClient,
						Parser:           htmlParser,
						ContentFilter:    contentFilter,
						URLFilter:        urlFilter,
						RobotsTxtChecker: robotsTxtChecker,
						// Pass-all relevance: collection has no keyword pruning (ADR-0038);
						// the Extract Gate still sheds hubs/indexes/reject-paths structurally.
						RelevanceFilter: contentFilter,
						GateConfig:      gateConfig,
						OnJobListing:    onJobListing,
						HasATSFetcher:   hasATSFetcher,
						// An ATS board embedded on a crawled page is fetched through the same
						// deduped lane, attributed to the page's Owner, with a Nil
						// CareerPageID (save-only: no sweep/dormancy for an embed board).
						OnATSEmbed: func(ctx context.Context, provider, tenant, owner string) error {
							return atsLane.Submit(ctx, atsingest.FetchTask{Provider: provider, TenantSlug: tenant, Owner: owner})
						},
						Recorder: llmRecorder,
					})
				}, pool.WithMaxWorkers[crawler.URL](maxWorkers))

			onNextURL := func(ctx context.Context, u *crawler.URL) error {
				counters.PagesCrawled.Add(1)
				return urlWorkerPool.Enqueue(ctx, u)
			}

			o := orchestrator.NewOrchestrator(orchestrator.Config{
				Frontier:   boundedFrontier,
				OnNextURL:  onNextURL,
				ShouldStop: shouldStop,
			})

			// Prime the ATS and refetch lanes last, after all fallible setup: live
			// priming goroutines feed the pools and Engine.Close reaps them.
			atsLane.PrimeAsync(ctx, atsTasks)
			refetchPriming := primeRefetchAsync(ctx, refetchLane, refetchPages)

			return &runner.Engine{
				Orchestrator: o,
				// crawlSeeds, not the ATS tenants: routed tenants are fetched by the lane
				// and must not enter the Frontier (ADR-0022).
				Seeds: crawlSeeds,
				// Close order: url pool first (stops walk pages + embed submits), then the
				// refetch lane (wait its priming, drain — it feeds the extract stage), then
				// the ATS lane (drains its board fetches), then the extract stage last (both
				// refetch and walk feed it). A clean finish drains each stream to empty; a
				// stop/shutdown leaves the PEL for resume.
				Close: func() {
					urlWorkerPool.Close()
					refetchPriming.Wait()
					refetchLane.Close()
					atsLane.Close()
					extractStage.Close()
					collectionMetrics.RecordCycle(ctx, time.Since(cycleStart))
					slog.Info("runner: llm stage summary", append([]any{"run_id", runID}, llmStats.Summary()...)...)
				},
			}, nil

		default:
			return nil, fmt.Errorf("unsupported crawl kind: %q", def.Kind)
		}
	}
}

// primeRefetchAsync submits the Cycle's refetch pages onto the refetch pool from a
// background goroutine so the run's start path is never blocked by pool
// backpressure. The returned WaitGroup lets Engine.Close wait for priming to finish
// enqueuing before it drains the pool; priming stops early if an Enqueue fails (ctx
// cancelled or pool closed). Mirrors atsingest.Lane.PrimeAsync for the generic pool.
func primeRefetchAsync(ctx context.Context, p *pool.Pool[crawler.CollectionSeed], pages []crawler.CollectionSeed) *sync.WaitGroup {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range pages {
			page := pages[i]
			if err := p.Enqueue(ctx, &page); err != nil {
				return
			}
		}
	}()
	return &wg
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
