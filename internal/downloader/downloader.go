package downloader

import (
	"context"
)

type Downloader interface {
	Get(ctx context.Context, url string) (resp *Response, err error)
}
