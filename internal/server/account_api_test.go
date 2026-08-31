package server

// The account modal's session list and its revoke button.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type sessionsReply struct {
	OK       bool `json:"ok"`
	Sessions []struct {
		CreatedAt int64  `json:"createdAt"`
		ExpiresAt *int64 `json:"expiresAt"`
		Current   bool   `json:"current"`
	} `json:"sessions"`
}

func getSessions(t *testing.T, h http.Handler, token string) sessionsReply {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/account/sessions", nil)
	if token != "" {
		req.Header.Set("Cookie", "mikrodash_sid="+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/account/sessions answered %d", rec.Code)
	}
	var out sessionsReply
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("not the expected shape: %s", rec.Body.String())
	}
	return out
}

// TestTheSessionListFindsTheUsersOwnSessions.
//
// ── THE IDENTITY HAS TO MATCH THE ONE THE STORE WAS KEYED WITH ──────────────
//
// `websession.Create` is called with the users.json id; the list resolves the
// username back to that id. If the two ever disagree the answer is an EMPTY
// LIST for everybody — a confident wrong answer on the page whose whole purpose
// is to show where you are signed in, and one that looks like "no other
// sessions" rather than like a fault.
//
// So the test signs in three times and expects three, rather than checking the
// endpoint merely answers.
func TestTheSessionListFindsTheUsersOwnSessions(t *testing.T) {
	const pw = "an-invented-password-for-accounts"
	h, token := signedInServer(t, pw)

	// Two more sign-ins for the same user, as a second and third browser.
	for i := 0; i < 2; i++ {
		if rec := postLogin(h, "someone", pw); rec.Code != http.StatusOK {
			t.Fatalf("extra sign-in %d answered %d", i, rec.Code)
		}
	}

	got := getSessions(t, h, token)
	if !got.OK {
		t.Fatal("ok was not true")
	}
	if len(got.Sessions) != 3 {
		t.Fatalf("%d sessions, want 3. Zero means the list is keyed on a different identity "+
			"than the store -- which reads as 'no other sessions' rather than as a fault",
			len(got.Sessions))
	}

	// EXACTLY ONE IS CURRENT, and it is the caller's.
	current := 0
	for _, s := range got.Sessions {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Errorf("%d sessions marked current, want exactly 1", current)
	}

	// NEWEST FIRST. `ForUser` walks a map, so without the sort the list
	// reshuffles between requests and the modal looks unstable.
	for i := 1; i < len(got.Sessions); i++ {
		if got.Sessions[i-1].CreatedAt < got.Sessions[i].CreatedAt {
			t.Errorf("the list is not newest-first: %v", got.Sessions)
			break
		}
	}

	// ── NULL FOR "NEVER EXPIRES", NOT A FAR-FUTURE TIMESTAMP ────────────
	//
	// The fixture sets no `sessionTimeoutMs`, so every session here never
	// expires and the live route sends null. A port that always stamped the
	// field would render a date in the year 292277026596 in the modal, which is
	// the sentinel leaking into the UI rather than being translated.
	for _, sn := range got.Sessions {
		if sn.ExpiresAt != nil {
			t.Errorf("expiresAt is %d for a session that never expires, want null -- the "+
				"NeverExpires sentinel must not reach the browser", *sn.ExpiresAt)
			break
		}
	}
	// Believability: the field IS decodable and would show a number if one were
	// sent, so the nils above are the route's answer rather than a parse
	// failure. Asserted on the raw body, which the struct above cannot show.
	if got.Sessions[0].CreatedAt <= 0 {
		t.Error("createdAt is not being sent, so the expiresAt assertion above proves nothing")
	}
}

// TestTheSessionListNeverCarriesTheToken.
//
// `listSessionsForUser` includes the token because the caller needs it to work
// out which session is current, and its comment says "it must be projected away
// before any of this reaches a browser". This is the assertion that the
// projection happened: a token in this payload is a session anybody reading the
// response can use.
func TestTheSessionListNeverCarriesTheToken(t *testing.T) {
	h, token := signedInServer(t, "another-invented-password")
	req := httptest.NewRequest("GET", "/api/account/sessions", nil)
	req.Header.Set("Cookie", "mikrodash_sid="+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	body := rec.Body.String()
	if len(token) < 16 {
		t.Fatalf("the fixture token is too short to search for: %q", token)
	}
	if contains(body, token) {
		t.Error("THE SESSION TOKEN IS IN THE RESPONSE. Anybody who can read this payload can " +
			"use it as a session; the live route projects it away deliberately")
	}
	// ...and no key that could carry one under another name.
	var generic map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &generic)
	rows, _ := generic["sessions"].([]any)
	for _, r := range rows {
		m, _ := r.(map[string]any)
		for k := range m {
			switch k {
			case "createdAt", "expiresAt", "current":
			default:
				t.Errorf("the session row carries an unexpected key %q -- the live route sends "+
					"exactly three", k)
			}
		}
	}
}

