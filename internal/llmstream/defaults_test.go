package llmstream

import (
	"testing"
	"time"
)

// TestDefaultMinIdleFloor pins the #227 fail-safe: the default first-reclaim crash
// window a caller gets when it omits WithMinIdle must stay ABOVE the LLM operation
// timeout, so a slow-but-alive worker's in-flight entry is never reclaimed and
// re-processed into a duplicate model call. It is a pure, Docker-free guard on the
// constant relationship, not a behavioral test of the reclaimer.
func TestDefaultMinIdleFloor(t *testing.T) {
	if defaultMinIdle <= llmOpTimeout {
		t.Errorf("defaultMinIdle (%s) must exceed the LLM op timeout (%s): a live worker on a slow model call would be reclaimed and double-called",
			defaultMinIdle, llmOpTimeout)
	}
	// Guard against llmOpTimeout silently drifting below the 5m OpenRouter default
	// it mirrors, which would quietly re-open the footgun.
	if llmOpTimeout < 5*time.Minute {
		t.Errorf("llmOpTimeout (%s) fell below the 5m openrouter default it mirrors", llmOpTimeout)
	}
}
