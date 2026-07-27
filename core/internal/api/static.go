package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

// Serving the built frontend.
//
// Optional: with CHIRAL_WEB_DIR unset the panel serves no files at all, which
// is the right thing during development (vite has its own server) and for
// anyone terminating at nginx or Caddy.
//
// Both interfaces use hash routing, so there are only two real paths — / for
// the portal and /admin/ for the console — and no SPA fallback is needed.

// WebDirFromEnv reports the directory of built assets, or "" for none.
func WebDirFromEnv() string { return strings.TrimSpace(os.Getenv("CHIRAL_WEB_DIR")) }

// routeStatic registers the file server, if there is one.
//
// Two details matter more than the serving itself:
//
//   - The catch-all "/" pattern does not shadow /api/, /sub/ or /healthz: Go
//     1.22's ServeMux picks the most specific pattern, not the first
//     registered. But a MISTYPED api path — /api/nodez — matches only "/" and
//     would otherwise return the HTML index with a 200, which is a confusing
//     thing to hand a fetch() that expected JSON. Hence the explicit /api/
//     fallback below.
//
//   - A directory with no index.html must 404. http.FileServer's default is to
//     render a listing, and dist/assets is a hundred-odd files including the
//     4 MB Monaco chunk — a browsable index of the whole build for anyone who
//     guesses the path.
func (s *Server) routeStatic(mux *http.ServeMux, dir string) {
	if dir == "" {
		return
	}
	root := os.DirFS(dir)
	files := http.FileServerFS(noListing{root})

	mux.Handle("/", files)
	// Anything under /api/ that reached here is a path no handler claimed.
	// Answer in the shape the caller expects rather than with the index page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint")
	})
	s.logger.Info("serving the web frontend", "dir", dir)
}

// noListing is an fs.FS that hides directories with no index.html, so
// http.FileServer answers 404 instead of rendering a listing of them.
type noListing struct{ fs.FS }

func (n noListing) Open(name string) (fs.File, error) {
	f, err := n.FS.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.IsDir() {
		return f, nil
	}
	// A directory is only openable if it has something to serve. FileServer
	// asks for index.html itself once this returns, so refusing here turns the
	// listing into a plain 404.
	if _, err := fs.Stat(n.FS, path.Join(name, "index.html")); err != nil {
		f.Close()
		return nil, fs.ErrNotExist
	}
	return f, nil
}
