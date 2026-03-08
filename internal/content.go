package crawler

import (
	"context"
)

type Content struct {
	URL   URL
	Title string
	Text  string
	URLs  []string
}

type ContentRepository interface {
	Save(ctx context.Context, content *Content) error
	Exists(ctx context.Context, url string) (bool, error)
}
