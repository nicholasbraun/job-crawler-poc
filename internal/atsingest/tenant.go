package atsingest

import "sync"

// tenantSet is the per-run dedup set that guarantees a given ATS tenant is
// fetched at most once a run (ADR-0022), no matter how many Seeds or embedded
// boards point at it. It is safe for concurrent use by the pool workers, the seed
// priming goroutine, and (in #129) the embed trigger.
type tenantSet struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newTenantSet() *tenantSet {
	return &tenantSet{seen: map[string]struct{}{}}
}

// Add records key and reports whether it was newly inserted: true the first time
// key is seen, false on every later call. The check-and-insert is atomic under
// the mutex, so concurrent Adds of the same key admit exactly one.
func (s *tenantSet) Add(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[key]; ok {
		return false
	}
	s.seen[key] = struct{}{}
	return true
}
