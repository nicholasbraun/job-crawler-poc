package crawler

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound is returned by repositories when a requested entity does not
// exist. Callers use errors.Is to map it to a 404.
var ErrNotFound = errors.New("crawler: not found")

// CrawlKind distinguishes the crawl strategies. discovery walks a site
// following the URL filters; keyword additionally gates pages by keywords.
// Only discovery is exercised in Step 1; keyword is reserved for later steps.
type CrawlKind string

const (
	CrawlKindDiscovery CrawlKind = "discovery"
	CrawlKindKeyword   CrawlKind = "keyword"
)

// URLFilterConfig captures the URL filtering rules for a crawl. A definition
// carries its own filters; a create request that omits them is filled with the
// built-in DefaultURLFilterConfig by the API.
type URLFilterConfig struct {
	AllowedTLDs         []string `json:"allowedTLDs"`
	BlockedSubdomains   []string `json:"blockedSubdomains"`
	BlockedPathSegments []string `json:"blockedPathSegments"`
	BlockedHostnames    []string `json:"blockedHostnames"`
	PassSubdomains      []string `json:"passSubdomains"`
	PassPathSegments    []string `json:"passPathSegments"`
}

// LLMGateConfig holds pre-LLM gate signals (ADR-0007 step 2): cheap URL-path
// checks that resolve a page's classifier/extractor verdict without a model
// call. A CareerPath segment marks a page a Career Page hub confidently enough
// to catalog it without the LLM classifier (and, on the keyword path, marks it
// an index to crawl rather than extract); a RejectPath segment marks it
// structurally not a job page, dropping it before any LLM call. A page with
// neither signal is ambiguous and still goes to the model.
//
// Unlike URLFilterConfig, this is not (yet) a per-definition, persisted field: the
// factory applies the process-wide DefaultLLMGateConfig to every run, so the type
// carries no json tags. Add them when it becomes a persisted definition field.
type LLMGateConfig struct {
	CareerPathSignals []string
	RejectPathSignals []string
}

// DefaultLLMGateConfig returns the built-in pre-LLM gate signals. CareerPathSignals
// is intentionally a high-precision set: a bare page on one of these paths is
// cataloged as a Career Page (or, on the keyword path, treated as an index) with no
// LLM confirmation, so only path tokens that are almost always a jobs hub belong
// here. Weaker, ambiguous tokens (e.g. "join", which is as often a newsletter or
// community signup) are deliberately left out; the pagegate content heuristic still
// accepts them, but as uncertain — the LLM confirms before cataloging.
func DefaultLLMGateConfig() LLMGateConfig {
	return LLMGateConfig{
		CareerPathSignals: []string{
			"careers", "career", "jobs", "karriere",
			"stellenangebote", "vacancies",
		},
		RejectPathSignals: []string{
			"blog", "news", "press", "media", "legal", "privacy",
			"terms", "imprint", "impressum", "cookie", "gdpr", "pricing",
		},
	}
}

// CrawlDefinition is the persisted specification of a crawl: what to crawl and
// how. A definition is immutable once created; each execution of it is a
// CrawlRun.
type CrawlDefinition struct {
	ID       uuid.UUID
	Name     string
	Kind     CrawlKind
	SeedURLs []string
	// Keywords gate pages for keyword crawls. Unused for discovery crawls.
	Keywords   []string
	MaxDepth   int
	MaxDomains int
	URLFilter  URLFilterConfig
	CreatedAt  time.Time
}

// CrawlDefinitionRepository persists and retrieves crawl definitions.
type CrawlDefinitionRepository interface {
	Create(ctx context.Context, def *CrawlDefinition) error
	Get(ctx context.Context, id uuid.UUID) (*CrawlDefinition, error)
	List(ctx context.Context) ([]*CrawlDefinition, error)
	// Delete removes a definition by ID. It is idempotent: deleting a
	// nonexistent definition is not an error.
	Delete(ctx context.Context, id uuid.UUID) error
}

// DefaultURLFilterConfig returns the built-in URL filtering rules applied to a
// crawl definition when a create request omits its own. These tuned lists steer
// a crawl toward company career pages: they restrict TLDs, short-circuit-pass
// hiring-related subdomains and path segments, and block the subdomains, path
// segments, and hostnames that reliably lead away from job listings (blogs,
// docs, shops, auth, social media, and so on). Previously sourced from
// config.json; now the process-wide default lives here in the domain.
func DefaultURLFilterConfig() URLFilterConfig {
	return URLFilterConfig{
		AllowedTLDs: []string{
			"de", "com", "org", "ai", "io", "jobs", "eu",
			"tech", "sh", "app", "dev", "cafe", "health", "xyz",
		},
		PassSubdomains: []string{
			"jobs", "career", "careers", "karriere", "hiring", "recruiting",
			"talent", "join", "apply", "boards", "team", "job-boards",
		},
		PassPathSegments: []string{
			"jobs", "job", "careers", "career", "karriere", "vacancies",
			"positions", "openings", "apply", "hiring", "opportunities",
			"recruitment", "stellenangebote", "stellen", "team",
		},
		BlockedSubdomains: []string{
			"apps", "wiki", "foundation", "docs", "donate", "blog", "news",
			"press", "media", "stories", "shop", "store", "marketplace", "help",
			"support", "forum", "community", "research", "discuss", "gist",
			"templates", "api", "books", "cdn", "static", "assets", "status",
			"staging", "dev", "test", "login", "auth", "sso", "accounts", "id",
			"ads", "mail", "email", "analytics", "tracking", "events",
		},
		BlockedPathSegments: []string{
			"add_to", "blog", "wiki", "signin", "news", "press", "users",
			"media", "podcast", "magazine", "articles", "stories", "learning",
			"posts", "products", "imprint", "impressum", "contact", "privacy",
			"legal", "terms", "disclaimer", "cookie", "gdpr", "tos", "agb",
			"datenschutz", "login", "signup", "register", "auth", "oauth", "sso",
			"account", "profile", "settings", "password", "logout", "shop",
			"store", "cart", "checkout", "pricing", "plans", "billing",
			"subscribe", "order", "help", "support", "faq", "docs",
			"documentation", "forum", "community", "knowledgebase", "comments",
			"share", "feed", "rss", "atom", "sitemap", "social", "assets",
			"static", "cdn", "download", "downloads", "api", "webhook",
			"graphql", "landing", "promo", "campaign", "ads", "referral",
			"affiliate", "events", "webinar", "top-content", "maps", "demo",
			"trial", "onboarding", "tour", "features", "integrations",
			"changelog", "roadmap", "status", "model", "workflows", "cgi",
			"cdn-cgi", "authenticate", "games",
		},
		BlockedHostnames: []string{
			"www.addtoany.com", "trustpilot.com", "www.apple.com", "x.com",
			"www.x.com", "youtube.com", "www.youtube.com", "youtu.be",
			"www.youtu.be", "foundation.wikimedia.com", "github.com",
			"www.github.com", "tiktok.com", "www.tiktok.com", "twitter.com",
			"www.twitter.com", "roboflow.com", "www.roboflow.com", "instagram.com",
			"www.instagram.com", "google.com", "www.google.com", "bing.com",
			"www.bing.com", "open.spotify",
		},
	}
}
