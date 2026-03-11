package http_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"

	myHttp "github.com/nicholasbraun/job-crawler-poc/internal/http"
)

type mockDownloader struct {
	responses []*myHttp.Response
	errors    []error
	callCount int
}

func (md *mockDownloader) Get(ctx context.Context, url string) (*myHttp.Response, error) {
	i := md.callCount
	md.callCount++
	return md.responses[i], md.errors[i]
}

func TestGet200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("hello world"))
	}))
	defer server.Close()

	url := server.URL

	downloader := myHttp.NewClient()

	res, err := downloader.Get(t.Context(), url)
	if err != nil {
		t.Fatalf("server did return an error: %v", err)
	}

	wantCode := 200
	gotCode := res.StatusCode

	if gotCode != wantCode {
		t.Errorf("server returned wrong status code. want: %d, got: %d", wantCode, gotCode)
	}

	wantContent := "hello world"
	gotContent := string(res.Content)

	if gotContent != wantContent {
		t.Errorf("server returned wrong content. want: %v, got: %v", wantContent, gotContent)
	}
}

func TestRetry(t *testing.T) {
	t.Run("Retry on 500", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			mock := &mockDownloader{
				responses: []*myHttp.Response{
					{StatusCode: 500},
					{StatusCode: 500},
					{StatusCode: 200, Content: []byte("hello world")},
				},
				errors: []error{nil, nil, nil},
			}
			downloader := myHttp.NewRetryClient(mock)

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
			responses: []*myHttp.Response{
				{StatusCode: 404},
				{StatusCode: 200},
			},
			errors: []error{nil, nil},
		}
		downloader := myHttp.NewRetryClient(mock)

		res, err := downloader.Get(t.Context(), "http://something.de")
		if err != nil {
			t.Fatalf("server did return an error: %v", err)
		}

		wantCode := 404
		gotCode := res.StatusCode
		if gotCode != wantCode {
			t.Errorf("expected status code %d. got: %d", wantCode, gotCode)
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
				responses: []*myHttp.Response{
					nil,
					{StatusCode: 200},
				},
				errors: []error{errors.New("error"), nil},
			}
			downloader := myHttp.NewRetryClient(mock)

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
				responses: []*myHttp.Response{
					{StatusCode: 500},
					{StatusCode: 500},
					{StatusCode: 500},
					{StatusCode: 200},
				},
				errors: []error{nil, nil, nil, nil},
			}
			maxRetries := 3
			downloader := myHttp.NewRetryClient(mock, myHttp.WithMaxTries(maxRetries))

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
				responses: []*myHttp.Response{
					{StatusCode: 500},
					{StatusCode: 500},
					{StatusCode: 500},
					{StatusCode: 200},
				},
				errors: []error{nil, nil, nil, nil},
			}

			downloader := myHttp.NewRetryClient(
				mock,
				myHttp.WithMultiplicator(1),
				myHttp.WithBackoff(time.Second),
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
