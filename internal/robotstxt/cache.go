package robotstxt

import (
	"sync"
)

type cache struct {
	rulesByHostname map[string]Rules
	mu              sync.RWMutex
}

// getOrFetch returns cached Rules for hostname, or calls fetchFn once and
// caches the result. Uses double-checked locking to avoid redundant fetches
// under concurrent access.
func (c *cache) getOrFetch(hostname string, fetchFn func() (Rules, error)) (Rules, error) {
	c.mu.RLock()

	if rules, ok := c.rulesByHostname[hostname]; ok {
		c.mu.RUnlock()
		return rules, nil
	}
	c.mu.RUnlock()

	rules, err := fetchFn()
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if rules, ok := c.rulesByHostname[hostname]; ok {
		return rules, nil
	}

	c.rulesByHostname[hostname] = rules

	return rules, nil
}

func newCache() *cache {
	return &cache{
		rulesByHostname: map[string]Rules{},
	}
}
