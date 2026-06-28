// Package web bundles the static Floor view (HTML/CSS/JS) into the binary via go:embed and
// serves it CWD-independently. Keeping the assets embedded means the server can be launched
// from any directory and still serve the UI from a single binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var staticFiles embed.FS

// Static returns the embedded static/ tree as a file system.
func Static() fs.FS {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		// staticFiles is embedded at build time; this can only fail on a programming error.
		panic(err)
	}
	return sub
}

// Handler serves the embedded Floor view (index.html, styles, app.js, assets) over HTTP.
func Handler() http.Handler {
	return http.FileServerFS(Static())
}
