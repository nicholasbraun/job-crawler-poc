package robotstxt

import (
	"sync"
	"time"
)

// cache stores parsed robots.txt Rules keyed by hostname, bounded in both size
// and age so it cannot grow without limit across a long-running discovery crawl
// (which resolves tens of thousands of distinct hosts). This mirrors the DNS
// cache in internal/downloader: an entry expires after ttl, and a store at
// capacity first evicts expired entries, then arbitrary ones. It is safe for
// concurrent use and, paired with the Checker's singleflight, fetches each
// host's robots.txt at most once per miss.
//
// This is the per-process L1 tier (ADR-0032); a shared L2 (a dedicated
// cache-Redis) is deferred to horizontal scaling.
type cache struct {
	mu         sync.RWMutex
	byHostname map[string]entry

	// ttl caps how long a cached result is served before a re-fetch. negTTL does
	// the same for a transiently-unavailable verdict (a 5xx robots.txt, which
	// disallows all), kept short so a one-off server blip does not lock a host out
	// for the full ttl. maxEntries bounds the map. now is the clock, left as
	// time.Now in production and faked by testing/synctest in tests.
	ttl        time.Duration
	negTTL     time.Duration
	maxEntries int
	now        func() time.Time
}

// entry is a cached resolution: parsed Rules valid until expiry.
type entry struct {
	rules  Rules
	expiry time.Time
}

func newCache(ttl, negTTL time.Duration, maxEntries int) *cache {
	return &cache{
		byHostname: map[string]entry{},
		ttl:        ttl,
		negTTL:     negTTL,
		maxEntries: maxEntries,
		now:        time.Now,
	}
}

// getOrFetch returns unexpired cached Rules for hostname, or calls fetchFn once
// and caches the result under the size and age bound. Double-checked locking
// avoids a redundant store when a concurrent caller populated a fresh entry
// while fetchFn ran.
func (c *cache) getOrFetch(hostname string, fetchFn func() (Rules, error)) (Rules, error) {
	c.mu.RLock()
	if e, ok := c.byHostname[hostname]; ok && c.now().Before(e.expiry) {
		c.mu.RUnlock()
		return e.rules, nil
	}
	c.mu.RUnlock()

	rules, err := fetchFn()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if e, ok := c.byHostname[hostname]; ok && c.now().Before(e.expiry) {
		return e.rules, nil
	}

	// A transiently-unavailable verdict (disallowAll from a 5xx robots.txt) is
	// cached only briefly (negTTL) so a one-second blip re-probes within minutes
	// instead of blocking every URL on the host for the full ttl. A real parsed
	// ruleset and the 404/410 allow-all keep the standard ttl.
	ttl := c.ttl
	if _, unavailable := rules.(disallowAll); unavailable {
		ttl = c.negTTL
	}
	c.storeLocked(hostname, entry{rules: rules, expiry: c.now().Add(ttl)})

	return rules, nil
}

// storeLocked inserts an entry, first evicting expired then arbitrary entries
// when the map is at capacity, so it stays bounded across a long crawl.
// Refreshing an already-present host does not grow the map and so skips
// eviction. Caller holds c.mu.
func (c *cache) storeLocked(hostname string, e entry) {
	if _, present := c.byHostname[hostname]; !present && len(c.byHostname) >= c.maxEntries {
		now := c.now()
		for k, v := range c.byHostname {
			if len(c.byHostname) < c.maxEntries {
				break
			}
			if now.After(v.expiry) {
				delete(c.byHostname, k)
			}
		}
		for k := range c.byHostname {
			if len(c.byHostname) < c.maxEntries {
				break
			}
			delete(c.byHostname, k)
		}
	}
	c.byHostname[hostname] = e
}
