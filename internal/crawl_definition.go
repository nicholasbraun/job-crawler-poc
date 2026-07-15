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
// As of ADR-0016 it also carries the final-rung Confidence Score weights and
// thresholds -- this is the shift from curated string lists to curated lists plus
// tunable floats -- but it stays in-memory and process-wide, still with no json
// tags.
type LLMGateConfig struct {
	CareerPathSignals []string
	RejectPathSignals []string

	// Confidence Score weights and thresholds for the Gate's final rung
	// (ADR-0016). The final rung sums the weight of each fired signal into an
	// additive Confidence Score, then maps it to the Gate's three verdicts:
	// score >= CertainThreshold certain-accepts (skips the LLM), score <=
	// RejectThreshold rejects, and the band between stays uncertain (the LLM
	// confirms). Weights are hand-set and coarse; only the two thresholds are
	// tuned against the Gold Set. Signal tickets add further weights.
	//
	// CareerKeywordWeight scores a career keyword found in the URL or title (the
	// weakest signal). JobLinkWeight scores a saturated set of same-host Job
	// Listing links; JobLinkSaturationCount (K) sets how many distinct links
	// saturate it, the count folding in continuously as min(count/K, 1).
	CareerKeywordWeight float64
	JobLinkWeight       float64
	// JobLinkSaturationCount is K in the same-host Job Listing signal's
	// min(count/K, 1) saturation: the distinct same-host Job Listing link count
	// at which that signal contributes its full JobLinkWeight (ADR-0016). Hand-set,
	// not bench-tuned. A value <= 0 leaves the signal silent (fail-safe), so a
	// zero-value config never divides by zero or over-weights.
	JobLinkSaturationCount int
	// JSONLDHubWeight scores a structured-data openings index (ADR-0016): the
	// page's JSON-LD is an ItemList containing a JobPosting, or carries two or
	// more JobPosting nodes. It is a strong Structural Signal -- seeded at or
	// above CertainThreshold so it certain-accepts on its own -- because such
	// markup is definitive of a Career Page hub. A lone JobPosting node (one Job
	// Listing, not a hub) contributes nothing, so this signal never turns a single
	// posting into a False-Certain. A zero value (an override that omits it)
	// leaves the signal silent, the same fail-safe as JobLinkSaturationCount <= 0.
	JSONLDHubWeight float64
	// ATSEmbedWeight scores an ATS Embed (ADR-0016): a Company page that renders a
	// third-party ATS board inline — an <iframe> pointing at a known ATS host, or a
	// <script> pointing at a known ATS host together with that provider's
	// board-container marker. It is the strongest Structural Signal — seeded at or
	// above CertainThreshold so it certain-accepts on its own — because an embedded
	// ATS board is definitive of a Career Page hub. A zero value (an override that
	// omits it) leaves the signal silent, the same fail-safe as JobLinkSaturationCount <= 0.
	ATSEmbedWeight float64
	// TitleStrengthWeight scores the title-strength / strong-careers signal
	// (ADR-0016): the page's Title leads with a careers-hub word, or the URL
	// carries a career token as a distinct exact path segment. It is weighted
	// ABOVE the weakest career-keyword substring so a page that reads as a careers
	// hub outweighs one that merely contains a career keyword. A zero value leaves
	// the signal silent, the same fail-safe as the other weights.
	TitleStrengthWeight float64
	CertainThreshold    float64
	RejectThreshold     float64
}

