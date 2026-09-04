package server

// The two download links on the Backups page, through the REAL mux.
//
// Both routes were MISSING for the whole life of the Go port: the page drew
// `<a href="/api/backups/<id>/rsc?routerId=…">` on every stored row, only
// `/raw` was registered, and the links answered Go's own "404 page not found".
// Issue #124.
//
// These tests exist as much for the ROUTE REGISTRATION as for the handler: they
// go through `http.ServeMux`, so a pattern that stops matching — a renamed
// wildcard, a dropped method, a moved prefix — fails here rather than at a
// user's browser.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/backups"
	"mikrodash/internal/db"
)

// dlServer builds a server with one stored pair on r1, and returns the mux, the
// row id and the directory holding the files.
func dlServer(t *testing.T) (*Server, *http.ServeMux, int64) {
	return dlServerAs(t, &Session{AuthMode: "none", Username: "admin"})
}

func dlServerAs(t *testing.T, sess *Session) (*Server, *http.ServeMux, int64) {
	t.Helper()
	s, mux, _ := routersServer(t, sess, "")
	// `routersServer` creates the database file with the ALERT fixture's DDL, and
	// `db.Open` builds the full schema only for a file that does not exist yet —
	// so `config_backups` is absent here and has to be made. Copied from
	// `internal/db/schema_ddl.go`; the columns this route reads are the ones that
	// matter, and `GetBackup` selects them by name.
	if err := execOn(t, routerDBDir[s], `
CREATE TABLE config_backups (
  id INTEGER PRIMARY KEY AUTOINCREMENT, router_id TEXT NOT NULL,
  taken_at INTEGER NOT NULL, outcome TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT 'schedule', actor TEXT, stem TEXT, dir TEXT,
  fingerprint TEXT, rsc_bytes INTEGER NOT NULL DEFAULT 0,
  backup_bytes INTEGER NOT NULL DEFAULT 0, model TEXT, serial TEXT,
  os_version TEXT, ms INTEGER NOT NULL DEFAULT 0, pruned_at INTEGER, error TEXT
);`); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if _, _, err := backups.WritePair(dir, "2026-09-04-0800",
		"/interface print\n# the export\n", []byte{0x88, 0x99, 0xAA}); err != nil {
		t.Fatal(err)
	}
	stem, d := "2026-09-04-0800", dir
	id, err := s.auditDB.RecordBackup(db.BackupRun{
		RouterID: "r1", TakenAt: time.Now().UnixMilli(), Outcome: "ok",
		Source: "manual", Stem: &stem, Dir: &d, RscBytes: 30, BackupBytes: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	s.registerBackupDownloads(mux)
	return s, mux, id
}

func dlGet(mux *http.ServeMux, id int64, part, routerID string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET",
		"/api/backups/"+strconv.FormatInt(id, 10)+"/"+part+"?routerId="+routerID, nil)
	r.Header.Set("Cookie", authed)
	r.RemoteAddr = "10.0.0.9:1234"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestTheDownloadRoutesAreRegistered is the regression itself. Before the fix
// both of these were 404 "page not found" from the static catch-all.
func TestTheDownloadRoutesAreRegistered(t *testing.T) {
	_, mux, id := dlServer(t)
	for _, part := range []string{"rsc", "backup"} {
		w := dlGet(mux, id, part, "r1")
		if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), "page not found") {
			t.Fatalf("/%s is not registered on the mux at all — this is the bug", part)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("/%s answered %d: %s", part, w.Code, w.Body.String())
		}
	}
}

