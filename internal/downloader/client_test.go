package downloader_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nicholasbraun/job-crawler-poc/internal/downloader"
)

type mockDownloader struct {
	responses []*downloader.Response
	errors    []error
	callCount int
}

func (md *mockDownloader) Get(ctx context.Context, url string) (*downloader.Response, error) {
	i := md.callCount
	md.callCount++
	return md.responses[i], md.errors[i]
}

func TestGet200(t *testing.T) {
	content := "<html>hello world</html>"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(content))
	}))
	defer server.Close()

	url := server.URL

	client := downloader.NewClient("userAgent")

	res, err := client.Get(t.Context(), url)
	if err != nil {
		t.Fatalf("server did return an error: %v", err)
	}

	wantCode := 200
	gotCode := res.StatusCode

	if gotCode != wantCode {
		t.Errorf("server returned wrong status code. want: %d, got: %d", wantCode, gotCode)
	}

	gotContent := string(res.Content)

	if gotContent != content {
		t.Errorf("server returned wrong content. want: %v, got: %v", content, gotContent)
	}
}

func TestResponseType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add(
			"content-type", "image/jpg")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	url := server.URL

	client := downloader.NewClient("userAgent")

	_, err := client.Get(t.Context(), url)

	if !errors.Is(err, downloader.ErrNoHTML) {
		t.Errorf("expected err: %v, got: %v", downloader.ErrNoHTML, err)
	}
}

func TestGetNon2xx(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		contentType   string
		wantRetryable bool
	}{
		{"404 is permanent", http.StatusNotFound, "text/html", false},
		{"403 is permanent", http.StatusForbidden, "text/html", false},
		{"429 throttle is retryable", http.StatusTooManyRequests, "text/html", true},
		{"503 is retryable", http.StatusServiceUnavailable, "text/html", true},
		// A throttle is often served as text/plain; it must be classified by
		// status (retryable), not rejected as non-HTML.
		{"429 as text/plain is retryable, not ErrNoHTML", http.StatusTooManyRequests, "text/plain", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", tt.contentType)
				w.WriteHeader(tt.status)
				w.Write([]byte("error page body"))
			}))
			defer server.Close()

			res, err := downloader.NewClient("userAgent").Get(t.Context(), server.URL)

			if errors.Is(err, downloader.ErrNoHTML) {
				t.Fatalf("status %d wrongly classified as ErrNoHTML", tt.status)
			}
			var statusErr *downloader.StatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("expected a StatusError, got: %v", err)
			}
			if res != nil {
				t.Errorf("expected nil response on a non-2xx status, got: %v", res)
			}
			if statusErr.StatusCode != tt.status {
				t.Errorf("StatusCode = %d, want %d", statusErr.StatusCode, tt.status)
			}
			if statusErr.Retryable != tt.wantRetryable {
				t.Errorf("Retryable = %v, want %v", statusErr.Retryable, tt.wantRetryable)
			}
		})
	}
}

func TestGetRetryAfter(t *testing.T) {
	t.Run("delta-seconds header is parsed onto the StatusError", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		_, err := downloader.NewClient("userAgent").Get(t.Context(), server.URL)

		var statusErr *downloader.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("expected a StatusError, got: %v", err)
		}
		if statusErr.RetryAfter != 30*time.Second {
			t.Errorf("RetryAfter = %v, want 30s", statusErr.RetryAfter)
		}
	})

	t.Run("HTTP-date header is parsed as a future delay", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		_, err := downloader.NewClient("userAgent").Get(t.Context(), server.URL)

		var statusErr *downloader.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("expected a StatusError, got: %v", err)
		}
		// HTTP-date has second granularity; the delay is ~1h minus request time.
		if statusErr.RetryAfter <= 0 || statusErr.RetryAfter > time.Hour {
			t.Errorf("RetryAfter = %v, want (0, 1h]", statusErr.RetryAfter)
		}
	})

	t.Run("absent header leaves RetryAfter zero", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()

		_, err := downloader.NewClient("userAgent").Get(t.Context(), server.URL)

		var statusErr *downloader.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("expected a StatusError, got: %v", err)
		}
		if statusErr.RetryAfter != 0 {
			t.Errorf("RetryAfter = %v, want 0", statusErr.RetryAfter)
		}
	})

	t.Run("malformed header is ignored", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "soon")
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		_, err := downloader.NewClient("userAgent").Get(t.Context(), server.URL)

		var statusErr *downloader.StatusError
		if !errors.As(err, &statusErr) {
			t.Fatalf("expected a StatusError, got: %v", err)
		}
		if statusErr.RetryAfter != 0 {
			t.Errorf("RetryAfter = %v, want 0", statusErr.RetryAfter)
		}
	})
}

