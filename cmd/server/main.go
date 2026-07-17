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
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/postgres"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	joblistingfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/job_listing_filter"
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

	// Crawl tuning defaults, previously sourced from config.json. The per-kind
	// maxDepth constants seed a new crawl definition's field when the request
	// omits maxDepth (overridable per definition via the API); Discovery reaches
	// deeper because it is the perpetual catalog-building crawl. maxWorkers sizes
	// the per-run worker pools.
	defaultLogLevel          = "INFO"
	defaultKeywordMaxDepth   = 4
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

	jobListingRepository := postgres.NewJobListingRepository(pgPool)
	companyRepository := postgres.NewCompanyRepository(pgPool)
	careerPageRepository := postgres.NewCareerPageRepository(pgPool)
	defRepository := postgres.NewCrawlDefinitionRepository(pgPool)
	runRepository := postgres.NewCrawlRunRepository(pgPool)
	importJobRepository := postgres.NewImportJobRepository(pgPool)

	factory := newFactory(defaultMaxWorkers, llmMaxWorkers, llmConfig, redisClient,
		jobListingRepository, companyRepository, careerPageRepository)
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
		Listings:    jobListingRepository,
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
			return redisfrontier.New(redisClient, runID).AddURL(ctx, u)
		},
		Defaults: api.Defaults{
			KeywordMaxDepth:   defaultKeywordMaxDepth,
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
// (HTTP client, parser, robots checker, extractor, filters) are built once
// here and shared across runs; per-run state (frontier, pools) is built inside.
func newFactory(
	maxWorkers int,
	llmMaxWorkers int,
	llmConfig openrouter.Config,
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

	jobListingExtractor := openrouter.NewJobListingExtractor(llmConfig)
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
			urlfilter.BlockHostnames(uf.BlockedHostnames...),
		)

		if def.Kind == crawler.CrawlKindDiscovery {
			// Perpetual mode: the run stays alive after the frontier drains,
			// waiting for URLs discovered later. It ends only on a desired-state
			// stop. The Catalog (company + career_page) is filled by the
			// career-page pool.
			discoveryFrontier := redisfrontier.New(redisClient, runID,
				redisfrontier.WithMaxDepth(def.MaxDepth),
				redisfrontier.WithMode(frontier.Perpetual),
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

		// Keyword: bounded run that finishes when the frontier drains,
		// extracting Job Listings via the LLM. Unknown kinds are rejected at the
		// API, so this is the only other real kind.
		if def.Kind != crawler.CrawlKindKeyword {
			return nil, fmt.Errorf("unsupported crawl kind: %q", def.Kind)
		}

		// Seed a Keyword Crawl from the Catalog: the union of every Career Page
		// URL and each Pageless Company's Website (its seed of last resort). Each
		// query returns a CatalogSeed carrying the seed URL and its owning
		// Company's stored CompanyKey; catalog.ResolveSeeds then pairs the two
		// ADR-0021 provenance keys onto each Seed -- Owner from the stored key (the
		// attribution key), Scope from catalog.Identify(URL) (the fence key) --
		// dropping any seed whose URL fails to parse so every Keyword seed carries
		// a real Scope. On re-adoption (Reconcile) re-seeding is a no-op: the
		// per-run Redis visited set survived the restart and dedups (which also
		// collapses any overlap between the two seed sources).
		careerPageSeeds, err := careerPageRepository.ListSeeds(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving keyword crawl seeds from catalog: %w", err)
		}
		pagelessSeeds, err := companyRepository.ListPagelessSeeds(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolving keyword crawl pageless seeds from catalog: %w", err)
		}
		catalogSeeds := append(careerPageSeeds, pagelessSeeds...)
		seeds := catalog.ResolveSeeds(catalogSeeds)

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
			redisfrontier.WithMaxDepth(def.MaxDepth),
		)

		// Durable LLM stage: relevance-passing pages are XADDed onto a per-run
		// Redis Stream and drained by a consumer group into the job-listing
		// extractor, so the crawl never blocks on the model and a crash or restart
		// redelivers rather than loses the listing (ADR-0007 step 4, closes #32).
		extractStage := llmstream.NewStage(redisClient, runID, llmobs.KindExtract,
			func() processor.Processor[crawler.RawJobListing] {
				return joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
					JobListingRepository: jobListingRepository,
					JobListingExtractor:  jobListingExtractor,
					DefinitionID:         def.ID,
					Recorder:             llmRecorder,
				})
			},
			llmstream.WithWorkers[crawler.RawJobListing](llmMaxWorkers),
			llmstream.WithRecorder[crawler.RawJobListing](llmRecorder),
			llmstream.WithMaxBacklog[crawler.RawJobListing](llmMaxBacklog),
			// The first-reclaim window must exceed the whole Process: a single
			// Extract is bounded by the http client's LLM_TIMEOUT, and the extra
			// minute absorbs the follow-on listing upsert, so a slow but alive worker
			// still in flight is never reclaimed and double-called. Only a truly dead
			// worker sits idle longer. Retries of an already-failed entry are paced by
			// the shorter default reclaim interval.
			llmstream.WithMinIdle[crawler.RawJobListing](llmConfig.Timeout+time.Minute),
		)
		if err := extractStage.Start(ctx); err != nil {
			return nil, fmt.Errorf("starting extract stage: %w", err)
		}

		// Counter tap: a matching page found becomes a job listing. Counted once
		// here on enqueue (not per process) so a redelivery does not double-count.
		onJobListing := func(ctx context.Context, jl *crawler.RawJobListing) error {
			counters.ListingsFound.Add(1)
			return extractStage.Enqueue(ctx, jl)
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
					GateConfig:       gateConfig,
					OnJobListing:     onJobListing,
					Recorder:         llmRecorder,
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
			Seeds:        seeds,
			// Close order: url pool first (the producer feeding the extract stage),
			// then the stage. Closing the producer first means no new task is XADDed
			// mid-drain; a clean finish then drains the stream to empty, while a
			// stop/shutdown leaves the PEL for resume.
			Close: func() {
				urlWorkerPool.Close()
				extractStage.Close()
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