func TestTheRscComesBackGunzippedAsAnAttachment(t *testing.T) {
	_, mux, id := dlServer(t)
	w := dlGet(mux, id, "rsc", "r1")
	if got := w.Body.String(); !strings.Contains(got, "/interface print") {
		t.Errorf("body is not the export: %q", got)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.HasSuffix(cd, `.rsc"`) {
		t.Errorf("Content-Disposition = %q; without an attachment name the "+
			"browser renders the export as a page", cd)
	}
	// NAMED AFTER THE DEVICE. Several downloads called by their row id are
	// indistinguishable once they are in a folder together.
	if !strings.Contains(cd, "one") {
		t.Errorf("the filename does not carry the router's label: %q", cd)
	}
}

func TestTheBackupComesBackAsBytes(t *testing.T) {
	_, mux, id := dlServer(t)
	w := dlGet(mux, id, "backup", "r1")
	if got := w.Body.Bytes(); len(got) != 3 || got[0] != 0x88 {
		t.Errorf("body = % x, want the three stored bytes", got)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

// TestABackupBelongingToAnotherRouterIs404 — the permission is asked about the
// router in the QUERY and the file comes from the row named by the PATH. If
// those are not compared, a principal with write on one router walks ids and
// collects the fleet.
//
// 404, not 403: a distinct status confirms the id exists to whoever is probing.
func TestABackupBelongingToAnotherRouterIs404(t *testing.T) {
	_, mux, id := dlServer(t)
	w := dlGet(mux, id, "rsc", "r2")
	if w.Code != http.StatusNotFound {
		t.Errorf("a row belonging to r1 fetched as r2 answered %d, want 404", w.Code)
	}
	if strings.Contains(w.Body.String(), "/interface print") {
		t.Error("IT SERVED THE OTHER ROUTER'S EXPORT")
	}
}

func TestAnAnonymousCallerGetsNoBackup(t *testing.T) {
	_, mux, id := dlServer(t)
	r := httptest.NewRequest("GET",
		"/api/backups/"+strconv.FormatInt(id, 10)+"/backup?routerId=r1", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("answered %d without a cookie, want 401", w.Code)
	}
}

// TestAPrunedPairIs404 — the row survives retention so the History table can
// explain the disappearance, but the files are gone.
func TestAPrunedPairIs404(t *testing.T) {
	s, mux, id := dlServer(t)
	if _, err := s.auditDB.MarkBackupPruned(id, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if w := dlGet(mux, id, "rsc", "r1"); w.Code != http.StatusNotFound {
		t.Errorf("a pruned pair answered %d, want 404", w.Code)
	}
}

// TestAMissingFileIsNotA200 — the row says stored and the file is not there.
// Distinct from pruning, which is excluded above, so this is a real fault.
func TestAMissingFileIsNotA200(t *testing.T) {
	s, mux, id := dlServer(t)
	row, err := s.auditDB.GetBackup(id)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(*row.Dir, *row.Stem+".rsc.gz")); err != nil {
		t.Fatal(err)
	}
	w := dlGet(mux, id, "rsc", "r1")
	if w.Code == http.StatusOK {
		t.Errorf("served %q for a file that is not on disk", w.Body.String())
	}
}

// ── DOWNLOAD IS A WRITE-LEVEL QUESTION, AND THAT IS THE WHOLE POINT ─────────
//
// `internal/server/backups.go`'s header states it: an export describes the whole
// network and the binary carries every key on the device, so `permitted` on the
// page payload is `backups` at WRITE. The page only draws these links when that
// flag is true.
//
// The cases above all run with `AuthMode: "none"`, which short-circuits every
// permission check — so a mutation swapping "write" for "read" here sailed
// through all seven of them. These two exercise the gate itself.

func TestAReadOnlyPrincipalCannotDownloadABackup(t *testing.T) {
	_, mux, id := dlServerAs(t, &Session{
		AuthMode: "modern", Username: "carol",
		Pages:    map[string]string{"backups": "read"},
		Readable: []string{"r1"},
	})
	for _, part := range []string{"rsc", "backup"} {
		w := dlGet(mux, id, part, "r1")
		if w.Code != http.StatusForbidden {
			t.Errorf("a principal with backups at READ got %d for /%s — an export "+
				"describing the whole network, and a binary holding every key on "+
				"the device, handed to a viewer the page never offered them to",
				w.Code, part)
		}
	}
}

// The other direction, or the case above passes by refusing everybody.
func TestAWritePrincipalCanDownloadABackup(t *testing.T) {
	_, mux, id := dlServerAs(t, &Session{
		AuthMode: "modern", Username: "carol",
		Pages:    map[string]string{"backups": "write"},
		Readable: []string{"r1"},
	})
	if w := dlGet(mux, id, "rsc", "r1"); w.Code != http.StatusOK {
		t.Errorf("a principal with backups at WRITE was refused: %d %s",
			w.Code, w.Body.String())
	}
}
