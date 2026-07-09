package llmobs_test

import (
	"testing"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/llmobs"
)

func TestDupProbeObserve(t *testing.T) {
	client := newTestClient(t)
	probe := llmobs.NewDupProbe(client, llmobs.WithDupTTL(time.Hour))
	ctx := t.Context()

	observe := func(kind llmobs.Kind, content string) bool {
		t.Helper()
		dup, err := probe.Observe(ctx, kind, content)
		if err != nil {
			t.Fatalf("Observe(%s, %q) error: %v", kind, content, err)
		}
		return dup
	}

	// First sighting of content is unique; a repeat is a duplicate.
	if observe(llmobs.KindClassify, "A") {
		t.Error("first Observe of content A should be unique")
	}
	if !observe(llmobs.KindClassify, "A") {
		t.Error("second Observe of the same content A should be a duplicate")
	}

	// Distinct content is unique.
	if observe(llmobs.KindClassify, "B") {
		t.Error("Observe of new content B should be unique")
	}

	// Each kind keys its own set: content A is unseen under extract even though
	// it recurred under classify.
	if observe(llmobs.KindExtract, "A") {
		t.Error("content A under extract should be unique (per-kind sets)")
	}
	if !observe(llmobs.KindExtract, "A") {
		t.Error("second Observe of content A under extract should be a duplicate")
	}

	// The set carries the configured TTL (Expire ran alongside the add).
	ttl, err := client.TTL(ctx, "llm:dupprobe:classify:seen").Result()
	if err != nil {
		t.Fatalf("TTL error: %v", err)
	}
	if ttl <= 0 || ttl > time.Hour {
		t.Errorf("classify set TTL = %v, want (0, 1h]", ttl)
	}
}
