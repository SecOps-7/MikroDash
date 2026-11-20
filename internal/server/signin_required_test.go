package server

// Sign-in is always required (#165; the operator's ruling of 2026-11-20,
// docs/DECISIONS.md). The "Require sign-in" switch is gone. These tests drive
// the real handler the way the issue's reporter did, with curl and no cookie,
// against a /data whose settings.json still says `"authMode":"none"`: an
// install that switched sign-in off before this release must come up requiring
// it, and one with no account yet must still reach first-run setup.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/store"
)

// signInOffInstall is a /data that switched sign-in off under an older
// release. usersJSON "" means no users.json at all: a fresh install.
//
// `collectionMigrated` is set, as on any install that has run a recent
// release: without it the start-up migration rewrites settings.json from the
// defaults, which happens to erase the stored "none", and these tests would
// then never meet the value they are about.
func signInOffInstall(t *testing.T, usersJSON string) (codedHandler, string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".secret": "test-secret", "routers.json": `[]`,
		"settings.json": `{"authMode":"none","topN":25,"collectionMigrated":true}`,
	}
	if usersJSON != "" {
		files["users.json"] = usersJSON
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(st, Options{WebDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return codedHandler{srv.Handler(), srv}, dir
}

func anonymous(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = "10.0.0.9:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// TestAStoredSignInOffStillRequiresSignIn is the issue's three checks, with the
// answers sign-in requires.
func TestAStoredSignInOffStillRequiresSignIn(t *testing.T) {
	h, dir := signInOffInstall(t, seedUsersJSON)

	// 1. The app sends a visitor with no session to the login page.
	if w := anonymous(h, "GET", "/", ""); w.Code != http.StatusFound || w.Header().Get("Location") != "/login" {
		t.Errorf("GET / answered %d to %q, want 302 to /login", w.Code, w.Header().Get("Location"))
	}
	// 2. The API refuses without a session.
	if w := anonymous(h, "GET", "/api/settings", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("GET /api/settings answered %d without a session, want 401: %s", w.Code, w.Body.String())
	}
	// 3. The status names no session and no auth mode: there is no switch for
	// the login page to consult, and a stored one must not be echoed.
	w := anonymous(h, "GET", "/api/auth/status", "")
	var status map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("status is not JSON: %s", w.Body.String())
	}
	if _, ok := status["session"]; !ok || status["session"] != nil {
		t.Errorf("status session = %#v, want null", status["session"])
	}
	if v, ok := status["authMode"]; ok {
		t.Errorf("status still sends authMode %#v", v)
	}
	if status["firstRun"] != false {
		t.Errorf("an install with accounts reports firstRun %#v", status["firstRun"])
	}

	// And nobody without a session can change anything, sign-in included.
	before, _ := os.ReadFile(filepath.Join(dir, "settings.json"))
	if !strings.Contains(string(before), `"authMode":"none"`) {
		t.Fatalf("the stored none is not on disk, so nothing above met it: %s", before)
	}
	for _, body := range []string{`{"topN":50}`, `{"authMode":"modern"}`, `{"authMode":"none"}`} {
		if w := anonymous(h, "POST", "/api/settings", body); w.Code != http.StatusUnauthorized {
			t.Errorf("POST /api/settings %s answered %d without a session, want 401", body, w.Code)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "settings.json")); string(after) != string(before) {
		t.Errorf("settings.json changed under anonymous saves:\n%s\n->\n%s", before, after)
	}
}

// TestAFreshInstallStillReachesFirstRunSetup: with no account yet, sign-in is
// still required for the app, and the first administrator can be created.
func TestAFreshInstallStillReachesFirstRunSetup(t *testing.T) {
	h, _ := signInOffInstall(t, "")

	w := anonymous(h, "GET", "/api/auth/status", "")
	var status map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &status); err != nil {
		t.Fatalf("status is not JSON: %s", w.Body.String())
	}
	if status["firstRun"] != true {
		t.Fatalf("a fresh install reports firstRun %#v; the setup page would never show", status["firstRun"])
	}
	if status["session"] != nil {
		t.Errorf("a fresh install answered a session: %#v", status["session"])
	}
	if w := anonymous(h, "GET", "/", ""); w.Code != http.StatusFound {
		t.Errorf("GET / on a fresh install answered %d, want 302 to the login page", w.Code)
	}
	if w := postSetup(h, `{"username":"admin","password":"longenough"}`); w.Code != http.StatusOK {
		t.Errorf("first-run setup answered %d: %s", w.Code, w.Body.String())
	}
}
