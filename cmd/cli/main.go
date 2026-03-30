// Package main is responsible for:
//
// - parsing args (seed urls)
// - running the crawl orchestrator
package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	jsonloader "github.com/nicholasbraun/job-crawler-poc/internal/config/json_loader"
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
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// err := godotenv.Load()
	// if err != nil {
	// 	log.Fatal("Error loading .env file")
	// }
	openrouterAPIKey := os.Getenv("OPENROUTER_API_KEY")

	// config
	dbPath := flag.String("db", "./data/database.db", "path to sqlite database")
	flag.Parse()

	jsonConfigLoader := jsonloader.NewJSONLoader("config.json")
	config, err := jsonConfigLoader.Load(ctx)
	if err != nil {
		log.Fatalf("error loading config: %v", err)
	}

	var logLevel slog.LevelVar
	if err := logLevel.UnmarshalText([]byte(config.LogLevel)); err != nil {
		log.Fatalf("error parsing logLevel from config: %v", err)
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: &logLevel,
	})

	slog.SetDefault(slog.New(handler))

	slog.Debug("loaded config from json", "config", config)

	otelShutdown, err := myotel.Setup(ctx)
	if err != nil {
		log.Fatalf("error setting up otel %v", err)
	}
	defer otelShutdown(context.Background())

	// downloadDuration, _ := meter.Float64Histogram("crawler.download.duration",
	// 	metric.WithUnit("s"),
	// )
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
	jobListingRepository := sqlite.NewJobListingRepository(db)

	// create frontier
	frontier := inmem.NewFrontier(
		inmem.WithMaxDomains(config.MaxDomains),
		inmem.WithMaxDepth(config.MaxDepth),
	)

	// create HTTP client + retry wrapper
	httpClient := downloader.NewClient()
	retryHTTPClient := downloader.NewRetryClient(httpClient)

	// create parser
	parser := parser.NewHTMLParser()

	// create filter chains (start with pass-through filters)
	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything
	relevanceFilter := filter.Chain(
		filter.Every(joblistingfilter.TitleContains(
			filter.Contains("developer", "engineer", "entwickler"),
			filter.Contains("golang", "go", "backend", "software"),
		),
			joblistingfilter.MainContentContains(
				filter.Contains("apply", "bewerben"),
				filter.Contains("golang", "go"),
				// filter.Contains("microservice"),
				filter.Contains("experience", "erfahrung", "years", "jahre"),
				// filter.Contains("remote"),
				// filter.Contains("europa", "europe", "germany", "deutschland", "berlin", "frankfurt", "hamburg", "nürnberg", "münchen", "munich", "nuremberg", "bremen", "stuttgart", "hannover"),
			),
		),
		filter.Reject[*crawler.Content](),
	)

	invalidURLCheck := urlfilter.BlockInvalidURLs()
	passSubdomainsCheck := urlfilter.PassSubdomains(config.PassSubdomains...)
	passPathSegmentsCheck := urlfilter.PassPathSegments(config.PassPathSegments...)
	blockSubdomainCheck := urlfilter.BlockSubdomains(config.BlockedSubdomains...)
	blockPathSegmentsCheck := urlfilter.BlockPathSegments(config.BlockedPathSegments...)
	blockHostnames := urlfilter.BlockHostnames(config.BlockedHostnames...)
	allowedTLDs := urlfilter.AllowedTLDs(config.AllowedTLDs...)

	urlFilter := filter.Chain[string](
		invalidURLCheck,
		allowedTLDs,
		passSubdomainsCheck,
		passPathSegmentsCheck,
		blockSubdomainCheck,
		blockPathSegmentsCheck,
		blockHostnames,
	)

	// create worker pools
	jobListingWorkerPool := pool.NewPool(
		ctx, "job_listing_worker_pool", func() processor.Processor[crawler.RawJobListing] {
			return joblistingprocessor.NewProcessor(&joblistingprocessor.Config{
				JobListingRepository: jobListingRepository,
				JobListingExtractor:  openrouter.NewJobListingExtractor(openrouterAPIKey),
			})
		})

	urlWorkerPool := pool.NewPool(
		ctx, "url_worker_pool", func() processor.Processor[crawler.URL] {
			return urlprocessor.NewProcessor(&urlprocessor.Config{
				Frontier:        frontier,
				Downloader:      retryHTTPClient,
				Parser:          parser,
				URLRepository:   urlRepository,
				ContentFilter:   contentFilter,
				URLFilter:       urlFilter,
				RelevanceFilter: relevanceFilter,
				OnJobListing:    jobListingWorkerPool.Enqueue,
			})
		}, pool.WithMaxWorkers[crawler.URL](config.MaxWorkers))

	defer jobListingWorkerPool.Close()
	defer urlWorkerPool.Close()

	// create orchestrator
	cfg := orchestrator.Config{
		Frontier:      frontier,
		URLRepository: urlRepository,
		OnNextURL:     urlWorkerPool.Enqueue,
	}
	o := orchestrator.NewOrchestrator(cfg)

	// run
	err = o.Run(ctx, config.SeedURLs)
	if err != nil {
		log.Fatalf("crawl ended: %v", err)
	}
	slog.Info("crawl complete")
}
