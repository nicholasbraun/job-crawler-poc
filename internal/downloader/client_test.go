package downloader_test

import (
	"context"
	"errors"
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
