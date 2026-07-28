package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func staticFixture(t *testing.T) (http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<!doctype html>portal")
	write("admin/index.html", "<!doctype html>console")
	// No index.html here, exactly like a real dist/assets.
	write("assets/index-abc123.js", "console.log(1)")
	write("assets/MonacoEditor-xyz.js", "// 4MB in production")

	srv := &Server{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	mux := http.NewServeMux()
	mux.Handle("GET /api/nodes", srv.requireAdmin(func(w http.ResponseWriter, r *http.Request) {}))
	srv.routeStatic(mux, os.DirFS(dir), dir)
	return mux, dir
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestStaticServesBothEntryPoints(t *testing.T) {
	h, _ := staticFixture(t)

	if w := get(t, h, "/"); !strings.Contains(w.Body.String(), "portal") {
		t.Errorf("/ served %q, want the portal index", w.Body)
	}
	if w := get(t, h, "/admin/"); !strings.Contains(w.Body.String(), "console") {
		t.Errorf("/admin/ served %q, want the console index", w.Body)
	}
	if w := get(t, h, "/assets/index-abc123.js"); w.Code != http.StatusOK {
		t.Errorf("an asset returned %d, want 200", w.Code)
	}
}

// The whole reason for the noListing wrapper. Without it, http.FileServer
// renders a browsable index of dist/assets to anyone who guesses the path.
func TestStaticRefusesToListADirectory(t *testing.T) {
	h, _ := staticFixture(t)

	w := get(t, h, "/assets/")
	if w.Code != http.StatusNotFound {
		t.Fatalf("/assets/ returned %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "MonacoEditor") {
		t.Fatalf("/assets/ listed its contents:\n%s", w.Body)
	}
}

// A mistyped API path matches only the catch-all. Serving it the HTML index
// with a 200 would hand a JSON parse error to a fetch() that deserved a 404.
func TestUnknownAPIPathsAnswerAsJSON(t *testing.T) {
	h, _ := staticFixture(t)

	w := get(t, h, "/api/nodez")
	if w.Code != http.StatusNotFound {
		t.Errorf("status %d for an unknown api path, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "<!doctype") {
		t.Errorf("an unknown api path was served the index page:\n%s", w.Body)
	}
	if !strings.Contains(w.Body.String(), `"error"`) {
		t.Errorf("body is not a JSON error: %s", w.Body)
	}
}

// The catch-all must not shadow a real route. Go 1.22 picks the most specific
// pattern, but this is worth pinning because the whole scheme depends on it.
func TestStaticDoesNotShadowRealRoutes(t *testing.T) {
	h, _ := staticFixture(t)

	w := get(t, h, "/api/nodes")
	// 401 from the auth middleware, i.e. the route was reached. An index page
	// or a JSON 404 would both mean the static handler swallowed it.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d for a real api route, want 401 from its guard: %s", w.Code, w.Body)
	}
}

// With no directory configured the panel serves nothing, so a deployment that
// terminates static files elsewhere is unaffected.
func TestNoWebDirServesNothing(t *testing.T) {
	srv := &Server{logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))}
	mux := http.NewServeMux()
	srv.routeStatic(mux, nil, "")

	if w := get(t, mux, "/"); w.Code != http.StatusNotFound {
		t.Errorf("status %d with no web dir, want 404", w.Code)
	}
}

// Path traversal, in case the fs.FS wrapper ever gets replaced by something
// hand-rolled.
func TestStaticRefusesTraversal(t *testing.T) {
	h, dir := staticFixture(t)
	if err := os.WriteFile(filepath.Join(filepath.Dir(dir), "secret.txt"), []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, p := range []string{"/../secret.txt", "/assets/../../secret.txt", "/%2e%2e/secret.txt"} {
		w := get(t, h, p)
		if strings.Contains(w.Body.String(), "nope") {
			t.Errorf("%s escaped the web dir: %s", p, w.Body)
		}
	}
}
