package server

// The disclosure boundary, exercised through the HTTP handler rather than only
// through the store.
//
// The store's own tests pin WHAT each payload contains. These pin that the
// handler picks the right one, and that the choice is made on a PERMISSION
// rather than on the stored role — the distinction the live comment calls out,
// because "an administrator whose grant is held through a group has role
// 'viewer' on their user record".

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/store"
)

// settingsServer builds a server whose store points at a throwaway /data.
func settingsServer(t *testing.T, settingsJSON string) *Server {
	t.Helper()
	dir := t.TempDir()
	if settingsJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// `.secret` is what the encryption key is derived from, and Open refuses
	// without it.
	//
	// THIS WAS A t.Skipf, AND THAT WAS A BUG IN THE TEST. Every one of the four
	// tests in this file skipped, and `go test` printed ok — four tests written
	// and none of them run. A skip is for something the environment does not
	// have; the secret is something this helper controls, so it creates one.
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret-value"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return &Server{store: st}
}

func getSettings(t *testing.T, s *Server, sess *Session) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	// THE SAME CODE THE HANDLER RUNS. An earlier version of this helper
	// reimplemented the payload choice beside the handler, which would have let
	// a mutation to the handler pass unnoticed — the test would have been
	// checking its own copy.
	writeJSON(rec, s.settingsPayload(sess))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestAuthModeNoneSeesEverything — one local operator with full reach, the same
// short circuit rbac.js makes.
func TestAuthModeNoneSeesEverything(t *testing.T) {
	s := settingsServer(t, `{"topN":25}`)
	if !s.maySeeAllSettings(&Session{AuthMode: "none"}) {
		t.Fatal("auth mode none was refused the administrator payload")
	}
	got := getSettings(t, s, &Session{AuthMode: "none"})
	if _, ok := got["routerHost"]; !ok {
		t.Error("the administrator payload is missing routerHost")
	}
	if got["topN"] != float64(25) {
		t.Errorf("topN = %#v, want the stored 25", got["topN"])
	}
}

// TestWithoutTheResolverTheViewerPayloadIsServed — FAILS CLOSED. An
// unanswerable permission question yields the reduced view, which still renders
// a working dashboard, rather than the administrator's.
func TestWithoutTheResolverTheViewerPayloadIsServed(t *testing.T) {
	s := settingsServer(t, `{"topN":25,"routerHost":"198.51.100.1"}`)
	sess := &Session{AuthMode: "modern", Username: "someone"}
	if s.maySeeAllSettings(sess) {
		t.Fatal("a server with no RBAC resolver granted the administrator payload")
	}

	got := getSettings(t, s, sess)
	for _, leak := range []string{"routerHost", "routerUser", "routerPass", "smtpHost", "telegramChatId"} {
		if _, present := got[leak]; present {
			t.Errorf("the viewer payload carries %q — the live comment calls these "+
				"admin-only recon", leak)
		}
	}
	// AND IT IS STILL USABLE: the fields a dashboard needs are there.
	for _, want := range []string{"authMode", "pingEnabled", "topN", "displayTimezone"} {
		if _, present := got[want]; !present {
			t.Errorf("the viewer payload is missing %q, which a rendering page reads", want)
		}
	}
}

// TestAMissingSettingsFileServesDefaults — `load()` catches its own read failure
// and starts from the defaults, so a fresh install must not answer 500.
func TestAMissingSettingsFileServesDefaults(t *testing.T) {
	s := settingsServer(t, "")
	got := getSettings(t, s, &Session{AuthMode: "none"})
	if len(got) < 100 {
		t.Fatalf("a missing settings.json produced %d keys; the defaults are ~113", len(got))
	}
	if _, ok := got["pollConns"]; !ok {
		t.Error("a default is missing from the payload")
	}
}

// TestTheHandlerNeverSendsACredential is the one worth having at this level.
// The store's tests prove the masking; this proves the HANDLER uses the masking
// path, so a future edit that reached for the merged map directly is caught.
func TestTheHandlerNeverSendsACredential(t *testing.T) {
	s := settingsServer(t, "")
	// Seal a credential the way the app does, then read it back through the
	// handler's own path.
	sealed, err := s.store.Encrypt("NOT-A-REAL-TOKEN")
	if err != nil {
		t.Fatalf("sealing in a store this helper built: %v", err)
	}
	raw := `{"telegramBotToken":` + jsonString(sealed) + `}`
	if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	got := getSettings(t, s, &Session{AuthMode: "none"})
	if got["telegramBotToken"] != store.Mask {
		t.Errorf("telegramBotToken = %#v, want the mask", got["telegramBotToken"])
	}
	body, _ := json.Marshal(got)
	if containsStr(string(body), "NOT-A-REAL-TOKEN") {
		t.Error("the plaintext credential reached the payload")
	}
	if containsStr(string(body), sealed) {
		t.Error("the CIPHERTEXT reached the payload — masked presence is the whole " +
			"contract, and sealed bytes are still the credential")
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func containsStr(h, n string) bool {
	if n == "" {
		return false
	}
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
