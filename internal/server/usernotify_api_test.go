package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTheInstallSwitchIsTheFirstGate.
//
// It ships OFF, and the live comment says why: per-user ntfy and SMTP let the
// USER choose a destination host, so enabling this widens what an ordinary
// account can make the server connect to. A server with no settings store at all
// must therefore answer "disabled", not "allowed" — absent means off.
func TestTheInstallSwitchIsTheFirstGate(t *testing.T) {
	s := &Server{}
	if s.userNotifyEnabled() {
		t.Fatal("a server with no settings store reported the feature ENABLED -- " +
			"absent must mean off, or an install that upgraded gets it switched on")
	}

	for _, path := range []string{
		userNotifyPath, userNotifyPath + "/test-notification",
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		_, _, ok := s.requireUserNotify(w, r)
		if ok {
			t.Errorf("%s passed the gate with the feature disabled", path)
		}
		if w.Code != http.StatusForbidden {
			t.Errorf("%s answered %d, want 403", path, w.Code)
		}
		// The refusal must say WHICH refusal it is: an operator seeing this needs
		// to know it is an install setting, not their account.
		if !strings.Contains(w.Body.String(), "disabled for this install") {
			t.Errorf("%s: %q does not name the install switch", path, w.Body.String())
		}
	}
}

// TestTheGateChecksTheSwitchBeforeTheSession.
//
// Order matters: a disabled feature must answer the same way to everyone,
// signed in or not. Checking the session first would let an anonymous caller
// tell an install with the feature off from one with it on, by the difference
// between 401 and 403.
func TestTheGateChecksTheSwitchBeforeTheSession(t *testing.T) {
	s := &Server{} // no store => disabled, and no auth either
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", userNotifyPath, nil)
	r.Header.Set("Cookie", "session=nonsense")

	if _, _, ok := s.requireUserNotify(w, r); ok {
		t.Fatal("the gate passed")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("answered %d with an invalid session; want 403 from the install switch, "+
			"so a caller cannot distinguish installs by their auth response", w.Code)
	}
}