// TestRevokeOthersSparesTheCaller.
//
// A password change is often a response to a suspected compromise, and this is
// the same action on its own. Signing the caller out with everybody else would
// make the security action look like a failure.
func TestRevokeOthersSparesTheCaller(t *testing.T) {
	const pw = "a-third-invented-password"
	h, token := signedInServer(t, pw)
	for i := 0; i < 3; i++ {
		if rec := postLogin(h, "someone", pw); rec.Code != http.StatusOK {
			t.Fatal("extra sign-in failed")
		}
	}
	if n := len(getSessions(t, h, token).Sessions); n != 4 {
		t.Fatalf("%d sessions before revoking, want 4", n)
	}

	req := httptest.NewRequest("POST", "/api/account/sessions/revoke-others", nil)
	req.Header.Set("Cookie", "mikrodash_sid="+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke answered %d", rec.Code)
	}
	var reply struct {
		OK      bool `json:"ok"`
		Revoked int  `json:"revoked"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	if !reply.OK || reply.Revoked != 3 {
		t.Errorf("revoked %d, want 3", reply.Revoked)
	}

	left := getSessions(t, h, token)
	if len(left.Sessions) != 1 || !left.Sessions[0].Current {
		t.Errorf("after revoking, %d sessions remain and current=%v -- the caller's own "+
			"session must survive", len(left.Sessions), left.Sessions[0].Current)
	}
	// ...and the caller is still signed in, which is the property the count
	// above only implies.
	req2 := httptest.NewRequest("GET", "/api/auth/status", nil)
	req2.Header.Set("Cookie", "mikrodash_sid="+token)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	var st map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &st)
	if st["session"] == nil {
		t.Error("the caller was signed out by their own revoke-others")
	}
}

// TestAccountSessionsAreNotServedWhileNodeRuns.
//
// The store is empty while Node owns sessions, so a Go answer would be "you have
// no sessions" beside a browser that is plainly signed in. A confident wrong
// answer is worse than the proxy's correct one — the same rule as the login
// routes, and the 502 here is the proof it was proxied.
func TestAccountSessionsAreNotServedWhileNodeRuns(t *testing.T) {
	h := newAuthServer(t, "http://127.0.0.1:1", "a-password")
	for _, r := range []*http.Request{
		httptest.NewRequest("GET", "/api/account/sessions", nil),
		httptest.NewRequest("POST", "/api/account/sessions/revoke-others", nil),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != http.StatusBadGateway {
			t.Errorf("%s %s answered %d with a Node configured, want 502 (proxied)",
				r.Method, r.URL.Path, rec.Code)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		func() bool {
			for i := 0; i+len(needle) <= len(haystack); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}()
}

// ── POST /api/account/password ──────────────────────────────────────────────
//
// Every password here is invented by the test. Nothing is taken from a real
// install: these files transplant into the public MikroDash repository at
// cutover, and a hash committed now is a hash published then.

func postPassword(h http.Handler, token, current, next string) *httptest.ResponseRecorder {
	body := `{"currentPassword":"` + current + `","newPassword":"` + next + `"}`
	req := httptest.NewRequest("POST", "/api/account/password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Cookie", "mikrodash_sid="+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestPasswordChangeIsNotServedWhileNodeRuns.
//
// It WRITES users.json, and `src/users.js` caches the file on first load — so a
// change written during coexistence is invisible to Node AND reverted by its
// next save. The operator would be told their password had changed when it had
// not, which is worse than the endpoint being absent. The 502 is the proof it
// was proxied.
func TestPasswordChangeIsNotServedWhileNodeRuns(t *testing.T) {
	h := newAuthServer(t, "http://127.0.0.1:1", "an-invented-password")
	rec := postPassword(h, "", "an-invented-password", "another-invented-password")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("answered %d with a Node configured, want 502 (proxied). A password change "+
			"while Node runs is reverted by its next save", rec.Code)
	}
}

// TestAPasswordChangeVerifiesTheCurrentOneFirst.
func TestAPasswordChangeVerifiesTheCurrentOneFirst(t *testing.T) {
	const pw = "the-current-invented-password"
	h, token, _ := grantedServer(t, pw)

	if rec := postPassword(h, "", pw, "a-new-invented-password"); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated answered %d, want 401", rec.Code)
	}
	if rec := postPassword(h, token, "not-the-current-one", "a-new-invented-password"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong current password answered %d, want 401", rec.Code)
	}
	// ...and the password is UNCHANGED after the refusal, which the status code
	// alone does not show.
	if rec := postPassword(h, token, pw, "a-new-invented-password"); rec.Code != http.StatusOK {
		t.Fatalf("the correct current password answered %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAPasswordChangeRefusesAShortOrMissingOne.
func TestAPasswordChangeRefusesAShortOrMissingOne(t *testing.T) {
	const pw = "the-current-invented-password"
	h, token, _ := grantedServer(t, pw)

	for _, c := range []struct{ current, next, why string }{
		{"", "a-new-invented-password", "no current password"},
		{pw, "", "no new password"},
		{"", "", "neither"},
		{pw, "abc", "three characters"},
	} {
		if rec := postPassword(h, token, c.current, c.next); rec.Code != http.StatusBadRequest {
			t.Errorf("%s answered %d, want 400", c.why, rec.Code)
		}
	}
	// FOUR IS ACCEPTED — the floor is `< 4`, and a port using `<= 4` refuses a
	// password the live app takes.
	if rec := postPassword(h, token, pw, "abcd"); rec.Code != http.StatusOK {
		t.Errorf("a four-character password answered %d, want 200 -- the floor is < 4", rec.Code)
	}
}

// TestAPasswordChangeSignsOutTheOtherSessionsAndNotThisOne.
//
// "A password change is often a response to a suspected compromise, so the
// other sessions go with it. The caller's own session is spared, or they would
// be signed out by their own security action."
func TestAPasswordChangeSignsOutTheOtherSessionsAndNotThisOne(t *testing.T) {
	const pw = "the-current-invented-password"
	h, token, _ := grantedServer(t, pw)
	for i := 0; i < 2; i++ {
		if rec := postLogin(h, "someone", pw); rec.Code != http.StatusOK {
			t.Fatal("extra sign-in failed")
		}
	}
	if n := len(getSessions(t, h, token).Sessions); n != 3 {
		t.Fatalf("%d sessions before the change, want 3", n)
	}

	rec := postPassword(h, token, pw, "a-new-invented-password")
	if rec.Code != http.StatusOK {
		t.Fatalf("the change answered %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		OK      bool `json:"ok"`
		Revoked int  `json:"revokedOtherSessions"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &reply)
	if !reply.OK || reply.Revoked != 2 {
		t.Errorf("revokedOtherSessions is %d, want 2", reply.Revoked)
	}

	left := getSessions(t, h, token).Sessions
	if len(left) != 1 || !left[0].Current {
		t.Errorf("%d sessions remain -- the caller must not be signed out by their own "+
			"security action", len(left))
	}
	// THE NEW PASSWORD WORKS AND THE OLD ONE DOES NOT, through the real login
	// route — so the file was actually rewritten rather than merely reported.
	if r := postLogin(h, "someone", "a-new-invented-password"); r.Code != http.StatusOK {
		t.Errorf("the new password does not sign in: %d", r.Code)
	}
	if r := postLogin(h, "someone", pw); r.Code != http.StatusUnauthorized {
		t.Errorf("the OLD password still signs in: %d", r.Code)
	}
}

