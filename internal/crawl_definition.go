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

// ErrDiscoveryDefinitionExists is returned by CrawlDefinitionRepository.Create
// when a discovery definition already exists: the singleton Discovery Crawl
// invariant (ADR-0017) permits only one. Callers map it to 409 Conflict.
var ErrDiscoveryDefinitionExists = errors.New("crawler: discovery definition already exists")

// CrawlKind distinguishes the crawl strategies. Two kinds are live: discovery
// walks a site following the URL filters, cataloguing career pages; collection
// (ADR-0036) is the periodic whole-Catalog Cycle that fills and keeps-live the
// Corpus (ATS fetch + crawl walk + refetch). "keyword" is a retired kind (the
// Keyword Crawl lane was removed); rows with that kind may still exist in the
// database until the Corpus schema cutover, so callers must treat any unknown
// kind as unsupported rather than assume it is absent.
type CrawlKind string

const (
	CrawlKindDiscovery  CrawlKind = "discovery"
	CrawlKindCollection CrawlKind = "collection"
)

// CollectionDefinitionID is the fixed id of the singleton Collection Crawl
// definition seeded at migration 0019 (ADR-0036). Constant so the scheduler
// (#191) and the startRun path resolve the one Collection definition without a
// lookup.
var CollectionDefinitionID = uuid.MustParse("00000000-0000-0000-0000-00000c011ec7")

// URLFilterConfig captures the URL filtering rules for a crawl. A definition
// carries its own filters; a create request that omits them is filled with the
// built-in DefaultURLFilterConfig by the API.
type URLFilterConfig struct {
	AllowedTLDs           []string `json:"allowedTLDs"`
	BlockedSubdomains     []string `json:"blockedSubdomains"`
	BlockedPathSegments   []string `json:"blockedPathSegments"`
	BlockedHostnames      []string `json:"blockedHostnames"`
	BlockedFileExtensions []string `json:"blockedFileExtensions"`
	PassSubdomains        []string `json:"passSubdomains"`
	PassPathSegments      []string `json:"passPathSegments"`
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

	// ExtractJobLinkSaturationCount is the Extract Gate's OWN job-link saturation
	// count (ADR-0019, #115): the number of distinct same-host Job Listing links at
	// which ShouldExtract rejects a page as a jobs index rather than send it to the
	// extractor. It is deliberately SEPARATE from JobLinkSaturationCount (the
	// Discovery Gate's K) so calibrating the extract path against the Extract Gold
	// Set (ADR-0020) can never shift the Discovery Gate — the config coupling hazard
	// ADR-0019 calls out. The value is drawn from the Extract Gold Set: openings-index
	// hubs there carry 5 distinct same-host job links while single postings carry <=1,
	// so 5 rejects every gold hub while giving a real self-hosted posting's
	// "more openings" sidebar up to 4 sibling links of headroom (ATS postings, which
	// carry the largest sidebars, are exempt one rung earlier). It is the FIRST reject
	// signal to raise or cut if production shows over-drops. A value <= 0 leaves the
	// signal silent (jobLinkSaturation returns 0), the same fail-safe as the Discovery
	// count — the escape hatch for dropping saturation entirely.
	ExtractJobLinkSaturationCount int
}

// DefaultLLMGateConfig returns the built-in pre-LLM gate signals. CareerPathSignals
// is intentionally a high-precision set: a bare page on one of these paths is
// cataloged as a Career Page (or, on the keyword path, treated as an index) with no
// LLM confirmation, so only path tokens that are almost always a jobs hub belong
// here. Weaker, ambiguous tokens (e.g. "join", which is as often a newsletter or
// community signup) are deliberately left out; the pagegate content heuristic still
// accepts them, but as uncertain — the LLM confirms before cataloging.
//
// It also seeds the final-rung Confidence Score floats (ADR-0016). A same-host
// openings index carrying a career keyword certain-accepts from the final rung
// once the index is dense enough: the career keyword (0.5) plus the same-host Job
// Listing signal (up to 1.0, folding the distinct link count in as min(count/5, 1))
// crosses CertainThreshold 1.25 at four same-host links (0.5 + 0.8 = 1.3), before
// full saturation. Sparser lexical-only evidence never crosses (the reject and
// structural-signal notes below cover the uncertain and reject bands). A
// structured-data openings index (JSON-LD ItemList of
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
		// certain-accepts a career keyword plus a dense same-host index — from four
		// same-host links up (0.5 + 0.8 = 1.3), before full saturation — while
		// saturated links alone (1.0) and lexical evidence alone (career keyword +
		// title strength = 1.0) stay uncertain, holding False-Certains at zero. RejectThreshold 0.75 rejects a no-signal page and a
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

		// Extract Gate saturation count (ADR-0019, #115), separate from the
		// Discovery Gate's JobLinkSaturationCount above. Drawn from the Extract Gold
		// Set: openings-index hubs carry 5 distinct same-host job links, single
		// postings carry <=1, so 5 rejects every gold hub with zero false-drops.
		ExtractJobLinkSaturationCount: 5,
	}
}

