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
	"github.com/nicholasbraun/job-crawler-poc/internal/env"
	"github.com/nicholasbraun/job-crawler-poc/internal/extractcapture"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	"github.com/nicholasbraun/job-crawler-poc/internal/freeextraction"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/nicholasbraun/job-crawler-poc/internal/importer"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
	"github.com/nicholasbraun/job-crawler-poc/internal/llmstream"
	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	myotel "github.com/nicholasbraun/job-crawler-poc/internal/otel"
	"github.com/nicholasbraun/job-crawler-poc/internal/pagegate"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	"github.com/nicholasbraun/job-crawler-poc/internal/pool"
	"github.com/nicholasbraun/job-crawler-poc/internal/processor"
	careerpageprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/career_page_processor"
	discoveryprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/discovery_processor"
	joblistingprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/job_listing_processor"
	shadowextractionprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/shadow_extraction_processor"
	urlprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/url_processor"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt/temoto"
	"github.com/nicholasbraun/job-crawler-poc/internal/runner"
	"github.com/nicholasbraun/job-crawler-poc/web"
	"github.com/redis/go-redis/v9"
)

const (
	userAgent        = "JobCrawlerBot/0.1 (+https://github.com/nicholasbraun/job-crawler-poc)"
	serverAddr       = ":8080"
	defaultRedisAddr = "localhost:6379"
	shutdownTimeout  = 30 * time.Second

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
	// when the request omits maxDepth (overridable per definition via the API).
	// Lowered 10 -> 7: a live frontier audit found the deep tail (depth >=4 = ~34%
	// of enqueued URLs) is reached almost entirely via cross-linking (recommendation
	// graphs, nav menus) into non-job content, not by postings living deep -- 93% of
	// crawl-sourced listings sit within +3 path levels of their seed. 7 keeps margin
	// over that while capping the sprawl. Instrumented via job_listing.discovered_depth
	// (migration 0024) so the cap can be re-tuned from real save-depths.
	// defaultMaxWorkers is the default size of the per-run worker pools,
	// overridable via CRAWL_MAX_WORKERS.
	defaultLogLevel          = "INFO"
	defaultDiscoveryMaxDepth = 7
	defaultMaxWorkers        = 50

	// defaultLLMMaxWorkers sizes the durable LLM stage's consumer group, and
	// defaultCollectionInterval is the Collection Cycle cadence (ADR-0036); both
	// are overridable per deployment (LLM_MAX_WORKERS / COLLECTION_INTERVAL).
	defaultLLMMaxWorkers      = 2
	defaultCollectionInterval = 24 * time.Hour

	// ATS Fetch lane tuning (ADR-0022), shared by every Collection Cycle:
	// defaultATSMaxWorkers sizes the ingest pool (how many tenants are fetched in
	// parallel across providers), and defaultATSRateInterval is the minimum spacing
	// between board-API calls to one provider (the per-provider HostLimiter).
	defaultATSMaxWorkers   = 8
	defaultATSRateInterval = 250 * time.Millisecond

	// defaultRefetchRateInterval is the minimum spacing between refetch-lane GETs
	// to one Politeness Domain (registrable domain), enforced by the shared
	// HostLimiter the politeDownloader waits on (ADR-0040). 1s — the Frontier's
	// blessed rate for arbitrary web servers, gentler than the ATS lane's 250ms.
	defaultRefetchRateInterval = time.Second

	// llmMaxBacklog is the high-water cap on a per-run LLM stream's outstanding
	// entries. Past it, the crawl's Enqueue blocks until the classify/extract
	// consumer group catches up, so a crawl that outruns the model applies
	// backpressure instead of growing Redis without bound (each entry carries the
	// full page content). It is a safety valve sized well above steady-state
	// backlog, so normal operation never reaches it.
	llmMaxBacklog = 5000

	// Shadow Extraction lane sizing (ADR-0044). It is a measurement, so it is sized
	// to stay out of the way of the work it measures: ONE consumer, so shadow calls
	// never compete with real extraction for model concurrency; its OWN small backlog
	// cap, so measurement can never consume the extract stage's capacity; and a short
	// bound on the walk's enqueue, so a full shadow backlog DROPS the sample instead
	// of pacing the crawl. A lost sample is acceptable; a slowed Collection Cycle is
	// not.
	shadowMaxWorkers     = 1
	shadowMaxBacklog     = 500
	shadowEnqueueTimeout = 2 * time.Second
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

	// Every knob the server reads is resolved in the block below, through one
	// env.Loader: a container started with several malformed values reports all of
	// them on the first boot rather than one per restart (ADR-0045). Nothing may
	// act on a knob until ld.Err() has been checked at the end of the block.
	var ld env.Loader

	// The classifier/extractor settings (LLM_API_KEY, LLM_BASE_URL, LLM_MODEL,
	// LLM_TIMEOUT, and the two prompt-window caps) are read by the openrouter
	// package itself, so the gate benchmarks in cmd/llmbench drive the model
	// through exactly the same configuration this crawl does -- see
	// openrouter.ConfigFromEnv for what each one does.
	llmConfig := openrouter.ConfigFromEnv(&ld)

	// LLM_MAX_WORKERS sizes the durable LLM stage's consumer group: how many
	// goroutines drain the per-run Redis Stream in parallel, not an in-process
	// pool. It defaults low to match a local model, which generates serially --
	// more workers there only build a deep server-side queue that blows
	// LLM_TIMEOUT. Raise it for a fast, highly-parallel cloud API.
	llmMaxWorkers := ld.PositiveInt("LLM_MAX_WORKERS", defaultLLMMaxWorkers)

	// DESCRIPTION_MAX_CHARS caps the stored Posting Body (ADR-0041). Its own knob, NOT
	// LLM_EXTRACT_MAX_CHARS: the prompt window is a latency dial for local models and
	// must never decide what the Corpus stores. 16000 sits above the p99 of real
	// postings (~12k), so it only fires on a pathological parse.
	descriptionMaxChars := ld.PositiveInt("DESCRIPTION_MAX_CHARS", crawler.DefaultDescriptionMaxChars)

	// EXTRACT_FROM_JSONLD is the Free Extraction kill switch (ADR-0042), default true:
	// a page publishing exactly one structured-data JobPosting, no openings index and a
	// title is extracted from that data with no model call. Set false to unwrap the
	// decorator and restore the model-only extract path exactly. It exists because this
	// is the ONE path that can save a Job Listing with no model in the loop — if it
	// goes wrong it goes wrong silently and at scale, and a dial beats a deploy.
	extractFromJSONLD := ld.Bool("EXTRACT_FROM_JSONLD", true)

	// SHADOW_EXTRACT_RATE is the fraction of Extract-Gate-rejected pages that are
	// extracted anyway, purely to measure the gate (ADR-0044). Default 0.01: post-gate
	// the walk skips roughly 300k pages a day, so one per cent is ~3k calls — about
	// eight cents — for the only instrument that can see a false-drop at all. A dropped
	// page is permanent (no listing means the Collection Cycle's visited-seeding never
	// seeds it, so the walk re-reaches and re-drops it every Cycle), and nothing else in
	// production moves when it happens. 0 switches the mechanism off entirely: no shadow
	// stream is created and the walk's hook stays nil.
	shadowExtractRate := ld.Fraction("SHADOW_EXTRACT_RATE", 0.01)

	// EXTRACT_REQUIRE_POSITIVE_EVIDENCE is the Extract Gate's Positive Evidence rung
	// (ADR-0044), default true: a page that clears every reject rung reaches the
	// extractor only on evidence that it IS one posting, rather than merely because
	// nothing rejected it. Set false to restore the pre-ADR-0044 blanket accept
	// exactly. It is a dial rather than a deploy because the failure it can cause is
	// the permanent one: a false-drop is never seeded as visited, so the walk
	// re-reaches and re-drops it every Collection Cycle, and nothing in production
	// moves when it happens. SHADOW_EXTRACT_RATE above is the instrument that would
	// see it; this is the switch to pull if it does.
	requirePositiveEvidence := ld.Bool("EXTRACT_REQUIRE_POSITIVE_EVIDENCE", true)

	// EXTRACT_LEARNED_VETO is the Extract Gate's Learned Veto (ADR-0049), default
	// FALSE -- today's gate exactly. Set it true and a page carrying Positive Evidence
	// reaches the extractor only if its Posting Score clears the threshold the training
	// run compiled in beside the weights. Unset restores the UNCONDITIONAL Positive
	// Evidence accept, with no deploy, and off the score is never computed at all, so a
	// crawl not using the rung pays nothing for it.
	//
	// It is a dial rather than a deploy for EXTRACT_REQUIRE_POSITIVE_EVIDENCE's reason
	// above -- a false-drop is the permanent failure -- and one of its own: this is the
	// only rung in the gate that is fitted rather than argued, so it cannot explain a
	// drop in words. SHADOW_EXTRACT_RATE above is the instrument that would see it.
	//
	// Turning it ON is a separate act with a runbook (ADR-0049): a capture window,
	// offline scoring, a blind confirmation of the pages it would drop, those labels
	// committed, and only then the flip.
	learnedVeto := ld.Bool("EXTRACT_LEARNED_VETO", false)

	// PARSE_STRUCTURAL_RENDERING makes the parser keep a page's structure instead of
	// flattening it (ADR-0046), default false: the Flattened Text it has always
	// produced. Set true and the parser renders headings, list items, table rows,
	// link targets and form controls -- the difference between one application form
	// offering three roles and an index of three roles. Every NON-MODEL consumer still
	// reads crawler.FlattenedText -- the Posting Body, the extraction-cache key, the
	// Gate's phrase marks, the duplication probe -- so nothing persisted moves, and the
	// round-trip is asserted byte for byte over the 90 committed fixtures. What the
	// switch really changes for a running crawl is the two LLM prompts: the classifier
	// and the extractor read the rendering with link targets omitted (#280). Setting it
	// back to false restores today's parser exactly, with no second DOM walk on any page
	// the Discovery Crawl fetches.
	//
	// Which way to set it DEPENDS ON THE MODEL (#289). Scored online over re-fetched real
	// postings, the rendering left a 9B's recall unchanged at 92% -- it has no headroom --
	// but on the pages that actually REACH the model (Free Extraction takes the rest with
	// no call at all) it nearly doubled a 3B instruct model's recall, 28.0%->50.6%. What
	// falls through carries no usable JSON-LD, so the DOM is the only signal left, and
	// discarding it costs most exactly where the model is weakest. Whoever changes
	// LLM_MODEL owns re-deciding this, with cmd/extractbench -fixtures ... -structural.
	//
	// One thing the switch does NOT gate: crawler.FlattenedText now sits in front of the
	// persisted paths unconditionally, so page text that itself contains marker syntax
	// ("[select all]", a literal "](") is rewritten whether this is on or off. Measured
	// zero across the 90 committed fixtures and all 457 Extract Gold Set rows, and
	// asserted by TestFlattenedTextIsIdentityOnTodaysOutput -- but "off is byte-identical
	// to before the renderer existed" is true of the parser, not of the derivation.
	structuralRendering := ld.Bool("PARSE_STRUCTURAL_RENDERING", parser.DefaultStructuralRendering)

	// CRAWL_MAX_WORKERS sizes the per-run discovery worker pool — how many pages
	// are downloaded and processed in parallel per run. Crawl workers are
	// I/O-bound (blocked on network downloads), so this
	// can be raised well past the default to lift throughput once the frontier is
	// no longer the bottleneck; the Postgres pool and outbound network are the next
	// caps to watch.
	crawlMaxWorkers := ld.PositiveInt("CRAWL_MAX_WORKERS", defaultMaxWorkers)

	// CRAWL_VISITED_CAP bounds each run's visited ZSET (ADR-0027 / #75). Read once
	// and applied to every Frontier built for a run so the FIFO cap is consistent
	// regardless of which Frontier performs the AddURL.
	visitedCap := ld.PositiveInt("CRAWL_VISITED_CAP", redisfrontier.DefaultVisitedCap)

	// ROBOTS_CACHE_SIZE / ROBOTS_CACHE_TTL bound the shared robots.txt Rules cache
	// (ADR-0032): how many hosts' parsed rules are held and for how long before a
	// re-fetch, so the cache cannot grow without limit across a discovery crawl
	// that touches tens of thousands of hosts.
	robotsCacheSize := ld.PositiveInt("ROBOTS_CACHE_SIZE", robotstxt.DefaultCacheSize)
	robotsCacheTTL := ld.PositiveDuration("ROBOTS_CACHE_TTL", robotstxt.DefaultCacheTTL)

	// COLLECTION_INTERVAL is the Collection Cycle cadence (ADR-0036): the minimum
	// time between Cycle starts. Default daily. COLLECTION_ENABLED (default true)
	// is the disable flag -- set it false to stop the scheduler from starting
	// Cycles (manual starts via the API still work).
	collectionInterval := ld.PositiveDuration("COLLECTION_INTERVAL", defaultCollectionInterval)
	collectionEnabled := ld.Bool("COLLECTION_ENABLED", true)

	var logLevel slog.LevelVar
	ld.Text("LOG_LEVEL", &logLevel, defaultLogLevel)

	// Redis holds transient per-run crawl state (frontier queues, visited set,
	// in-flight leases); Postgres holds the durable Catalog and Corpus. Both
	// addresses are read here with the rest of the configuration so a typo in one
	// is reported alongside every other bad knob, before either is dialled.
	redisAddr := ld.String("REDIS_ADDR", defaultRedisAddr)
	databaseURL := ld.String("DATABASE_URL", postgres.DefaultURL)

	// The one gate for the whole block: past this line every knob above is valid.
	// It reports every malformed variable at once, so fixing a container's
	// environment takes one round trip rather than one per mistake.
	if err := ld.Err(); err != nil {
		log.Fatalf("error reading configuration:\n%v", err)
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &logLevel})))

	otelShutdown, err := myotel.Setup(ctx)
	if err != nil {
		log.Fatalf("error setting up otel: %v", err)
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

	factory := newFactory(crawlMaxWorkers, visitedCap, robotsCacheTTL, robotsCacheSize, llmMaxWorkers, llmConfig,
		descriptionMaxChars, extractFromJSONLD, shadowExtractRate, requirePositiveEvidence, learnedVeto, structuralRendering,
		redisClient, companyRepository, careerPageRepository, corpusRepository)
	crawlRunner := runner.New(runRepository, defRepository, factory,
		// One cleaner sweeps all of a run's transient Redis state on a terminal
		// status or factory error: the frontier keys and the LLM stage's streams
		// (every kind — classify, extract, shadow — plus their dead-letter streams,
		// via the llmstream:{runID}:* glob). A paused run (graceful shutdown) is not
		// terminal, so its streams survive for a resumed run to redeliver.
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
	descriptionMaxChars int,
	extractFromJSONLD bool,
	shadowExtractRate float64,
	requirePositiveEvidence bool,
	learnedVeto bool,
	structuralRendering bool,
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
	htmlParser := parser.NewHTMLParser(parser.WithStructuralRendering(structuralRendering))

	robotsTxtParser := temoto.NewRobotsTxtParser(userAgent)
	robotsTxtDownloader := robotstxt.NewRobotsTxtDownloader(userAgent, sharedTransport)
	robotsTxtChecker := robotstxt.NewChecker(robotsTxtParser, robotsTxtDownloader,
		robotstxt.WithCacheTTL(robotsCacheTTL), robotstxt.WithCacheSize(robotsCacheSize))

	careerPageConfirmer := openrouter.NewCareerPageClassifier(llmConfig)

	// The collection extract stage's extractor. The Free Extraction (ADR-0042) wraps
	// it as a decorator over the same port: a page whose structured data publishes
	// exactly one JobPosting, no openings index and a non-empty title is extracted
	// from that data and never reaches the model; every other page is delegated
	// unchanged. Wrapped HERE and only here — Discovery's classifier path
	// (careerPageConfirmer above) is untouched, which is deliberate: a structured-data
	// bypass is exactly what cost that pipeline precision in #45/#46.
	var jobListingExtractor joblistingprocessor.JobListingExtractor = openrouter.NewJobListingExtractor(llmConfig)
	if extractFromJSONLD {
		jobListingExtractor = freeextraction.NewExtractor(jobListingExtractor)
	}
	slog.Info("free extraction (ADR-0042)", "enabled", extractFromJSONLD)
	slog.Info("shadow extraction (ADR-0044)", "rate", shadowExtractRate)
	// The renderer is stamped on every extract-capture record and decides what the
	// two prompts read, so which one is running has to be legible from the log
	// rather than inferred from the environment (ADR-0046).
	slog.Info("structural rendering (ADR-0046)", "enabled", structuralRendering, "renderer", htmlParser.RendererID())

	// The extraction-cache key (ADR-0035): ONE closure over the extractor's prompt
	// window, handed to both the save processor that stamps it and the refetch lane
	// that recomputes it. Sharing the closure is what keeps the two byte-identical —
	// a drift would re-extract the whole Corpus every Cycle.
	sourceHash := func(mainContent string) string { return crawler.SourceHash(mainContent, llmConfig.ExtractMaxChars) }

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
	// Create the Shadow Extraction series at zero before any sample can land, so the
	// FIRST false-drop on a rung is visible to increase() rather than being absorbed
	// as the series' baseline. llmobs owns the verdicts; pagegate owns the rungs.
	// context.Background: this only creates zero-valued series on the meter, so there
	// is nothing for a cancellation to abort and no run to tie it to.
	primeRungs := make([]string, 0, len(pagegate.RejectRungs()))
	for _, rung := range pagegate.RejectRungs() {
		primeRungs = append(primeRungs, string(rung))
	}
	llmMetrics.PrimeShadow(context.Background(), primeRungs)

	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything

	// Pre-LLM gate signals (ADR-0007 step 2), shared across runs: cheap URL-path
	// checks that resolve a page's classifier/extractor verdict without a model call.
	//
	// ONE value serves both gates and every lane. Of the three that take it, the two
	// consulting the EXTRACT Gate -- the walk's url processor and the refetch lane's
	// changed-content re-extract -- are the ones the override below moves; the
	// discovery processor reads the same struct for the DISCOVERY Gate, which never
	// consults RequirePositiveEvidence or LearnedVeto. So each kill switch covers the
	// whole extract path or none of it, and neither can touch Discovery.
	gateConfig := crawler.DefaultLLMGateConfig()
	gateConfig.RequirePositiveEvidence = requirePositiveEvidence
	slog.Info("extract gate positive evidence (ADR-0044)", "enabled", requirePositiveEvidence)
	gateConfig.LearnedVeto = learnedVeto
	// The threshold is compiled in beside the weights, so this log line is the only
	// place a running crawl says which operating point it is enforcing (ADR-0049).
	slog.Info("extract gate learned veto (ADR-0049)", "enabled", learnedVeto, "threshold", pagegate.VetoThreshold)

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
			urlfilter.BlockQueryParams(uf.BlockedQueryParams...),
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
						// The Posting Body cap and the extraction-cache key the save
						// processor stamps (ADR-0041/0035); the key closure is the one the
						// refetch lane below also uses, so the two never drift.
						DescriptionMaxChars: descriptionMaxChars,
						SourceHash:          sourceHash,
						// A saved listing (found or refreshed) increments the reused
						// ListingsFound run counter and the collection.found metric.
						OnSaved: func(ctx context.Context) {
							counters.ListingsFound.Add(1)
							collectionMetrics.Found(ctx)
						},
						// Extract Gold Set harvest tap (#116, ADR-0043): off unless
						// EXTRACT_CAPTURE_PATH is set. Emits
						// {url, verdict, ts, renderer, content} per extraction; `llmbench
						// goldset-sample` commits a stratified, weighted sample of it as the
						// gold set. The renderer is taken from the parser that actually
						// produces the captured content rather than re-derived from the kill
						// switch here, so a harvest run with PARSE_STRUCTURAL_RENDERING on is
						// distinguishable row by row from one with it off (ADR-0046, #281).
						CaptureDecision: extractcapture.FromEnv(htmlParser.RendererID()),
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

			// Shadow Extraction (ADR-0044): a sampled fraction of the pages the Extract
			// Gate rejects is extracted anyway, purely to measure how often the gate drops
			// a real posting. It travels on its OWN durable stream, drained by its OWN
			// processor built WITHOUT a Corpus repository — a measurement must be
			// structurally incapable of writing to the data it measures, and the separate
			// stream also means measurement can never eat the extract stage's backlog
			// capacity. A zero rate builds nothing and leaves the walk's hook nil.
			closeShadow := func() {}
			var onShadowExtract func(ctx context.Context, sample *crawler.ShadowSample) error
			if shadowExtractRate > 0 {
				// Its own cancellable context so Engine.Close can STOP this lane rather than
				// DRAIN it: llmstream drains on a clean finish, and draining a backlog of
				// measurement calls would extend every Cycle for samples nobody is waiting
				// on. Cancelling first puts Close on its stop/shutdown path, leaving any
				// remaining entries in the PEL to be swept with the rest of the run's streams
				// (llmstream.DeleteRun).
				shadowCtx, cancelShadow := context.WithCancel(ctx)
				shadowStage := llmstream.NewStage(redisClient, runID, llmobs.KindShadow,
					func() processor.Processor[crawler.ShadowSample] {
						return shadowextractionprocessor.NewProcessor(&shadowextractionprocessor.Config{
							// The SAME extractor the real lane uses, so a shadow verdict is the
							// verdict this page would have received had the gate kept it.
							Extractor: jobListingExtractor,
							Recorder:  llmRecorder,
						})
					},
					llmstream.WithWorkers[crawler.ShadowSample](shadowMaxWorkers),
					llmstream.WithRecorder[crawler.ShadowSample](llmRecorder),
					llmstream.WithMaxBacklog[crawler.ShadowSample](shadowMaxBacklog),
					llmstream.WithMinIdle[crawler.ShadowSample](llmConfig.Timeout+time.Minute),
				)
				if err := shadowStage.Start(shadowCtx); err != nil {
					// A measurement lane must never fail a Collection Cycle, and failing here
					// would also strand the extract stage's goroutines. Log and run without it.
					slog.Error("collection: starting the shadow extraction stage, continuing without it",
						"err", err, "run_id", runID)
					cancelShadow()
				} else {
					closeShadow = func() { cancelShadow(); shadowStage.Close() }
					onShadowExtract = func(ctx context.Context, sample *crawler.ShadowSample) error {
						// Bounded: a full shadow backlog must drop the sample, never park a walk
						// worker on capacity. Enqueue honours the context (awaitCapacity). A
						// returned error is the walk's cue to count the sample as shed.
						ctx, cancel := context.WithTimeout(ctx, shadowEnqueueTimeout)
						defer cancel()
						return shadowStage.Enqueue(ctx, sample)
					}
				}
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

			// Refetch-lane politeness (ADR-0040): ONE shared limiter + decorator across all
			// refetch workers, so a page's probe + its N listings and two workers hitting
			// the same platform share one per-registrable-domain spacing bucket. Reuses the
			// already-cache-warm robotsTxtChecker (no new robots cache). onRobotsBlocked taps
			// collectionMetrics.RobotsBlocked — the single choke point covering both the page
			// probe and per-listing robots blocks (ADR-0040 / #239).
			refetchLimiter := atsingest.NewHostLimiter(defaultRefetchRateInterval)
			politeRefetchDownloader := collection.NewPoliteDownloader(retryHTTPClient, robotsTxtChecker, refetchLimiter, collectionMetrics.RobotsBlocked)

			// Refetch + dormancy lane (ADR-0035): probes each crawled Career Page and
			// refetches its known-open postings for liveness, re-enqueueing changed pages
			// onto the extract stage.
			refetchLane := pool.NewPool(ctx, "collection_refetch_pool",
				func() processor.Processor[crawler.CollectionSeed] {
					return collection.NewRefetchProcessor(&collection.RefetchConfig{
						Downloader: politeRefetchDownloader,
						Parser:     htmlParser,
						Liveness:   corpusRepository,
						Dormancy:   careerPageRepository,
						// Same career-page classifier Discovery uses (ADR-0035): a reachable
						// page that no longer lists openings accrues dormancy like a 404.
						Classifier: careerPageConfirmer,
						// Structural pre-gate for the re-classification, so a page discovery
						// certain-accepted on structure is not dormant-closed by an LLM blip —
						// and the Extract Gate the changed-content re-extract now consults
						// (ADR-0044). It is the SAME gateConfig the walk's url processor gets
						// below, so the two lanes cannot diverge.
						GateConfig: gateConfig,
						SourceHash: sourceHash,
						// Legacy-summary heal (ADR-0041): rewrite a model-authored body from
						// the page the refetch already fetched, under the same
						// DESCRIPTION_MAX_CHARS cap the save processor above uses, so a healed
						// body and a freshly-extracted one are bounded identically.
						Descriptions:        corpusRepository,
						DescriptionMaxChars: descriptionMaxChars,
						EnqueueExtract:      extractStage.Enqueue,
						StaleThreshold:      crawler.DefaultCrawlStaleThreshold,
						DormancyThreshold:   crawler.DefaultPageDormancyThreshold,
						OnRefreshed:         collectionMetrics.Refreshed,
						OnClosed:            collectionMetrics.Closed,
						// Re-gate counter (ADR-0044): how many Open listings a full content
						// re-gate WOULD Close. It Closes none — healing the Corpus is a
						// separate decision, gated on this number (#208).
						OnRegateRejected: collectionMetrics.RegateRejected,
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
						// Shadow Extraction (ADR-0044): the sampled fraction of the pages the
						// gate rejects, routed to the measurement lane above. Nil hook / zero
						// rate = off.
						OnShadowExtract: onShadowExtract,
						ShadowRate:      shadowExtractRate,
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
				// Shadow Extraction lane (its only producer is that pool), then the refetch
				// lane (wait its priming, drain — it feeds the extract stage), then the ATS
				// lane (drains its board fetches), then the extract stage last (both refetch
				// and walk feed it). A clean finish drains each stream to empty; a
				// stop/shutdown leaves the PEL for resume.
				Close: func() {
					urlWorkerPool.Close()
					// Shadow Extraction is STOPPED rather than drained, so a backlog of
					// measurement calls cannot lengthen the Cycle.
					closeShadow()
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