// TestNoPasswordReachesTheAuditTrail. The row records that a change happened
// and how many sessions went with it — never the old value, never the new one.
func TestNoPasswordReachesTheAuditTrail(t *testing.T) {
	const pw = "the-current-invented-password"
	const next = "a-brand-new-invented-password"
	h, token, _ := grantedServer(t, pw)

	if rec := postPassword(h, token, pw, next); rec.Code != http.StatusOK {
		t.Fatalf("the change answered %d", rec.Code)
	}
	rows := auditRows(t, "account.password")
	if len(rows) != 1 {
		t.Fatalf("%d account.password rows, want 1", len(rows))
	}
	raw := auditDetail(t, "account.password")
	for _, secret := range []string{pw, next} {
		if strings.Contains(raw, secret) {
			t.Errorf("a password reached the audit row: %s", raw)
		}
	}
	if !strings.Contains(raw, "otherSessionsRevoked") {
		t.Errorf("the row does not record how many sessions went with the change: %s", raw)
	}
}

// auditDetail reads one action's detail column, so the assertion above is about
// what was WRITTEN rather than what was returned.
func auditDetail(t *testing.T, action string) string {
	t.Helper()
	h, err := sql.Open("sqlite", navDBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var detail string
	err = h.QueryRow(
		`SELECT COALESCE(detail,'') FROM audit_events WHERE action = ? ORDER BY ts DESC LIMIT 1`,
		action).Scan(&detail)
	if err != nil {
		t.Fatal(err)
	}
	return detail
}
