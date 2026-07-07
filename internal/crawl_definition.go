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

// URLFilterConfig captures the URL filtering rules for a crawl. It mirrors the
// list fields of config.Config so a definition can carry its own filters; when
// a field is empty the server fills it from the process-wide config.json.
type URLFilterConfig struct {
	AllowedTLDs         []string `json:"allowedTLDs"`
	BlockedSubdomains   []string `json:"blockedSubdomains"`
	BlockedPathSegments []string `json:"blockedPathSegments"`
	BlockedHostnames    []string `json:"blockedHostnames"`
	PassSubdomains      []string `json:"passSubdomains"`
	PassPathSegments    []string `json:"passPathSegments"`
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
}
