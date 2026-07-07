// Package web embeds the built dashboard assets so the server can serve the
// SPA from a single binary. The embed directive must live beside the dist
// directory (go:embed cannot traverse parent directories), so it is here at the
// web/ root rather than in cmd/server.
package web

import "embed"

// DistFS holds the built frontend (web/dist). A committed placeholder
// index.html keeps this compiling before the first `vite build`.
//
//go:embed all:dist
var DistFS embed.FS
