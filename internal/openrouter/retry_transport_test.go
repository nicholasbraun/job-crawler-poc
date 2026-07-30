package openrouter

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func fastTransport() *retryTransport {
	return &retryTransport{base: http.DefaultTransport, maxRetries: 4, backoff: time.Millisecond, maxBackoff: 5 * time.Millisecond}
}

func post(t *testing.T, client *http.Client, url, body string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	return client.Do(req)
}

// TestRetryTransport_RetriesThenSucceeds: two 429s then a 200; the request body is
// replayed intact on every attempt.
func TestRetryTransport_RetriesThenSucceeds(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if b, _ := io.ReadAll(r.Body); string(b) != "payload" {
			t.Errorf("attempt %d body = %q, want payload (body not replayed)", n, b)
		}
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := post(t, &http.Client{Transport: fastTransport()}, srv.URL, "payload")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("hits = %d, want 3 (2x429 + 1x200)", got)
	}
}

// TestRetryTransport_ExhaustsRetries: a persistent 429 returns the final response
// after initial + maxRetries attempts, not an error.
func TestRetryTransport_ExhaustsRetries(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	resp, err := post(t, &http.Client{Transport: fastTransport()}, srv.URL, "x")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 5 {
		t.Fatalf("hits = %d, want 5 (initial + 4 retries)", got)
	}
}

// TestRetryTransport_NonRetryableStatus: a 400 is returned immediately, no retry.
func TestRetryTransport_NonRetryableStatus(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := post(t, &http.Client{Transport: fastTransport()}, srv.URL, "x")
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("hits = %d, want 1 (no retry on 400)", got)
	}
}

// TestRetryTransport_RespectsContextCancel: a cancel during the backoff wait aborts
// with the context error rather than hanging or retrying on.
func TestRetryTransport_RespectsContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	// A long backoff so the cancel lands during the wait, not between requests.
	rt := &retryTransport{base: http.DefaultTransport, maxRetries: 10, backoff: time.Hour, maxBackoff: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, bytes.NewReader([]byte("x")))
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := (&http.Client{Transport: rt}).Do(req); err == nil {
		t.Fatal("want an error from the context cancel")
	}
}

func TestRetryAfter(t *testing.T) {
	cases := []struct {
		h    string
		want time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"0", 0},
		{"-3", 0},
		{"garbage", 0},
	}
	for _, tc := range cases {
		if got := retryAfter(tc.h); got != tc.want {
			t.Errorf("retryAfter(%q) = %v, want %v", tc.h, got, tc.want)
		}
	}
}

func TestRetryableStatus(t *testing.T) {
	retryable := []int{429, 502, 503, 504}
	notRetryable := []int{200, 201, 400, 401, 403, 404, 500}
	for _, c := range retryable {
		if !retryableStatus(c) {
			t.Errorf("retryableStatus(%d) = false, want true", c)
		}
	}
	for _, c := range notRetryable {
		if retryableStatus(c) {
			t.Errorf("retryableStatus(%d) = true, want false", c)
		}
	}
}