// CrawlDefinition is the persisted specification of a crawl: what to crawl and
// how. A definition is immutable once created; each execution of it is a
// CrawlRun.
type CrawlDefinition struct {
	ID        uuid.UUID
	Name      string
	Kind      CrawlKind
	SeedURLs  []string
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
	// AppendSeedURL idempotently adds url to the definition's seed_urls:
	// re-adding an existing Seed is a no-op. It is the one sanctioned mutation of
	// an otherwise immutable definition (ADR-0018: additive runtime Seed injection
	// into the Discovery Crawl). Returns ErrNotFound when no definition has the id.
	AppendSeedURL(ctx context.Context, id uuid.UUID, url string) error
}

// DefaultURLFilterConfig returns the built-in URL filtering rules applied to a
// crawl definition when a create request omits its own. These tuned lists steer
// a crawl toward company career pages: they restrict TLDs, short-circuit-pass
// hiring-related subdomains and path segments, and block the subdomains, path
// segments, hostnames, and file extensions that reliably lead away from job
// listings (docs, shops, auth, social media, media assets, and so on).
// Previously sourced from config.json; now the process-wide default lives here
// in the domain.
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
			// CMS internals and media directories: WordPress/TYPO3/Drupal asset
			// trees that hold only binaries, never a page that links to a company.
			"wp-content", "wp-json", "wp-includes", "wp-admin",
			"uploads", "fileadmin", "plugins", "themes",
			// Taxonomy / archive index pages. These are duplicative navigation over
			// a site's own posts (every post reappears under each tag, category, and
			// author), not the posts themselves -- the frontier bloats without
			// reaching new outbound links. The editorial posts they index stay
			// crawlable: /blog, /news and date-permalink posts are not blocked, so a
			// blog's outbound company links are still harvested (see note above).
			"category", "categories", "tag", "tags",
			"author", "authors", "archive", "archives",
			// Site search results: an unbounded query space with no unique content.
			"search",
		},
		BlockedFileExtensions: []string{
			// Documents: never HTML, never a source of outbound company links.
			"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx",
			"odt", "ods", "odp", "rtf", "csv",
			// Images.
			"jpg", "jpeg", "png", "gif", "svg", "webp", "bmp", "ico",
			"tif", "tiff",
			// Archives and installers.
			"zip", "rar", "gz", "tar", "7z", "bz2", "dmg", "exe", "pkg",
			// Audio / video.
			"mp4", "mp3", "mov", "avi", "wmv", "m4a", "m4v", "wav",
			"ogg", "webm", "flv", "mkv", "m3u", "m3u8",
			// Fonts.
			"woff", "woff2", "ttf", "otf", "eot",
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

// collectionBlockedEditorialPaths are the editorial/marketing path segments a
// Collection Crawl blocks that Discovery deliberately leaves crawlable. Discovery
// walks blogs/news for their outbound company links; a Collection Cycle already
// knows its companies (it seeds from the Catalog) and only wants each Company's
// careers/jobs subtree, so it sheds these editorial subtrees to keep the walk
// narrow (ADR-0036). Both singular and plural forms, since the URL filter matches
// per path segment exactly.
var collectionBlockedEditorialPaths = []string{
	"blog", "news", "press", "media", "articles", "article",
	"stories", "story", "posts", "post", "magazine",
}

// DefaultCollectionURLFilterConfig returns the narrow URL filter for the
// singleton Collection Crawl (ADR-0036): the discovery default plus the editorial
// path segments Discovery leaves crawlable, so a Cycle stays on each Company's
// careers/jobs subtrees instead of roaming its blog and press pages. It is
// reference/parity only — the authoritative filter is the seeded definition row
// (migration 0019); a testcontainers assertion pins the two together so they
// cannot drift.
func DefaultCollectionURLFilterConfig() URLFilterConfig {
	cfg := DefaultURLFilterConfig()
	cfg.BlockedPathSegments = append(cfg.BlockedPathSegments, collectionBlockedEditorialPaths...)
	return cfg
}

// DefaultDiscoverySeeds returns the baseline Seed set for the singleton Discovery
// Crawl: startup directories and VC portfolios (Germany / EU focus). It lives in
// the domain rather than a docs file so a database reset can never lose it; the
// Discovery start modal prefills its editable Seed list from here via the API
// defaults endpoint. Previously frozen in docs/discovery-baseline-definition.json.
func DefaultDiscoverySeeds() []string {
	return []string{
		"https://www.eu-startups.com/directory/",
		"https://www.startupbrett.de/startups/",
		"https://www.deutsche-startups.de/startup-datenbank/",
		"https://startup-map.berlin/",
		"https://www.gruenderszene.de/datenbank",
		"https://dealroom.co/companies",
		"https://www.crunchbase.com/hub/germany-startups",
		"https://www.f6s.com/companies/germany",
		"https://www.rocketinternet.com/companies",
		"https://www.earlybird.com/portfolio",
		"https://www.hv.capital/portfolio",
		"https://www.pointnine.com/portfolio",
		"https://cherry.vc/portfolio",
		"https://www.projecta.com/portfolio",
		"https://www.holtzbrinck-ventures.com/portfolio/",
		"https://www.speedinvest.com/portfolio",
		"https://www.lakestar.com/portfolio",
		"https://www.techstars.com/portfolio",
		"https://www.ycombinator.com/companies?regions=Europe",
		"https://www.startupberlin.io/",
		"https://www.germanaccelerator.com/portfolio/",
		"https://www.bitkom.org/Mitglieder",
		"https://www.startupverband.de/mitglieder/",
		"https://www.wko.at/startups",
		"https://www.swissstartupradar.ch/",
	}
}
