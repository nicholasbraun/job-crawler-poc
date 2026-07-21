package redis_test

import (
	"encoding/binary"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/google/uuid"
	crawler "github.com/nicholasbraun/job-crawler-poc/internal"
	"github.com/nicholasbraun/job-crawler-poc/internal/frontier"
	redisfrontier "github.com/nicholasbraun/job-crawler-poc/internal/frontier/redis"
	"github.com/redis/go-redis/v9"
)

// wantVisitedMember recomputes the impl's 8-byte big-endian xxhash64 visited
// member independently, so the tests assert the byte layout rather than trust it.
func wantVisitedMember(raw string) string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], xxhash.Sum64String(raw))
	return string(b[:])
}

// TestVisitedCap covers the hashed FIFO-capped visited ZSET (ADR-0027 / #161):
// the visited type/member shape, dedup, the per-run cap and its FIFO eviction,
// re-admission of an evicted URL, and that the full member stays on the queue.
func TestVisitedCap(t *testing.T) {
	client := newTestClient(t)

	t.Run("visited is a hashed ZSET with an ms score", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id)
		prefix := "frontier:" + id.String() + ":"
		raw := "http://a/1"

		before := time.Now().UnixMilli()
		if err := f.AddURL(ctx, url("a", raw, 0)); err != nil {
			t.Fatalf("AddURL: %v", err)
		}
		after := time.Now().UnixMilli()

		if typ := client.Type(ctx, prefix+"visited").Val(); typ != "zset" {
			t.Fatalf("visited type = %q, want zset", typ)
		}
		if n := client.ZCard(ctx, prefix+"visited").Val(); n != 1 {
			t.Fatalf("visited ZCard = %d, want 1", n)
		}
		got := client.ZRangeWithScores(ctx, prefix+"visited", 0, -1).Val()
		if len(got) != 1 {
			t.Fatalf("ZRangeWithScores len = %d, want 1", len(got))
		}
		member, _ := got[0].Member.(string)
		if len(member) != 8 {
			t.Errorf("visited member length = %d, want 8", len(member))
		}
		if member != wantVisitedMember(raw) {
			t.Errorf("visited member = %x, want %x", member, wantVisitedMember(raw))
		}
		if score := int64(got[0].Score); score < before || score > after {
			t.Errorf("visited score = %d, want in [%d, %d]", score, before, after)
		}
	})

	t.Run("dedup under cap: repeat add is a DUP no-op with no score bump", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id)
		prefix := "frontier:" + id.String() + ":"
		raw := "http://a/1"
		u := url("a", raw, 0)

		if err := f.AddURL(ctx, u); err != nil {
			t.Fatalf("AddURL first: %v", err)
		}
		s1 := client.ZScore(ctx, prefix+"visited", wantVisitedMember(raw)).Val()

		time.Sleep(3 * time.Millisecond)
		if err := f.AddURL(ctx, u); err != nil {
			t.Fatalf("AddURL repeat: %v", err)
		}

		if n := client.ZCard(ctx, prefix+"visited").Val(); n != 1 {
			t.Errorf("visited ZCard after repeat = %d, want 1", n)
		}
		if s2 := client.ZScore(ctx, prefix+"visited", wantVisitedMember(raw)).Val(); s2 != s1 {
			t.Errorf("visited score bumped on DUP: got %v, want unchanged %v", s2, s1)
		}
		if n := client.LLen(ctx, prefix+"q:a").Val(); n != 1 {
			t.Errorf("queue len after DUP = %d, want 1 (no double-enqueue)", n)
		}

		got, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.RawURL != raw {
			t.Errorf("Next RawURL = %q, want %q", got.RawURL, raw)
		}
		if err := f.MarkDone(ctx, got.RawURL); err != nil {
			t.Fatalf("MarkDone: %v", err)
		}
		if _, err := f.Next(ctx); !errors.Is(err, frontier.ErrDone) {
			t.Errorf("second Next err = %v, want ErrDone", err)
		}
	})

	t.Run("cap/FIFO eviction: cardinality never exceeds the cap, oldest go first", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithVisitedCap(3))
		prefix := "frontier:" + id.String() + ":"

		raws := []string{
			"http://h0/1", "http://h1/1", "http://h2/1", "http://h3/1", "http://h4/1",
		}
		hosts := []string{"h0", "h1", "h2", "h3", "h4"}
		for i, raw := range raws {
			if i > 0 {
				time.Sleep(2 * time.Millisecond) // strictly increasing scores => deterministic FIFO
			}
			if err := f.AddURL(ctx, url(hosts[i], raw, 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
			if n := client.ZCard(ctx, prefix+"visited").Val(); n > 3 {
				t.Fatalf("after add %d visited ZCard = %d, want <= 3", i, n)
			}
		}

		if n := client.ZCard(ctx, prefix+"visited").Val(); n != 3 {
			t.Fatalf("final visited ZCard = %d, want 3", n)
		}
		// The first two inserted are evicted; the last three remain resident.
		for _, raw := range raws[:2] {
			if err := client.ZScore(ctx, prefix+"visited", wantVisitedMember(raw)).Err(); !errors.Is(err, redis.Nil) {
				t.Errorf("evicted %q ZScore err = %v, want redis.Nil", raw, err)
			}
		}
		for _, raw := range raws[2:] {
			if err := client.ZScore(ctx, prefix+"visited", wantVisitedMember(raw)).Err(); err != nil {
				t.Errorf("resident %q ZScore err = %v, want found", raw, err)
			}
		}
	})

	t.Run("re-admission: an evicted URL re-added is NEW and re-enqueued", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithVisitedCap(2))
		prefix := "frontier:" + id.String() + ":"

		if err := f.AddURL(ctx, url("hostA", "http://hostA/1", 0)); err != nil {
			t.Fatalf("AddURL u1: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
		if err := f.AddURL(ctx, url("hostB", "http://hostB/1", 0)); err != nil {
			t.Fatalf("AddURL u2: %v", err)
		}
		if n := client.LLen(ctx, prefix+"q:hostA").Val(); n != 1 {
			t.Fatalf("q:hostA len = %d, want 1", n)
		}

		time.Sleep(2 * time.Millisecond)
		if err := f.AddURL(ctx, url("hostC", "http://hostC/1", 0)); err != nil {
			t.Fatalf("AddURL u3: %v", err)
		}
		// u1 is the oldest, so u3's insert evicts it.
		if err := client.ZScore(ctx, prefix+"visited", wantVisitedMember("http://hostA/1")).Err(); !errors.Is(err, redis.Nil) {
			t.Fatalf("u1 ZScore err = %v, want redis.Nil (evicted)", err)
		}

		// Re-adding the evicted u1 is NEW again, so it is re-enqueued.
		if err := f.AddURL(ctx, url("hostA", "http://hostA/1", 0)); err != nil {
			t.Fatalf("AddURL u1 re-add: %v", err)
		}
		if n := client.LLen(ctx, prefix+"q:hostA").Val(); n != 2 {
			t.Errorf("q:hostA len after re-admission = %d, want 2 (re-enqueued)", n)
		}
	})

	t.Run("queue member keeps the full encoded member; only visited is hashed", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id)
		prefix := "frontier:" + id.String() + ":"
		want := crawler.URL{
			Depth:    1,
			Hostname: "acme.com",
			Scope:    "acme-scope",
			Owner:    "acme-owner",
			RawURL:   "http://acme.com/jobs",
		}

		if err := f.AddURL(ctx, want); err != nil {
			t.Fatalf("AddURL: %v", err)
		}

		queued := client.LRange(ctx, prefix+"q:acme.com", 0, -1).Val()
		if len(queued) != 1 {
			t.Fatalf("queue len = %d, want 1", len(queued))
		}
		qm := queued[0]
		if !strings.Contains(qm, "\x1f") {
			t.Errorf("queue member %q missing \\x1f separators", qm)
		}
		if !strings.Contains(qm, want.RawURL) {
			t.Errorf("queue member %q missing full RawURL %q", qm, want.RawURL)
		}
		vm := client.ZRange(ctx, prefix+"visited", 0, -1).Val()
		if len(vm) != 1 {
			t.Fatalf("visited len = %d, want 1", len(vm))
		}
		if len(vm[0]) != 8 {
			t.Errorf("visited member length = %d, want 8", len(vm[0]))
		}
		if vm[0] == qm {
			t.Errorf("visited member equals queue member; visited should be hashed")
		}

		got, err := f.Next(ctx)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got != want {
			t.Errorf("Next round-trip = %+v, want %+v", got, want)
		}
	})

	t.Run("a too-deep URL is rejected before the add script and never counts against the cap", func(t *testing.T) {
		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id, redisfrontier.WithMaxDepth(2))
		prefix := "frontier:" + id.String() + ":"

		if err := f.AddURL(ctx, url("a", "http://a/deep", 3)); !errors.Is(err, frontier.ErrMaxDepth) {
			t.Fatalf("AddURL too-deep err = %v, want ErrMaxDepth", err)
		}
		if n := client.Exists(ctx, prefix+"visited").Val(); n != 0 {
			t.Errorf("visited key exists after too-deep reject (n=%d); the add script must not have run", n)
		}
	})

	t.Run("default cap is 5,000,000", func(t *testing.T) {
		if redisfrontier.DefaultVisitedCap != 5_000_000 {
			t.Fatalf("DefaultVisitedCap = %d, want 5000000", redisfrontier.DefaultVisitedCap)
		}

		ctx := t.Context()
		id := uuid.New()
		f := redisfrontier.New(client, id) // no WithVisitedCap => default
		prefix := "frontier:" + id.String() + ":"
		for i := 0; i < 10; i++ {
			raw := "http://d/" + strconv.Itoa(i)
			if err := f.AddURL(ctx, url("d", raw, 0)); err != nil {
				t.Fatalf("AddURL %s: %v", raw, err)
			}
		}
		if n := client.ZCard(ctx, prefix+"visited").Val(); n != 10 {
			t.Errorf("visited ZCard under default cap = %d, want 10 (no eviction)", n)
		}
	})
}
