package llmobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// defaultDupTTL bounds how long the cross-run content-hash sets live. This probe
// is a transient measurement aid (ADR-0007 step 1), not the durable verdict
// cache of step 3, so the sets self-expire rather than being swept by
// redisfrontier.DeleteRun.
const defaultDupTTL = 7 * 24 * time.Hour

// DupProbe measures how often identical page content is fed to the LLM -- an
// upper bound on what step 3's content-hash verdict cache would save. It hashes
// the exact MainContent sent to the model and records recurrence in a Redis set
// shared across runs, so both within-run and cross-run duplicates count. The set
// is keyed by kind (the classifier and extractor cache separately in step 3) and
// is TTL-bounded and never persisted.
type DupProbe struct {
	client *redis.Client
	ttl    time.Duration
}

// DupProbeOption configures a DupProbe.
type DupProbeOption func(*DupProbe)

// WithDupTTL overrides how long the content-hash sets live before expiring.
func WithDupTTL(ttl time.Duration) DupProbeOption {
	return func(p *DupProbe) { p.ttl = ttl }
}

// NewDupProbe builds a probe over the given Redis client. A nil client yields a
// probe whose Observe is a no-op reporting every content as unique.
func NewDupProbe(client *redis.Client, opts ...DupProbeOption) *DupProbe {
	p := &DupProbe{client: client, ttl: defaultDupTTL}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Observe records that content was fed to the given LLM kind and reports whether
// its hash had been seen before (this run or a prior one). The set add and TTL
// refresh run in one round trip. A nil probe or client reports unique, so
// unconfigured setups and tests need no guarding.
func (p *DupProbe) Observe(ctx context.Context, kind Kind, content string) (duplicate bool, err error) {
	if p == nil || p.client == nil {
		return false, nil
	}
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])
	key := "llm:dupprobe:" + string(kind) + ":seen"

	pipe := p.client.Pipeline()
	added := pipe.SAdd(ctx, key, hash)
	pipe.Expire(ctx, key, p.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	// SAdd returns the count of newly-added members; 0 means the hash was already
	// in the set -- i.e. this content is a duplicate.
	return added.Val() == 0, nil
}
