package crawler

import "context"

type URL struct {
	Base  string
	Path  string
	Depth int
}

func (u URL) GetFullURL() string {
	// TODO: add logic to insert "/" between the two if it is missing
	return u.Base + u.Path
}

type URLRepository interface {
	Save(ctx context.Context, url string) error
	Visted(ctx context.Context, url string) (bool, error)
}
