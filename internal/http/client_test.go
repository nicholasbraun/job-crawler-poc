package http_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

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
	synctest.Test(t, func(t *testing.T) {
		mock := &mockDownloader{
			responses: []*myHttp.Response{
				{
					StatusCode: 500,
				},
				{
					StatusCode: 500,
				},
				{StatusCode: 200, Content: []byte("hello world")},
			},
			errors: []error{nil, nil, nil},
		}
		downloader := myHttp.NewRetryClient(mock)

		res, err := downloader.Get(t.Context(), "http://something.de")
		synctest.Wait()

		if err != nil {
			t.Fatalf("server did return an error: %v", err)
		}

		if res.StatusCode != 200 {
			t.Errorf("expected status code 200 on first try. got: %d", res.StatusCode)
		}

		if mock.callCount != 3 {
			t.Errorf("expected three attemps. got: %d", mock.callCount)
		}
	})
}
