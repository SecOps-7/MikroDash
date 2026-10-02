package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/store"
)

func importServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	dir := t.TempDir()
	routers := `[{"id":"r1","label":"Office","host":"198.51.100.1","port":8729,"username":"u","password":""}]`
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(routers), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	s := &Server{auditDB: d, auth: authFor("tok", sess), hub: hub.New(), store: st}
	mux := http.NewServeMux()
	s.registerRouterImport(mux, func(h http.HandlerFunc) http.HandlerFunc { return h })
	return s, mux
}

func importCall(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Cookie", authed)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// waitImport polls the status route until the job stops.
func waitImport(t *testing.T, mux *http.ServeMux) importJob {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		var got struct{ Job *importJob }
		_ = json.Unmarshal(importCall(mux, "GET", "/api/routers/bulk", "").Body.Bytes(), &got)
		if got.Job != nil && !got.Job.Running {
			return *got.Job
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the import never finished")
	return importJob{}
}

func TestOnlyAnAdministratorMayImport(t *testing.T) {
	_, mux := importServer(t, &Session{AuthMode: "password", Username: "ops"})
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/routers/bulk/check"}, {"POST", "/api/routers/bulk"}, {"GET", "/api/routers/bulk"},
	} {
		if w := importCall(mux, c.method, c.path, `{"rows":[{"host":"192.0.2.9","username":"a"}]}`); w.Code != 403 {
			t.Errorf("%s %s: %d for a non-administrator", c.method, c.path, w.Code)
		}
	}
}

func TestThePreviewWritesNothingAndEchoesNoPassword(t *testing.T) {
	s, mux := importServer(t, &Session{AuthMode: "none"})
	w := importCall(mux, "POST", "/api/routers/bulk/check",
		`{"rows":[{"line":2,"host":"192.0.2.9","username":"admin","password":"s3cret-in-a-sheet","sites":"North"}]}`)
	if w.Code != 200 || strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if all, _ := s.store.Routers(); len(all) != 1 {
		t.Errorf("the preview added a device: %d", len(all))
	}
	if list, _ := s.auditDB.ListSites(); len(list) != 0 {
		t.Errorf("the preview created a site: %v", list)
	}
}

func TestAnImportAddsReadyRowsAndCreatesTheirSites(t *testing.T) {
	s, mux := importServer(t, &Session{AuthMode: "none"})
	w := importCall(mux, "POST", "/api/routers/bulk", `{"rows":[
		{"line":2,"host":"192.0.2.9","name":"Branch","username":"admin","password":"pw","sites":"North|Depot"},
		{"line":3,"host":"198.51.100.1","username":"admin","password":"pw"},
		{"line":4,"host":"bad host!","username":"admin"}]}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	job := waitImport(t, mux)
	if len(job.Rows) != 1 || job.Rows[0].State != "added" || job.Rows[0].Line != 2 {
		t.Fatalf("only the ready row is imported: %+v", job)
	}
	all, _ := s.store.Routers()
	if len(all) != 2 {
		t.Fatalf("fleet %d", len(all))
	}
	nw := all[1]
	if nw.Label != "Branch" || nw.Password != "pw" || len(nw.SiteIDs) != 2 {
		t.Errorf("the device: %+v", nw)
	}
	list, _ := s.auditDB.ListSites()
	if len(list) != 2 {
		t.Errorf("both sites created once: %v", list)
	}

	// The same file again: everything is a duplicate now.
	if w := importCall(mux, "POST", "/api/routers/bulk",
		`{"rows":[{"line":2,"host":"192.0.2.9","username":"admin","password":"pw"}]}`); w.Code != 400 {
		t.Errorf("a re-upload with nothing ready: %d %s", w.Code, w.Body.String())
	}
}

// A profile row with no password adds NOTHING unless MikroDash can sign in with
// the profile first. Port 1 on loopback refuses at once.
func TestAProfileRowThatCannotSignInIsNotAdded(t *testing.T) {
	s, mux := importServer(t, &Session{AuthMode: "none"})
	if _, err := s.store.AddLoginProfile("MikroDash login", "a-long-enough-password"); err != nil {
		t.Fatal(err)
	}
	w := importCall(mux, "POST", "/api/routers/bulk",
		`{"rows":[{"line":2,"host":"127.0.0.1","port":"1","tls":"no","credentialProfile":"MikroDash login"}]}`)
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	job := waitImport(t, mux)
	if job.Rows[0].State != "failed" || !strings.Contains(job.Rows[0].Message, "not added") {
		t.Errorf("%+v", job.Rows[0])
	}
	if all, _ := s.store.Routers(); len(all) != 1 {
		t.Errorf("a device that could not sign in was added: %d", len(all))
	}
}
