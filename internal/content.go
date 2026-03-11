package crawler

import (
	"context"
)

type Content struct {
	Title       string
	MainContent string
	URLs        []string
}

type ContentRepository interface {
	Save(ctx context.Context, url string, content *Content) error
	Exists(ctx context.Context, url string) (bool, error)
}
