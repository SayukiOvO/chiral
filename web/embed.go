// Package web carries the built frontend into the Core binary.
//
// It lives here rather than under core/ because go:embed cannot reach outside
// its own directory, and dist/ is produced here by Vite.
//
// The embed is unconditional and degrades instead of failing. A checkout that
// has never run `npm run build` still compiles — dist/ holds a committed
// placeholder so the pattern always matches — and Assets reports that there is
// nothing to serve. That keeps `go build ./...` working for anyone touching
// only Go, and keeps CI's Go job independent of its Node job, at the cost of a
// binary that silently has no UI if somebody forgets the frontend build. The
// release path builds it; see .github/workflows/publish.yml.
package web

import (
	"embed"
	"io/fs"
)

// all: so the placeholder — a dotfile — matches too. Without it a checkout with
// no build fails to compile rather than producing a UI-less binary.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the built frontend and whether there is one.
//
// The second return is the whole point: an empty dist embeds fine, so the
// caller has to be told the difference between "here is the UI" and "this
// binary was built without one" rather than serving an empty file system and
// answering 404 to every page.
func Assets() (fs.FS, bool) {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil, false
	}
	// index.html is the portal's entry point. Its absence means the build
	// output was never copied in, whatever else the directory contains.
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil, false
	}
	return sub, true
}