// TestTheRoutesAreRegisteredWithTheirOwnLimits.
//
// The test endpoint makes this server connect to a host the USER chose, so it
// gets a tenth of the read/write budget. A shared limiter would let someone
// spend the whole allowance on outbound requests.
func TestTheRoutesAreRegisteredWithTheirOwnLimits(t *testing.T) {
	s := &Server{}
	mux := http.NewServeMux()
	s.registerUserNotify(mux)

	for _, tc := range []struct{ method, path string }{
		{"GET", userNotifyPath},
		{"POST", userNotifyPath},
		{"POST", userNotifyPath + "/test-notification"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		h, pattern := mux.Handler(req)
		if h == nil || pattern == "" {
			t.Errorf("%s %s matches no route", tc.method, tc.path)
		}
	}
	// The test route must match its OWN pattern, not the bare one.
	req := httptest.NewRequest("POST", userNotifyPath+"/test-notification", nil)
	_, pattern := mux.Handler(req)
	if !strings.Contains(pattern, "test-notification") {
		t.Errorf("the test route matched %q -- it would share the 60/min budget", pattern)
	}

	// THE REGISTERED HANDLERS, not limiters this test built.
	//
	// An earlier version constructed a 60 and a 10 here and asserted they behaved
	// differently, which they do — and a mutation making the test route SHARE the
	// read/write limiter survived it, because the assertion never touched the
	// mux. The limiter runs before the handler, so a refused request answers 429
	// where an allowed one falls through to the install-switch 403.
	send := func(path string) int {
		req := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		req.RemoteAddr = "10.0.0.9:1234"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	for i := 1; i <= 10; i++ {
		if code := send(userNotifyPath + "/test-notification"); code == http.StatusTooManyRequests {
			t.Fatalf("test request %d of 10 was rate-limited", i)
		}
	}
	if code := send(userNotifyPath + "/test-notification"); code != http.StatusTooManyRequests {
		t.Errorf("an eleventh test request answered %d, want 429", code)
	}
	// ...and the read/write budget is untouched by that burst.
	for i := 1; i <= 11; i++ {
		if code := send(userNotifyPath); code == http.StatusTooManyRequests {
			t.Fatalf("save %d was rate-limited after the TEST budget was spent -- "+
				"the two routes are sharing a limiter", i)
		}
	}
}

// TestAMalformedBodyIsRefusedBeforeAnythingElse.
func TestAMalformedBodyIsRefusedBeforeAnythingElse(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", userNotifyPath, strings.NewReader("{not json"))
	if _, ok := s.readUserNotifyBody(w, r); ok {
		t.Fatal("malformed JSON was accepted")
	}
	if w.Code != http.StatusBadRequest {
		t.Errorf("answered %d, want 400", w.Code)
	}

	// An empty body is NOT malformed: it is a save that changes nothing, and the
	// merge handles it.
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest("POST", userNotifyPath, strings.NewReader(`{}`))
	body, ok := s.readUserNotifyBody(w2, r2)
	if !ok {
		t.Fatalf("an empty object was refused: %s", w2.Body.String())
	}
	if body == nil {
		t.Error("an empty object decoded to nil -- the merge would see no keys at all")
	}
}

// TestAnOversizedBodyIsRefused: the per-field caps in notify apply after
// decoding, so without a limit on the whole body a caller could still make the
// server buffer an arbitrary amount before any of them ran.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	s := &Server{}
	huge := `{"emailTo":"` + strings.Repeat("x", 200<<10) + `"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", userNotifyPath, strings.NewReader(huge))
	if _, ok := s.readUserNotifyBody(w, r); ok {
		t.Error("a 200KB body was accepted")
	}
}

// TestDecryptFailureReadsAsEmpty.
//
// One unreadable credential should cost that channel, not the whole page. The
// channel then reports "not configured", which is what an operator can act on.
func TestDecryptFailureReadsAsEmpty(t *testing.T) {
	s := &Server{} // no store
	if got := s.decryptSetting("some-ciphertext"); got != "" {
		t.Errorf("decrypting without a store returned %q", got)
	}
	if got := s.decryptSetting(""); got != "" {
		t.Errorf("decrypting an empty value returned %q", got)
	}
}

// TestEncryptFailureIsAnError, unlike decrypt.
//
// Writing a credential in the clear because encryption was unavailable is worse
// than refusing the save, so the two halves fail in opposite directions on
// purpose.
func TestEncryptFailureIsAnError(t *testing.T) {
	s := &Server{}
	if _, err := s.encryptSetting("secret"); err == nil {
		t.Error("encrypting without a store succeeded -- a credential would be " +
			"stored in the clear")
	}
}

// TestAnAbsentSwitchMeansOff, against a REAL settings store.
//
// The earlier tests use a nil store, which short-circuits before the setting is
// ever read — so a mutation making an ABSENT key mean "enabled" survived them.
// This one writes a settings file that simply has no `userNotifyEnabled`, which
// is what every install that upgraded looks like.
func TestAnAbsentSwitchMeansOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		json string
		want bool
	}{
		{"absent entirely", `{"authMode":"modern"}`, false},
		{"explicitly false", `{"userNotifyEnabled":false}`, false},
		{"explicitly true", `{"userNotifyEnabled":true}`, true},
		// A non-boolean is not a yes. A settings file hand-edited to "true" is
		// not the same as the switch being on, and reading it as truthy would
		// enable the widest thing in this file on a typo.
		{"the string true", `{"userNotifyEnabled":"true"}`, false},
		{"the number one", `{"userNotifyEnabled":1}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := settingsServer(t, tc.json)
			if got := s.userNotifyEnabled(); got != tc.want {
				t.Errorf("userNotifyEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestDecryptFailureReadsAsEmptyWithARealStore.
//
// A nil store returns early, so the error branch was never reached. This feeds
// the real decryptor something that is not ciphertext.
func TestDecryptFailureReadsAsEmptyWithARealStore(t *testing.T) {
	s := settingsServer(t, `{"userNotifyEnabled":true}`)
	for _, bad := range []string{
		"not-base64-at-all!!",
		"aGVsbG8=",       // valid base64, not a valid envelope
		"AAAAAAAAAAAAAA", // too short to hold an IV and a tag
	} {
		if got := s.decryptSetting(bad); got != "" {
			t.Errorf("decrypting %q returned %q -- an unreadable credential must "+
				"cost that channel, not leak whatever came back", bad, got)
		}
	}
	// ...and a value this store encrypted comes back intact, or the check above
	// would pass against a decryptor that always failed.
	enc, err := s.encryptSetting("a-real-token")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.decryptSetting(enc); got != "a-real-token" {
		t.Errorf("round trip gave %q -- this test would otherwise prove nothing", got)
	}
}
