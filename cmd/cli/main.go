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
	jobfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/job"
	urlfilter "github.com/nicholasbraun/job-crawler-poc/internal/filter/url"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier/inmem"
	"github.com/nicholasbraun/job-crawler-poc/internal/http"
	"github.com/nicholasbraun/job-crawler-poc/internal/orchestrator"
	"github.com/nicholasbraun/job-crawler-poc/internal/parser"
	inmemprocessor "github.com/nicholasbraun/job-crawler-poc/internal/processor/inmem"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// seed URLs from command line args
	seedURLsPtr := flag.String("seedURLs", "https://news.ycombinator.com/jobs", "comma separated list of seedURLs")
	dbPath := flag.String("db", "./data/database.db", "path to sqlite database")
	maxDepth := flag.Int("maxDepth", 2, "maximum depth a URL should be crawled")
	maxDomains := flag.Int("maxDomains", 10, "maximum domains that can be crawled")
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
	frontier := inmem.NewFrontier(inmem.WithMaxDomains(*maxDomains))

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
				filter.Contains("golang"),
				filter.Contains("experience", "erfahrung"),
				filter.Contains("remote", "europa", "europe", "germany", "deutschland", "berlin", "frankfurt", "hamburg"),
			),
		),
		filter.Reject[*crawler.Content](),
	)

	invalidURLCheck := urlfilter.BlockInvalidURLs()
	allowSubdomainsCheck := urlfilter.AllowSubdomains("jobs", "career", "careers", "hiring", "recruiting", "talent", "join", "apply", "boards", "team", "job-boards")
	allowPathSegmentsCheck := urlfilter.AllowPathSegments(
		"jobs",
		"careers", "career",
		"vacancies",
		"positions",
		"openings",
		"apply",
		"hiring",
		"opportunities",
		"recruitment",
		"stellenangebote",
		"stellen",
		"team",
	)
	blockSubdomainCheck := urlfilter.BlockSubdomains("blog", "news", "press", "media", "stories", "shop", "store", "marketplace", "help", "support", "docs", "wiki", "forum", "community", "research", "discuss", "gist", "templates",
		"api", "cdn", "static", "assets", "status", "staging", "dev", "test", "login", "auth", "sso", "accounts", "id", "ads", "mail", "email", "analytics", "tracking", "events")
	blockPathSegmentsCheck := urlfilter.BlockPathSegments("blog", "news", "press", "media", "podcast", "magazine", "articles", "stories",
		"learning", "posts", "products", "imprint", "impressum", "contact", "privacy", "legal", "terms", "disclaimer", "cookie", "gdpr", "tos", "agb", "datenschutz",
		"login", "signin", "signup", "register", "auth", "oauth", "sso", "account", "profile", "settings", "password", "logout",
		"shop", "store", "cart", "checkout", "pricing", "plans", "billing", "subscribe", "order",
		"help", "support", "faq", "docs", "documentation", "wiki", "forum", "community", "knowledgebase",
		"share", "feed", "rss", "atom", "sitemap", "social",
		"assets", "static", "cdn", "download", "downloads", "api", "webhook", "graphql",
		"landing", "promo", "campaign", "ads", "referral", "affiliate", "events", "webinar", "top-content", "maps",
		"demo", "trial", "onboarding", "tour", "features", "integrations", "changelog", "roadmap", "status", "model", "workflows", "cgi", "cdn-cgi", "authenticate", "games")

	blockHostnames := urlfilter.BlockHostnames("trustpilot.com", "www.apple.com", "x.com", "www.x.com", "youtube.com", "www.youtube.com", "youtu.be", "www.youtu.be", "github.com", "www.github.com", "tiktok.com", "www.tiktok.com", "twitter.com", "www.twitter.com", "roboflow.com", "www.roboflow.com", "instagram.com", "www.instagram.com", "google.com", "www.google.com", "bing.com", "www.bing.com", "open.spotify")

	urlFilter := filter.Chain[string](
		invalidURLCheck,
		allowSubdomainsCheck,
		allowPathSegmentsCheck,
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
		MaxDepth:        *maxDepth,
		Processor:       processor,
	}
	o := orchestrator.NewOrchestrator(cfg)

	// run
	err = o.Run(ctx, seedURLs)
	if err != nil {
		log.Fatalf("crawl failed: %v", err)
	}
	slog.Info("crawl complete")
}
