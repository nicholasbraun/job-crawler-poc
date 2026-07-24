package llmstream

import (
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/openrouter"
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
}

// TestLLMOpTimeoutMirrorsOpenrouter locks llmOpTimeout to the openrouter client's
// DefaultTimeout that it mirrors. Production llmstream deliberately does not import
// openrouter, so the constant is copied by hand; this white-box test is free to
// import openrouter (there is no import cycle) and fails the moment the two drift.
// Without it, lowering openrouter.DefaultTimeout would silently drop llmOpTimeout's
// mirror below the real timeout and quietly re-open the reclaim-double-call footgun.
func TestLLMOpTimeoutMirrorsOpenrouter(t *testing.T) {
	if llmOpTimeout != openrouter.DefaultTimeout {
		t.Errorf("llmOpTimeout (%s) drifted from openrouter.DefaultTimeout (%s); update the mirror in llmstream.go",
			llmOpTimeout, openrouter.DefaultTimeout)
	}
}
