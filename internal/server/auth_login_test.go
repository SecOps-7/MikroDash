package server

// Login served by Go, driven through the REAL mux.
//
// ── THE FIRST ASSERTION IS THAT IT IS NOT REGISTERED ────────────────────────
//
// Registering these routes while Node runs is the failure this whole design
// avoids: `/api/auth/login` would stop reaching Node, the browser would hold a
// Go session Node does not know, and every unported page would answer 401 — a
// bug that looks like "the login works but half the app logged me out". So the
// coexistence case is tested first and explicitly.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/store"
	"mikrodash/internal/websession"
)

// authFixture writes a /data with one user whose password is known, using the
// SAME hashing the app uses so the verification path is real rather than
// stubbed. The password is a literal here and nowhere else — it is a value this
// test invents, not one taken from any install.
func authFixture(t *testing.T, password string) *store.Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".secret"), []byte("test-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	salt := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hash := store.HashPassword(password, salt)
	users := `[{"id":"u-1","username":"someone","role":"admin","salt":"` + salt +
		`","passwordHash":"` + hash + `"}]`
	if err := os.WriteFile(filepath.Join(dir, "users.json"), []byte(users), 0o600); err != nil {
		t.Fatal(err)
	}
	// `collectionMigrated` SET, because this fixture stands for an ordinary
	// install rather than one mid-upgrade. Without it the #105 one-shot runs on
	// the first `Server` built here and SAVES settings — and live's `save()`
	// writes `{...load(), ...updates}`, so every default is materialised into the
	// file. That is faithful (it is what a real install gets on its first start
	// after upgrading) and it is not what these tests are about: one of them
	// asserts that a session never expires BECAUSE no `sessionTimeoutMs` is
	// configured, which stops being true the moment the default is written in.
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(`{"collectionMigrated":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func newAuthServer(t *testing.T, nodeURL, password string) http.Handler {
	t.Helper()
	srv, err := New(authFixture(t, password), Options{NodeURL: nodeURL, WebDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return srv.Handler()
}

func postLogin(h http.Handler, user, pass string) *httptest.ResponseRecorder {
	body := `{"username":"` + user + `","password":"` + pass + `"}`
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestLoginIsNotServedWhileNodeRuns. The route must reach the proxy, which in a
// test has nowhere to go and answers 502 — that is the PROOF it was proxied.
// A 200 or a 401 here would mean Go answered, which is the coexistence bug.
func TestLoginIsNotServedWhileNodeRuns(t *testing.T) {
	h := newAuthServer(t, "http://127.0.0.1:1", "hunter-two-not-a-real-password")
	rec := postLogin(h, "someone", "hunter-two-not-a-real-password")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("POST /api/auth/login answered %d with a Node configured, want 502 (proxied). "+
			"Go must NOT own this route during coexistence: the browser would hold a session "+
			"Node does not know and every unported page would answer 401", rec.Code)
	}
	if rec.Header().Get("Set-Cookie") != "" {
		t.Error("a cookie was minted while Node is the authority")
	}
}

// TestLoginMintsASessionWhenStandalone.
func TestLoginMintsASessionWhenStandalone(t *testing.T) {
	const pw = "correct-horse-battery-staple"
	h := newAuthServer(t, "", pw)

	rec := postLogin(h, "someone", pw)
	if rec.Code != http.StatusOK {
		t.Fatalf("login answered %d, want 200: %s", rec.Code, rec.Body.String())
	}
	cookie := rec.Header().Get("Set-Cookie")
	if !strings.HasPrefix(cookie, websession.CookieName+"=") {
		t.Fatalf("no session cookie: %q", cookie)
	}
	for _, want := range []string{"HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("the cookie is missing %s: %q", want, cookie)
		}
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["ok"] != true || body["username"] != "someone" || body["role"] != "admin" {
		t.Errorf("the body is not the live shape: %v", body)
	}

	// The cookie is then ACCEPTED — a login that mints a session nothing
	// honours is worse than no login, and would pass every assertion above.
	token := strings.SplitN(strings.TrimPrefix(cookie, websession.CookieName+"="), ";", 2)[0]
	req := httptest.NewRequest("GET", "/api/auth/status", nil)
	req.Header.Set("Cookie", websession.CookieName+"="+token)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	var st map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &st)
	sess, _ := st["session"].(map[string]any)
	if sess == nil || sess["username"] != "someone" {
		t.Errorf("the minted session was not recognised: %s", rec2.Body.String())
	}

	// ...and LOGOUT ends it.
	reqOut := httptest.NewRequest("GET", "/api/auth/logout", nil)
	reqOut.Header.Set("Cookie", websession.CookieName+"="+token)
	recOut := httptest.NewRecorder()
	h.ServeHTTP(recOut, reqOut)
	if !strings.Contains(recOut.Header().Get("Set-Cookie"), "Max-Age=0") {
		t.Error("logout did not clear the cookie")
	}
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req)
	var st3 map[string]any
	_ = json.Unmarshal(rec3.Body.Bytes(), &st3)
	if st3["session"] != nil {
		t.Error("the session survived a logout")
	}
}

// TestAWrongPasswordAndAMissingUserAreIndISTINGUISHABLE.
//
// Both by STATUS and by BODY. The constant-time hashing in store.VerifyPassword
// closes the timing channel; a response that named which half failed would hand
// back by content exactly what that withholds.
func TestAWrongPasswordAndAMissingUserAreIndistinguishable(t *testing.T) {
	h := newAuthServer(t, "", "the-right-one")

	wrong := postLogin(h, "someone", "the-wrong-one")
	missing := postLogin(h, "nobody-at-all", "the-wrong-one")

	if wrong.Code != http.StatusUnauthorized || missing.Code != http.StatusUnauthorized {
		t.Fatalf("statuses %d and %d, want 401 for both", wrong.Code, missing.Code)
	}
	if wrong.Body.String() != missing.Body.String() {
		t.Errorf("a wrong password and a missing user answer differently:\n  wrong:   %s"+
			"\n  missing: %s\nThat is a username-enumeration oracle in the response body, "+
			"and it undoes the constant-time hashing underneath",
			wrong.Body.String(), missing.Body.String())
	}
	if !strings.Contains(wrong.Body.String(), "Invalid username or password") {
		t.Errorf("the live message is 'Invalid username or password': %s", wrong.Body.String())
	}
	for _, r := range []*httptest.ResponseRecorder{wrong, missing} {
		if r.Header().Get("Set-Cookie") != "" {
			t.Error("a failed login set a cookie")
		}
	}
}

// TestMissingCredentialsAre400. The live route distinguishes "you sent nothing"
// from "you sent the wrong thing", and that is not an oracle: it says nothing
// about whether any account exists.
func TestMissingCredentialsAre400(t *testing.T) {
	h := newAuthServer(t, "", "whatever")
	for _, c := range []struct{ user, pass string }{
		{"", "x"}, {"someone", ""}, {"", ""},
	} {
		if rec := postLogin(h, c.user, c.pass); rec.Code != http.StatusBadRequest {
			t.Errorf("login(%q,%q) answered %d, want 400", c.user, c.pass, rec.Code)
		}
	}
	// A body that is not JSON at all reads as empty credentials, not as a 500.
	req := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader("not json"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a non-JSON body answered %d, want 400", rec.Code)
	}
}

// TestTheAuditActorForALoginIsTheClaimedUsername.
//
// ── THE ASSERTION READS THE ROW, NOT THE RESPONSE ───────────────────────────
//
// This is the second identity bug found in two days by looking at the real
// database rather than at a test, and the shape is identical to the
// `user_layouts` keying one: a value written into a table BOTH PROCESSES SHARE,
// where a round trip through either half agrees with itself whatever was
// written. Nothing errors. The only way to see it is to compare what each writes.
//
// `httpRecorder(r, nil)` passes an empty name to `audit.ForUser`, which
// substitutes "local" — the fallback for an unauthenticated socket. So every
// login through this process appeared in the trail as `local` where Node records
// the account name, and the actor column is exactly what an operator filters by.
//
// THE ID STAYS NULL, which is `forLogin`'s point: "a failed login may name a
// user that does not exist, and that is worth seeing". So a successful login
// carries the id in target_id and not in actor_id — asserted here, because a
// port "improving" that would look tidier and would break the filter Node uses.
func TestTheAuditActorForALoginIsTheClaimedUsername(t *testing.T) {
	const pw = "an-invented-password-for-audit"
	h, _, _ := grantedServer(t, pw)

	// `grantedServer` has already signed in once — that success is the row this
	// checks, rather than a second login on top of it.
	//
	// ...and a FAILED one for a user that does not exist, which is the case the
	// null id exists for.
	if rec := postLogin(h, "nobody-at-all", "wrong"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("the failed login answered %d", rec.Code)
	}

	rows := auditRows(t, "auth.login")
	if len(rows) != 2 {
		t.Fatalf("%d auth.login rows, want 2 (one success from the fixture, one refusal)",
			len(rows))
	}
	for _, row := range rows {
		if row.ActorName == "local" {
			t.Errorf("the audit actor is %q. Node records the claimed USERNAME here, and the "+
				"actor column is what an operator filters by -- every login through this "+
				"process would be attributed to nobody", row.ActorName)
		}
		if row.ActorID != "" {
			t.Errorf("actor_id is %q, want empty: a login is a PRE-authentication event and "+
				"the claimed name is all there is", row.ActorID)
		}
	}
	byName := map[string]auditedActor{}
	for _, row := range rows {
		byName[row.ActorName] = row
	}
	ok, found := byName["someone"]
	if !found {
		t.Fatalf("no row names the account that signed in: %+v", rows)
	}
	if ok.TargetID != "u-1" {
		t.Errorf("target_id is %q, want the user id -- the id belongs there, not in actor_id",
			ok.TargetID)
	}
	// THE CLAIMED NAME FOR A USER THAT DOES NOT EXIST. Resolving it away would
	// hide exactly the row an operator is looking for after a break-in attempt.
	denied, found := byName["nobody-at-all"]
	if !found {
		t.Fatalf("the failed login did not record the claimed name: %+v", rows)
	}
	if denied.Outcome != "denied" {
		t.Errorf("outcome is %q, want denied", denied.Outcome)
	}
}

// auditedActor is one row's identity columns. NOT called auditRow: that name is
// already the API's row type in audit_api.go.
type auditedActor struct {
	ActorID, ActorName, TargetID, Outcome string
}

// auditRows reads what was actually written, rather than what the handler
// returned. See the test above for why that distinction is the whole point.
func auditRows(t *testing.T, action string) []auditedActor {
	t.Helper()
	h, err := sql.Open("sqlite", navDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	rows, err := h.Query(
		`SELECT COALESCE(actor_id,''), COALESCE(actor_name,''), COALESCE(target_id,''),
		        COALESCE(outcome,'') FROM audit_events WHERE action = ? ORDER BY ts`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := []auditedActor{}
	for rows.Next() {
		var r auditedActor
		if err := rows.Scan(&r.ActorID, &r.ActorName, &r.TargetID, &r.Outcome); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}
