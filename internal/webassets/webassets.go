// Package webassets contains the production frontend embedded in the server
// binary. Deployments may replace the contents of dist/ during the frontend
// build, but dist/index.html is deliberately checked in as a small fallback so
// a clean backend checkout always produces a useful binary.
package webassets

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// Dist is rooted at the frontend dist directory, so callers can open
// "index.html" and "assets/..." without knowing the embed layout.
var Dist fs.FS = mustSub(embedded, "dist")

func mustSub(files embed.FS, dir string) fs.FS {
	root, err := fs.Sub(files, dir)
	if err != nil {
		panic(err)
	}
	return root
}