// DefaultLLMGateConfig returns the built-in pre-LLM gate signals. CareerPathSignals
// is intentionally a high-precision set: a bare page on one of these paths is
// cataloged as a Career Page (or, on the keyword path, treated as an index) with no
// LLM confirmation, so only path tokens that are almost always a jobs hub belong
// here. Weaker, ambiguous tokens (e.g. "join", which is as often a newsletter or
// community signup) are deliberately left out; the pagegate content heuristic still
// accepts them, but as uncertain — the LLM confirms before cataloging.
//
// It also seeds the final-rung Confidence Score floats (ADR-0016). A dense
// same-host openings index carrying a career keyword now certain-accepts from the
// final rung (career keyword 0.5 + a saturated same-host Job Listing set 1.0 =
// 1.5 >= CertainThreshold 1.25); every weaker combination — saturated links alone,
// a keyword alone, or a keyword plus a thin set of links — stays uncertain and
// still reaches the LLM. A structured-data openings index (JSON-LD ItemList of
// JobPosting, or >=2 JobPosting nodes) contributes 1.5, certain-accepting on its
// own; a lone JobPosting contributes nothing. An ATS Embed (1.5) certain-accepts
// on its own too, like the JSON-LD hub: a Company page rendering a third-party ATS
// board inline (an iframe to a known ATS host, or a script to one with the
// provider's board-container marker present).
//
// The title-strength signal (0.5) fires when the page reads as a careers hub — its
// Title leads with a careers-hub word, or the URL carries a career token as a
// distinct exact path segment — lifting a page above the weakest career keyword.
// The two thresholds sit at the midpoints of the widest failure-free score gaps,
// placed for margin rather than at the call-rate minimum: RejectThreshold 0.75
// auto-rejects a page whose only signal is the weak career keyword (0.5 <= 0.75),
// while a keyword page that ALSO reads as a careers hub (0.5 + 0.5 = 1.0) clears
// reject and stays uncertain — never leaking a real career sub-page. And lexical
// evidence alone (career keyword + title strength = 1.0 < CertainThreshold 1.25)
// never certain-accepts, so certain still requires a Structural Signal (an ATS
// embed, a JSON-LD hub, or a career keyword plus a dense same-host index).
func DefaultLLMGateConfig() LLMGateConfig {
	return LLMGateConfig{
		CareerPathSignals: []string{
			"careers", "career", "jobs", "karriere",
			"stellenangebote", "vacancies",
		},
		RejectPathSignals: []string{
			// Editorial / content paths. The URL filter no longer blocks these
			// (they are crawled for their outbound links to companies), so they now
			// reach the gate: shed them here before the LLM, since an editorial page
			// is never itself a Career Page or Job Listing even when its copy trips
			// the careerish content heuristic (e.g. a post about "joining the team").
			// Both singular and plural forms, since the match is per-segment exact.
			"blog", "news", "press", "media", "articles", "article",
			"stories", "story", "posts", "post", "magazine",
			"authors", "author",
			// Legal / commercial boilerplate. Still blocked by the URL filter too,
			// so these rarely reach the gate; kept as a backstop.
			"legal", "privacy", "terms", "imprint", "impressum", "cookie",
			"gdpr", "pricing",
			// Software-docs and media-taxonomy paths. A framework's `/docs/.../jobs`
			// page or a media site's `/tag/careers` / `/category/jobs` page trips the
			// certain-accept career-hub rule purely because a path segment is a career
			// token; shed them here so they are rejected before the LLM, never
			// certain-accepted (#62 catalog audit false positives).
			"docs", "tag", "category",
		},

		// Confidence Score seeds (ADR-0016). The career keyword (weak) contributes
		// 0.5; title strength (a careers-hub title or an exact career path segment)
		// another 0.5; the same-host Job Listing signal up to 1.0, folding the
		// distinct same-host link count in as min(count/5, 1). CertainThreshold 1.25
		// certain-accepts a career keyword plus a saturated same-host index (0.5 +
		// 1.0 = 1.5), while saturated links alone (1.0) and lexical evidence alone
		// (career keyword + title strength = 1.0) stay uncertain, holding
		// False-Certains at zero. RejectThreshold 0.75 rejects a no-signal page and a
		// weak-keyword-only page (0.5), while a keyword page that also reads as a
		// careers hub (1.0) clears reject. Both thresholds land at the midpoints of
		// empty score gaps (0.25 margin each side) — placed for margin, not the
		// call-rate minimum.
		CareerKeywordWeight:    0.5,
		JobLinkWeight:          1.0,
		JobLinkSaturationCount: 5,
		JSONLDHubWeight:        1.5,
		ATSEmbedWeight:         1.5,
		TitleStrengthWeight:    0.5,
		CertainThreshold:       1.25,
		RejectThreshold:        0.75,
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
	Keywords  []string
	MaxDepth  int
	URLFilter URLFilterConfig
	CreatedAt time.Time
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
// segments, and hostnames that reliably lead away from job listings (docs,
// shops, auth, social media, and so on). Previously sourced from
// config.json; now the process-wide default lives here in the domain.
//
// Editorial paths (blog, news, press, media, articles, stories, posts, magazine)
// are intentionally NOT blocked here: they often link out to companies, so they
// are worth crawling for their outbound links. The ones that are also structural
// non-job pages (blog, news, press, media) are still shed at the pre-LLM gate
// (DefaultLLMGateConfig.RejectPathSignals), so we harvest their links without
// spending an LLM call on the page itself.
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
			"apps", "wiki", "foundation", "docs", "donate",
			"shop", "store", "marketplace", "help",
			"support", "forum", "community", "research", "discuss", "gist",
			"templates", "api", "books", "cdn", "static", "assets", "status",
			"staging", "dev", "test", "login", "auth", "sso", "accounts", "id",
			"ads", "mail", "email", "analytics", "tracking", "events",
		},
		BlockedPathSegments: []string{
			"add_to", "wiki", "signin", "users",
			"podcast", "learning",
			"products", "imprint", "impressum", "contact", "privacy",
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
