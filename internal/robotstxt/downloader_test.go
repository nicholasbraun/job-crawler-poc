package robotstxt_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nicholasbraun/job-crawler-poc/internal/robotstxt"
)

func TestDownloaderGet200(t *testing.T) {
	body := []byte("User-agent: *\nAllow: /\n")
	var gotUserAgent string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUserAgent = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Write(body)
	}))
	defer server.Close()

	d := robotstxt.NewRobotsTxtDownloader("TestBot/1.0")

	res, err := d.Get(t.Context(), server.URL+"/robots.txt")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want %d", res.StatusCode, http.StatusOK)
	}
	if !bytes.Equal(res.Content, body) {
		t.Fatalf("body: got %q want %q", res.Content, body)
	}
	if gotUserAgent != "TestBot/1.0" {
		t.Fatalf("user-agent: got %q want %q", gotUserAgent, "TestBot/1.0")
	}
}

func TestDownloaderPropagatesStatusCode(t *testing.T) {
	tests := []struct {
		name   string
		status int
	}{
		{"404 not found", http.StatusNotFound},
		{"410 gone", http.StatusGone},
		{"500 internal", http.StatusInternalServerError},
		{"503 unavailable", http.StatusServiceUnavailable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer server.Close()

			d := robotstxt.NewRobotsTxtDownloader("TestBot/1.0")
			res, err := d.Get(t.Context(), server.URL)
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if res.StatusCode != tt.status {
				t.Fatalf("status: got %d want %d", res.StatusCode, tt.status)
			}
		})
	}
}

func TestDownloaderCapsBodyAt1MB(t *testing.T) {
	const bodySize = 2 * 1000 * 1000 // 2 MB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write(bytes.Repeat([]byte("a"), bodySize))
	}))
	defer server.Close()

	d := robotstxt.NewRobotsTxtDownloader("TestBot/1.0")
	res, err := d.Get(t.Context(), server.URL)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(res.Content) != 1_000_000 {
		t.Fatalf("body length: got %d want %d", len(res.Content), 1_000_000)
	}
}

func TestDownloaderCancelledCtxErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	d := robotstxt.NewRobotsTxtDownloader("TestBot/1.0")
	_, err := d.Get(ctx, server.URL)
	if err == nil {
		t.Fatalf("expected error from cancelled ctx, got nil")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("expected context canceled in err, got %v", err)
	}
}
