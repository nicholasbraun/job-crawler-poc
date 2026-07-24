package crawler

import "errors"

// ErrNotFound is returned by repositories when a requested entity does not
// exist. Callers use errors.Is to map it to a 404.
var ErrNotFound = errors.New("crawler: not found")
