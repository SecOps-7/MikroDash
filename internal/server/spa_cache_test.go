package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// THE SHELL AND ITS BUNDLE ARE REVALIDATED ON EVERY LOAD.
//
// `app.js` and `app.css` keep their names across builds and the file server
// sends only Last-Modified, which lets a browser go on running the copy it has
// after a rebuild: a fix is deployed and the operator still sees the bug. With
// `no-cache` the browser keeps its copy but asks first, so a reload is a 304
// rather than a download, and never stale.
func TestTheAppShellAndBundleAreRevalidated(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"index.html", "app.js", "app.css"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{web: http.FileServer(http.Dir(dir))}

	for _, p := range []string{"/", "/home", "/app.js", "/app.css"} {
		rec := httptest.NewRecorder()
		srv.spa().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d, want 200", p, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache, or a browser keeps the old "+
				"bundle after a rebuild", p, got)
		}
	}
}
