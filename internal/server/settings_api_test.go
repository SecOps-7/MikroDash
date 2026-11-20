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

// TestAStoredSignInOffGrantsNothing: an install whose settings.json still says
// `"authMode":"none"` (the retired Require sign-in switch, #165) is not opened
// by it. The session is an ordinary one with no grants, so it gets the viewer
// payload, and the retired key is not echoed to anyone as if it still counted.
func TestAStoredSignInOffGrantsNothing(t *testing.T) {
	s := settingsServer(t, `{"authMode":"none","topN":25,"routerHost":"198.51.100.1"}`)
	sess := &Session{Username: "someone"}
	if s.maySeeAllSettings(sess) {
		t.Fatal("a stored authMode none granted the administrator payload")
	}
	got := getSettings(t, s, sess)
	if _, ok := got["routerHost"]; ok {
		t.Error("a stored authMode none disclosed routerHost")
	}
	if v, ok := got["authMode"]; ok {
		t.Errorf("the retired authMode is still sent: %#v", v)
	}
	if got["topN"] != float64(25) {
		t.Errorf("topN = %#v, want the stored 25: the rest of the file must still load", got["topN"])
	}
	// Nor to an administrator, whose payload is every setting there is.
	admin := &Session{}
	signedInAdmin(t, s, admin)
	if v, ok := getSettings(t, s, admin)["authMode"]; ok {
		t.Errorf("the administrator payload still carries the retired authMode: %#v", v)
	}
}

// TestWithoutTheResolverTheViewerPayloadIsServed — FAILS CLOSED. An
// unanswerable permission question yields the reduced view, which still renders
// a working dashboard, rather than the administrator's.
func TestWithoutTheResolverTheViewerPayloadIsServed(t *testing.T) {
	s := settingsServer(t, `{"topN":25,"routerHost":"198.51.100.1"}`)
	sess := &Session{Username: "someone"}
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
	// `authMode` left this list with the Require sign-in switch (#165).
	for _, want := range []string{"pingTarget", "topN", "displayTimezone"} {
		if _, present := got[want]; !present {
			t.Errorf("the viewer payload is missing %q, which a rendering page reads", want)
		}
	}
}

// TestAMissingSettingsFileServesDefaults — `load()` catches its own read failure
// and starts from the defaults, so a fresh install must not answer 500.
func TestAMissingSettingsFileServesDefaults(t *testing.T) {
	s := settingsServer(t, "")
	admin := &Session{}
	signedInAdmin(t, s, admin)
	got := getSettings(t, s, admin)
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
	raw := `{"aiApiKey":` + jsonString(sealed) + `}`
	if err := os.WriteFile(filepath.Join(s.store.Dir, "settings.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}

	admin := &Session{}
	signedInAdmin(t, s, admin)
	got := getSettings(t, s, admin)
	if got["aiApiKey"] != store.Mask {
		t.Errorf("aiApiKey = %#v, want the mask", got["aiApiKey"])
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
