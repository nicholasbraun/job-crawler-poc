package collection

import (
	"context"
	"errors"
	"fmt"

	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/catalog"
	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
)

// ErrRobotsDisallowed marks a refetch that the lane's robots.txt re-check
// short-circuited before any network GET (ADR-0040). The lane's status
// classifiers map it to ProbeInconclusive — "cannot verify, let it decay" —
// never to a hard close, so a fresh Disallow (or a transient robots.txt outage
// the checker reports as disallow-all) rides the staleness backstop.
var ErrRobotsDisallowed = errors.New("collection: robots.txt disallows refetch")

// robotsChecker re-validates a URL against its host's robots.txt on refetch.
// Satisfied in production by *robotstxt.Checker (already cache-warm from the
// collection url-worker pool); a fake stands in for tests.
type robotsChecker interface {
	Check(ctx context.Context, u string) error
}

// hostLimiter spaces successive fetches sharing a key (the Politeness Domain).
// Satisfied in production by *atsingest.HostLimiter (reused in place, ADR-0040);
// a spy stands in for tests.
type hostLimiter interface {
	Wait(ctx context.Context, key string) error
}

// politeDownloader wraps the refetch lane's downloader with the two Frontier
// politeness guarantees the lane otherwise bypasses (ADR-0040): a robots.txt
// re-check and per-Politeness-Domain spacing. Per Get it does, in order,
// robots-check → per-registrable-domain spacing wait → inner GET. Robots is
// checked FIRST so a URL we won't fetch never burns a spacing slot, and the
// decorator sits OUTSIDE the retry client so robots is checked once per logical
// request (re-attempts self-space via the retry client's backoff).
type politeDownloader struct {
	inner           downloader.Downloader
	robots          robotsChecker
	limiter         hostLimiter
	onRobotsBlocked func(ctx context.Context) // optional; nil-safe
}

var _ downloader.Downloader = (*politeDownloader)(nil)

// NewPoliteDownloader builds the refetch lane's politeness decorator around inner.
// robots and limiter are the SHARED, already-constructed checker and per-host
// limiter (one instance across all refetch workers, so a page's probe + its N
// listings and two workers hitting the same platform share one spacing bucket).
// onRobotsBlocked is an optional counter hook, fired once per robots
// short-circuit; nil disables it.
func NewPoliteDownloader(inner downloader.Downloader, robots robotsChecker, limiter hostLimiter, onRobotsBlocked func(ctx context.Context)) downloader.Downloader {
	return &politeDownloader{inner: inner, robots: robots, limiter: limiter, onRobotsBlocked: onRobotsBlocked}
}

// Get re-checks robots.txt, spaces per Politeness Domain, then delegates to the
// inner downloader — in that order (ADR-0040). Any robots error short-circuits
// with ErrRobotsDisallowed before any spacing wait or network GET.
func (d *politeDownloader) Get(ctx context.Context, url string) (*downloader.Response, error) {
	// Robots first: ADR-0040 deliberately conflates a real Disallow, a robots.txt
	// 5xx the checker folds to disallow-all, and a robots.txt fetch failure — all
	// are "cannot verify → decay." (The checker already folds a 404/410 to
	// allow=nil.) A short-circuit here never burns a spacing slot nor calls inner.
	if err := d.robots.Check(ctx, url); err != nil {
		if d.onRobotsBlocked != nil {
			d.onRobotsBlocked(ctx)
		}
		return nil, fmt.Errorf("collection: robots re-check blocked %q: %w", url, ErrRobotsDisallowed)
	}

	// Space per Politeness Domain. A cancelled wait returns the context error and
	// never calls inner. The nil guard is defensive parity with the nil-safe hook;
	// production and tests always pass a limiter.
	if d.limiter != nil {
		if err := d.limiter.Wait(ctx, d.spacingKey(url)); err != nil {
			return nil, err
		}
	}

	return d.inner.Get(ctx, url)
}

// spacingKey folds url to its Politeness Domain — the registrable domain
// (eTLD+1) per ADR-0040 — so a platform's tenant subdomains share one spacing
// bucket. Falls back to the raw url on a parse failure (degenerate but safe:
// over-spacing, never under). Reached only after robots.Check parsed the URL.
func (d *politeDownloader) spacingKey(url string) string {
	if u, err := crawler.NewURL(url); err == nil {
		if rd := catalog.RegistrableDomain(u.Hostname); rd != "" {
			return rd
		}
	}
	return url
}
