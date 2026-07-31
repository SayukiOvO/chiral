package api

import (
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/SayukiOvO/chiral/web"
)

// Serving the built frontend.
//
// Normally the assets are compiled into the binary (see package web), so a
// single Core executable is the whole panel. CHIRAL_WEB_DIR overrides that with
// a directory on disk, which is what development wants (vite serves its own,
// with hot reload) and what anyone terminating static content at nginx or Caddy
// wants. With neither, the panel serves no files at all.
//
// Both interfaces use hash routing, so there are only two real paths — / for
// the portal and /admin/ for the console — and no SPA fallback is needed.

// WebDirFromEnv reports the directory of built assets, or "" for none.
func WebDirFromEnv() string { return strings.TrimSpace(os.Getenv("CHIRAL_WEB_DIR")) }

// routeStatic registers the file server, if there is one.
//
// Takes an fs.FS rather than a path so the same handler serves the embedded
// assets and an on-disk directory; everything below cares about the shape of
// the tree, not where it came from.
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
func (s *Server) routeStatic(mux *http.ServeMux, root fs.FS, source string) {
	if root == nil {
		return
	}
	files := http.FileServerFS(noListing{root})

	mux.Handle("/", files)
	// Anything under /api/ that reached here is a path no handler claimed.
	// Answer in the shape the caller expects rather than with the index page.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint")
	})
	s.logger.Info("serving the web frontend", "from", source)
}

// WebAssets picks what to serve: CHIRAL_WEB_DIR if set and present, otherwise
// whatever was compiled in. Returns nil for the assets when there is neither,
// and a non-empty third value when CHIRAL_WEB_DIR was set and ignored.
//
// The directory is checked because os.DirFS does not check: it accepts any
// path and fails per-request, so a CHIRAL_WEB_DIR that does not exist produced
// a panel that logged the directory as its source and then answered 404 for
// the console, the portal and every asset — with the frontend sitting compiled
// into the very binary that was refusing to serve it. That configuration
// shipped in the panel's own compose file, where it pointed at a path the
// image does not have.
func WebAssets() (fs.FS, string, string) {
	dir := WebDirFromEnv()
	if dir != "" {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return os.DirFS(dir), dir, ""
		}
		if assets, ok := web.Assets(); ok {
			return assets, "embedded",
				"CHIRAL_WEB_DIR=" + dir + " is not a directory; serving the compiled-in frontend instead"
		}
		return nil, "", "CHIRAL_WEB_DIR=" + dir + " is not a directory and nothing is compiled in; no frontend will be served"
	}
	if assets, ok := web.Assets(); ok {
		return assets, "embedded", ""
	}
	return nil, "", ""
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
