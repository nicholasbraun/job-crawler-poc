package downloader

import (
	"context"
)

// Downloader fetches the content of a URL over HTTP. Implementations may
// add retry logic, rate limiting, or other resilience behavior.
type Downloader interface {
	Get(ctx context.Context, url string) (resp *Response, err error)
}