func TestRetry(t *testing.T) {
	t.Run("Retry on 500", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{
					nil,
					nil,
					{StatusCode: 200, Content: []byte("hello world")},
				},
				errors: []error{
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					nil,
				},
			}
			downloader := downloader.NewRetryClient(mock)

			res, err := downloader.Get(t.Context(), "http://something.de")
			if err != nil {
				t.Fatalf("server did return an error: %v", err)
			}

			wantCode := 200
			gotCode := res.StatusCode
			if gotCode != wantCode {
				t.Errorf("expected status code %d. got: %d", wantCode, gotCode)
			}

			wantAttempts := 3
			gotAttempts := mock.callCount
			if gotAttempts != wantAttempts {
				t.Errorf("expected %d attemps. got: %d", wantAttempts, gotAttempts)
			}
		})
	})

	t.Run("Don't retry on 404", func(t *testing.T) {
		mock := &mockDownloader{
			responses: []*downloader.Response{nil},
			errors:    []error{&downloader.StatusError{StatusCode: 404, Retryable: false}},
		}
		var statusErr *downloader.StatusError
		retryClient := downloader.NewRetryClient(mock)

		res, err := retryClient.Get(t.Context(), "http://something.de")

		if !errors.As(err, &statusErr) {
			t.Fatalf("expected a StatusError, got: %v", err)
		}
		if statusErr.StatusCode != 404 {
			t.Errorf("expected status code 404, got: %d", statusErr.StatusCode)
		}
		if res != nil {
			t.Errorf("expected res to be nil, got: %v", res)
		}

		wantAttempts := 1
		gotAttempts := mock.callCount
		if gotAttempts != wantAttempts {
			t.Errorf("expected %d attemps. got: %d", wantAttempts, gotAttempts)
		}
	})

	t.Run("Don't retry on NXDOMAIN", func(t *testing.T) {
		dnsErr := &net.DNSError{Err: "no such host", Name: "nxdomain.invalid", IsNotFound: true}
		mock := &mockDownloader{
			responses: []*downloader.Response{nil},
			// Wrapped as the base Client wraps a download error, to prove the
			// verdict survives unwrapping.
			errors: []error{fmt.Errorf("error downloading url (%s). %w", "http://nxdomain.invalid", dnsErr)},
		}
		retryClient := downloader.NewRetryClient(mock)

		res, err := retryClient.Get(t.Context(), "http://nxdomain.invalid")

		var gotDNS *net.DNSError
		if !errors.As(err, &gotDNS) || !gotDNS.IsNotFound {
			t.Fatalf("expected a not-found DNS error, got: %v", err)
		}
		if res != nil {
			t.Errorf("expected res to be nil, got: %v", res)
		}
		if mock.callCount != 1 {
			t.Errorf("expected 1 attempt (NXDOMAIN is permanent), got: %d", mock.callCount)
		}
	})

	t.Run("Retry on a transient DNS timeout", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{nil, {StatusCode: 200}},
				errors: []error{
					&net.DNSError{Err: "i/o timeout", Name: "slow.example", IsTimeout: true},
					nil,
				},
			}
			retryClient := downloader.NewRetryClient(mock)

			res, err := retryClient.Get(t.Context(), "http://slow.example")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.StatusCode != 200 {
				t.Errorf("expected status 200, got: %d", res.StatusCode)
			}
			if mock.callCount != 2 {
				t.Errorf("expected 2 attempts (a DNS timeout is transient), got: %d", mock.callCount)
			}
		})
	})

	t.Run("Retry on error", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{
					nil,
					{StatusCode: 200},
				},
				errors: []error{errors.New("error"), nil},
			}
			downloader := downloader.NewRetryClient(mock)

			res, err := downloader.Get(t.Context(), "http://something.de")
			if err != nil {
				t.Fatalf("server did return an error: %v", err)
			}

			wantCode := 200
			gotCode := res.StatusCode
			if gotCode != wantCode {
				t.Errorf("expected status code %d. got: %d", wantCode, gotCode)
			}

			wantAttempts := 2
			gotAttempts := mock.callCount
			if gotAttempts != wantAttempts {
				t.Errorf("expected %d attemps. got: %d", wantAttempts, gotAttempts)
			}
		})
	})

	t.Run("Max retries exhausted", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{nil, nil, nil, {StatusCode: 200}},
				errors: []error{
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					nil,
				},
			}
			maxRetries := 3
			downloader := downloader.NewRetryClient(mock, downloader.WithMaxTries(maxRetries))

			res, err := downloader.Get(t.Context(), "http://something.de")
			if err == nil {
				t.Error("expected an error. got nil")
			}

			if res != nil {
				t.Errorf("expected res to be nil. got %v", res)
			}

			wantAttempts := maxRetries
			gotAttempts := mock.callCount
			if gotAttempts != wantAttempts {
				t.Errorf("expected %d attemps. got: %d", wantAttempts, gotAttempts)
			}
		})
	})

	t.Run("Honors Retry-After hint over backoff", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{nil, {StatusCode: 200}},
				errors: []error{
					&downloader.StatusError{StatusCode: 429, Retryable: true, RetryAfter: 30 * time.Second},
					nil,
				},
			}
			// Default backoff is 2s; the 30s hint must win.
			retryClient := downloader.NewRetryClient(mock)

			start := time.Now()
			_, err := retryClient.Get(t.Context(), "http://something.de")
			if err != nil {
				t.Fatalf("server did return an error: %v", err)
			}

			if elapsed := time.Since(start); elapsed != 30*time.Second {
				t.Errorf("waited %v, want 30s from Retry-After hint", elapsed)
			}
		})
	})

	t.Run("Caps an over-long Retry-After hint at maxBackoff", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{nil, {StatusCode: 200}},
				errors: []error{
					&downloader.StatusError{StatusCode: 503, Retryable: true, RetryAfter: 24 * time.Hour},
					nil,
				},
			}
			retryClient := downloader.NewRetryClient(mock, downloader.WithMaxBackoff(30*time.Second))

			start := time.Now()
			_, err := retryClient.Get(t.Context(), "http://something.de")
			if err != nil {
				t.Fatalf("server did return an error: %v", err)
			}

			if elapsed := time.Since(start); elapsed != 30*time.Second {
				t.Errorf("waited %v, want the 30s ceiling, not the 24h hint", elapsed)
			}
		})
	})

	t.Run("Context cancels retries", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*downloader.Response{nil, nil, nil, {StatusCode: 200}},
				errors: []error{
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					&downloader.StatusError{StatusCode: 500, Retryable: true},
					nil,
				},
			}

			downloader := downloader.NewRetryClient(
				mock,
				downloader.WithMultiplicator(1),
				downloader.WithBackoff(time.Second),
			)

			// 2.5s timeout should cancel after 3 retries
			// 1. attempt at 0s
			// 2. attempt at 1s
			// 3. attempt at 2s
			ctxTimeout, cancel := context.WithTimeout(t.Context(), 2500*time.Millisecond)
			defer cancel()
			res, err := downloader.Get(ctxTimeout, "http://something.de")
			if err == nil {
				t.Error("expected an error. got nil")
			}

			if res != nil {
				t.Errorf("expected res to be nil. got %v", res)
			}

			wantAttempts := 3
			gotAttempts := mock.callCount
			if gotAttempts != wantAttempts {
				t.Errorf("expected %d attemps. got: %d", wantAttempts, gotAttempts)
			}
		})
	})
}
