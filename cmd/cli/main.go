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

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	jsonloader "github.com/nicholasbraun/job-crawler-poc/internal/config/json_loader"
	"github.com/nicholasbraun/job-crawler-poc/internal/database/sqlite"
	"github.com/nicholasbraun/job-crawler-poc/internal/filter"
	jobfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/job"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier/inmem"
	"github.com/nicholasbraun/job-crawler-poc/internal/http"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	myotel "github.com/nicholasbraun/job-crawler-poc/internal/otel"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	inmemprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/inmem"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

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

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
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
	jobRepository := sqlite.NewJobRepository(db)

	// create frontier
	frontier := inmem.NewFrontier(inmem.WithMaxDomains(config.MaxDomains))

	// create HTTP client + retry wrapper
	httpClient := http.NewClient()
	retryHTTPClient := http.NewRetryClient(httpClient)

	// create parser
	parser := parser.NewHTMLParser()

	// create processor
	processor := inmemprocessor.NewInMemProcessor(ctx, jobRepository)
	// create filter chains (start with pass-through filters)
	contentFilter := filter.Chain[*crawler.Content]() // empty chain = pass everything
	relevanceFilter := filter.Chain(
		filter.Every(jobfilter.TitleContains(
			filter.Contains("developer", "engineer", "entwickler"),
			filter.Contains("golang", "go", "backend", "software"),
		),
			jobfilter.MainContentContains(
				filter.Contains("apply", "bewerben"),
				filter.Contains("golang", "go"),
				filter.Contains("microservice"),
				filter.Contains("experience", "erfahrung", "years", "jahre"),
				filter.Contains("remote"),
				filter.Contains("europa", "europe", "germany", "deutschland", "berlin", "frankfurt", "hamburg", "nürnberg", "münchen", "munich", "nuremberg", "bremen", "stuttgart", "hannover"),
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
		MaxDepth:        config.MaxDepth,
		MaxWorkers:      config.MaxWorkers,
		Processor:       processor,
	}
	o := orchestrator.NewOrchestrator(cfg)

	// run
	err = o.Run(ctx, config.SeedURLs)
	if err != nil {
		log.Fatalf("crawl failed: %v", err)
	}
	slog.Info("crawl complete")
}
