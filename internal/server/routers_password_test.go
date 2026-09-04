package server

// The Edit dialog's password field, through the REAL mux.
//
// ── WHAT THESE PIN, AND WHY IT IS NOT OBVIOUS FROM THE ROUTE ───────────────
//
// `Router.Encrypted` is tagged `json:"password"`. So does the browser's form
// field. One key, two meanings, and `routerUpdate` used to patch the second over
// the first: every Save from the Edit dialog replaced the SEALED credential with
// the raw contents of a box the dialog itself had just cleared.
//
// The dialog's placeholder says "leave blank to keep current", so the normal
// case — open it to change a label, press Save — silently destroyed the router's
// password. Issue #124: "suddenly it doesn't work anymore", from a user who had
// opened the dialog to change a bandwidth unit. `Test Connection` went on
// passing, because that route uses the password TYPED INTO THE FORM rather than
// the stored one, which is why the app and the test disagreed.
//
// The body in each case is the WHOLE FORM, as `collectRouterForm` sends it —
// not a minimal patch — because sending only the field under test would not
// reproduce the bug.

import (
	"net/http"
	"strings"
	"testing"

	"mikrodash/internal/store"
)

// wholeForm is what the Add/Edit dialog posts, with `password` substituted.
func wholeForm(password string) string {
	return `{"id":"r1","label":"One","host":"198.51.100.1","port":8728,` +
		`"username":"u","password":` + password + `,"defaultIf":"ether1",` +
		`"pingTarget":"1.1.1.1","tls":true,"tlsInsecure":false,` +
		`"bwDownMbps":1000,"bwUpMbps":1000,"alertsEnabled":false,` +
		`"connDownThresholdSec":30}`
}

// storedPassword is the DECRYPTED credential for one router, or the failure.
func storedPassword(t *testing.T, s *Server, id string) string {
	t.Helper()
	rs, problems := s.store.Routers()
	for _, p := range problems {
		t.Logf("store problem: %v", p)
	}
	for _, r := range rs {
		if r.ID == id {
			return r.Password
		}
	}
	t.Fatalf("router %s is gone from the store", id)
	return ""
}

// TestABlankPasswordBoxKeepsTheStoredCredential is the reported bug.
func TestABlankPasswordBoxKeepsTheStoredCredential(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	if err := s.store.SetRouterPassword("r1", "correct-horse"); err != nil {
		t.Fatal(err)
	}

	w := routerPut(mux, "r1", wholeForm(`""`), authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := storedPassword(t, s, "r1"); got != "correct-horse" {
		t.Errorf("after saving the dialog with an untouched password box the "+
			"credential is %q, want %q. The box is blank on every open and the "+
			"placeholder promises it keeps the stored value, so this save has "+
			"silently unauthenticated the router.", got, "correct-horse")
	}
}

// TestTheMaskKeepsTheStoredCredential — `routersList` sends `store.Mask` back
// for a router that has one, so a client echoing what it was given must not
// overwrite anything either.
func TestTheMaskKeepsTheStoredCredential(t *testing.T) {
	s, mux, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	if err := s.store.SetRouterPassword("r1", "correct-horse"); err != nil {
		t.Fatal(err)
	}

	w := routerPut(mux, "r1", wholeForm(`"`+store.Mask+`"`), authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := storedPassword(t, s, "r1"); got != "correct-horse" {
		t.Errorf("the mask was written over the credential: %q", got)
	}
}

// TestATypedPasswordIsSealedAndNeverStoredInClear.
//
// The other half: a password the operator DID type must actually take effect,
// or the fix above becomes "the password can never be changed". And it must
// reach disk sealed — writing it in clear was the second half of the same bug.
func TestATypedPasswordIsSealedAndNeverStoredInClear(t *testing.T) {
	s, mux, dir := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	if err := s.store.SetRouterPassword("r1", "old-one"); err != nil {
		t.Fatal(err)
	}

	w := routerPut(mux, "r1", wholeForm(`"brand-new-secret"`), authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if got := storedPassword(t, s, "r1"); got != "brand-new-secret" {
		t.Errorf("the typed password did not take effect: credential is %q", got)
	}
	// ON DISK, sealed. The whole file, because the record is written raw and a
	// stray key elsewhere would be just as bad.
	rec := routerByID(t, dir, "r1")
	onDisk, _ := rec["password"].(string)
	if strings.Contains(onDisk, "brand-new-secret") {
		t.Errorf("the password is on disk IN CLEAR: %q", onDisk)
	}
	if onDisk == "" {
		t.Error("nothing was written where the sealed credential belongs")
	}
}

// TestTheStoreRefusesAnUnsealedPasswordPatch is the mechanism behind the fix,
// tested where a FUTURE caller would hit it. `SetRouterPassword` has warned in a
// comment since it was written that assembling this patch by hand is the hazard;
// six call sites relied on that comment, and the seventh is the one that broke.
func TestTheStoreRefusesAnUnsealedPasswordPatch(t *testing.T) {
	s, _, _ := routersServer(t, &Session{AuthMode: "none", Username: "admin"}, "")
	if err := s.store.SetRouterPassword("r1", "correct-horse"); err != nil {
		t.Fatal(err)
	}

	err := s.store.UpdateRouter("r1", map[string]any{"password": "plaintext-oops"})
	if err == nil {
		t.Fatal("UpdateRouter accepted a plaintext credential")
	}
	if !strings.Contains(err.Error(), "sealed") {
		t.Errorf("the refusal does not say what is wrong: %v", err)
	}
	if got := storedPassword(t, s, "r1"); got != "correct-horse" {
		t.Errorf("the refused patch still changed the credential: %q", got)
	}

	// AND THE DELIBERATE CLEAR STILL WORKS. `Encrypt("")` is "", so an empty
	// string is a well-formed sealed value; refusing it would break
	// `SetRouterPassword(id, "")`.
	if err := s.store.SetRouterPassword("r1", ""); err != nil {
		t.Fatalf("clearing a router password was refused: %v", err)
	}
	if got := storedPassword(t, s, "r1"); got != "" {
		t.Errorf("the clear did not take: %q", got)
	}
}
