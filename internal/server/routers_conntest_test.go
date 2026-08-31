package server

// `POST /api/routers/test`, through the REAL mux.
//
// ── WHAT IS ACTUALLY BEING TESTED ───────────────────────────────────────────
//
// Not the dial. The dial is a socket, and the sentences it produces are pinned
// against the live classifier in `internal/routeros/testreason_test.go`.
//
// What is tested here is WHO GETS THE STORED PASSWORD. The live comment calls
// this route a credential oracle if the lookup is done by id alone: submit a
// stored id with an attacker-chosen host, and the server posts the saved
// password to that host. Every refusal below is one way that attack is spelled,
// and each is silent by design — the caller sees a failed login, not a reason.
//
// The one connection actually attempted goes to a CLOSED PORT ON LOOPBACK, so
// the end-to-end path produces a real "connection refused" from the real client
// with no network dependency and no router.

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/routers"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

// connTestServer is `routersServer` plus an ENCRYPTED password on r1, which the
// shared fixture deliberately lacks — every test there is about fields, and this
// file is about the secret.
//
// The session's AuthMode decides the gate: `mayManagePrincipals` returns true
// for "none" and otherwise asks RBAC, and `routerRbacDDL` grants alice nothing
// global. So "none" is the administrator here and "" is the non-administrator,
// which is what the 403 test relies on.
func connTestServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	s, mux, dir := routersServer(t, sess, "")
	s.registerRouterTest(mux)

	enc, err := s.store.Encrypt("stored-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Rewritten rather than added to the fixture constant: `routersServer` is
	// shared with routers_api_test.go, and a password appearing there would
	// change what those tests are looking at.
	recs := []map[string]any{
		{"id": "r1", "label": "One", "host": "198.51.100.1", "port": 8729,
			"username": "u", "password": enc, "tls": true, "tlsInsecure": false},
	}
	b, _ := json.Marshal(recs)
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return s, mux
}

func connTestPost(mux *http.ServeMux, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/api/routers/test", strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// ── The credential decision ─────────────────────────────────────────────────

// TestTheStoredPasswordIsReusedOnlyForItsOwnEndpoint is the security property.
//
// Each refusal names a field that decides WHERE the secret goes or HOW it
// travels. The two that match change neither: a stray space, and a port written
// as a numeric string.
func TestTheStoredPasswordIsReusedOnlyForItsOwnEndpoint(t *testing.T) {
	s, _ := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})

	stored := func(mut func(*routers.Endpoint)) string {
		e := routers.Endpoint{Host: "198.51.100.1", Port: 8729, Username: "u",
			TLS: true, Insecure: false}
		if mut != nil {
			mut(&e)
		}
		return s.storedPasswordFor("r1", &e)
	}

	if got := stored(nil); got != "stored-secret" {
		t.Fatalf("the unchanged endpoint got %q; without reuse no field of an existing "+
			"device can be saved, because Save refuses until a test passes and the modal "+
			"blanks the password", got)
	}

	for name, mut := range map[string]func(*routers.Endpoint){
		"a different host":     func(e *routers.Endpoint) { e.Host = "198.51.100.9" },
		"a subdomain":          func(e *routers.Endpoint) { e.Host = "evil.198.51.100.1" },
		"a different port":     func(e *routers.Endpoint) { e.Port = 8728 },
		"a different user":     func(e *routers.Endpoint) { e.Username = "root" },
		"the username's case":  func(e *routers.Endpoint) { e.Username = "U" },
		"TLS turned off":       func(e *routers.Endpoint) { e.TLS = false },
		"cert checking off":    func(e *routers.Endpoint) { e.Insecure = true },
		"an empty host":        func(e *routers.Endpoint) { e.Host = "" },
		"TLS off as a string":  func(e *routers.Endpoint) { e.TLS = "false" },
		"cert off as a string": func(e *routers.Endpoint) { e.Insecure = "true" },
	} {
		if got := stored(mut); got != "" {
			t.Errorf("%s STILL GOT THE PASSWORD (%q). That is the credential oracle: "+
				"the stored secret reached a destination nobody stored it against", name, got)
		}
	}

	// The ones that must NOT cost an admin a retype.
	for name, mut := range map[string]func(*routers.Endpoint){
		"surrounding space in the host": func(e *routers.Endpoint) { e.Host = " 198.51.100.1 " },
		"the port as a numeric string":  func(e *routers.Endpoint) { e.Port = "8729" },
	} {
		if got := stored(mut); got != "stored-secret" {
			t.Errorf("%s was refused; the field did not change, so the admin is made to "+
				"retype a password for nothing", name)
		}
	}
}

// TestAnUnknownIdYieldsNothingAndSaysNothing — the id itself must not be an
// oracle either. A response distinguishing "no such router" from "that is not
// its endpoint" would enumerate the fleet for an attacker who already got past
// the admin gate, which is the threat model this route is written for.
func TestAnUnknownIdYieldsNothingAndSaysNothing(t *testing.T) {
	s, _ := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})
	e := routers.Endpoint{Host: "198.51.100.1", Port: 8729, Username: "u", TLS: true}
	if got := s.storedPasswordFor("no-such-router", &e); got != "" {
		t.Errorf("an unknown id returned %q", got)
	}
	if got := s.storedPasswordFor("", &e); got != "" {
		t.Errorf("an empty id returned %q", got)
	}
}

// ── The route ───────────────────────────────────────────────────────────────

func TestTheTestRouteRequiresAnAdministrator(t *testing.T) {
	// AuthMode "" sends `mayManagePrincipals` to RBAC, and `routerRbacDDL` grants
	// alice only `devices:write` on r1 — never `system:principals`, which is
	// global-only. There is no router to scope to here (the device may not exist
	// yet), so anything less than global admin grants administration by another
	// name.
	_, mux := connTestServer(t, &Session{Username: "alice"})

	if w := connTestPost(mux, `{"host":"127.0.0.1"}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("no cookie: got %d, want 401", w.Code)
	}
	if w := connTestPost(mux, `{"host":"127.0.0.1"}`, "mikrodash_sid=tok"); w.Code != http.StatusForbidden {
		t.Errorf("a non-admin: got %d, want 403", w.Code)
	}
}

// TestAMissingHostIsA400 — and it is checked BEFORE anything dials, so a body
// with no host cannot open a socket.
func TestAMissingHostIsA400(t *testing.T) {
	_, mux := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})

	for _, body := range []string{`{}`, `{"host":""}`, `{"host":"   "}`, `not json`} {
		w := connTestPost(mux, body, "mikrodash_sid=tok")
		if w.Code != http.StatusBadRequest {
			t.Errorf("body %s: got %d, want 400", body, w.Code)
		}
		if !strings.Contains(w.Body.String(), "host is required") {
			t.Errorf("body %s: got %q", body, w.Body.String())
		}
	}
}

// TestARefusedConnectionAnswers200WithTheSentence — the whole end-to-end path,
// against a port that is genuinely closed.
//
// ── 200, NOT 502, AND THAT IS THE POINT ─────────────────────────────────────
//
// The request succeeded; the connection did not. The live route answers
// `{ok:false, error}` at 200 for every failure because the modal renders that
// text inline — a 4xx or 5xx makes the browser's error handling swallow it, and
// the text is the entire value of the button.
func TestARefusedConnectionAnswers200WithTheSentence(t *testing.T) {
	_, mux := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})

	// A port nothing is listening on: opened, its number taken, then closed.
	// Hard-coding one would pass until the day something happened to be there.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().(*net.TCPAddr)
	_ = l.Close()

	body, _ := json.Marshal(map[string]any{
		"host": "127.0.0.1", "port": addr.Port, "username": "u", "password": "p",
		"tls": false,
	})
	w := connTestPost(mux, string(body), "mikrodash_sid=tok")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200 — a failed connection is a successful request", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != false {
		t.Errorf("ok = %v, want false", got["ok"])
	}
	if got["error"] != "Connection refused — check host and port" {
		t.Errorf("error = %q; the classifier did not see a real refusal", got["error"])
	}
}

// TestTheMaskIsNotTreatedAsAPassword — now a BEHAVIOURAL test, because the
// decision was extracted for exactly that reason.
//
// Inline in the route it was unreachable: whether the mask is blanked or sent as
// a literal, the observable is the same failed connection, and a mutation making
// the route log in with eight bullet characters survived the whole suite. This
// file used to check the ORDER of two statements instead, which is the weaker
// thing you settle for when the real one is out of reach.
func TestTheMaskIsNotTreatedAsAPassword(t *testing.T) {
	if store.Mask == "" {
		t.Fatal("store.Mask is empty; this test would pass vacuously")
	}
	s, _ := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})
	e := routers.Endpoint{Host: "198.51.100.1", Port: 8729, Username: "u",
		TLS: true, Insecure: false}

	// THE MASK, against the endpoint it was stored for: blanked, then the stored
	// secret found. Taking the mask literally returns the mask.
	if got := s.testPassword(store.Mask, "r1", &e); got != "stored-secret" {
		t.Errorf("the mask gave %q; the operator would be told their credentials are wrong "+
			"while the app logs in with eight bullet characters", got)
	}
	// THE MASK, against an endpoint that changed: blanked, and nothing replaces
	// it. Anything else is the credential oracle wearing a mask.
	moved := e
	moved.Host = "198.51.100.9"
	if got := s.testPassword(store.Mask, "r1", &moved); got != "" {
		t.Errorf("the mask against a changed host gave %q", got)
	}
	// A REAL PASSWORD is used as typed and never triggers the lookup, even when
	// the id names a router whose stored secret would have matched.
	if got := s.testPassword("typed-by-hand", "r1", &e); got != "typed-by-hand" {
		t.Errorf("a typed password gave %q", got)
	}
	// NO PASSWORD AND NO ID is empty, not a lookup over the whole file.
	if got := s.testPassword("", "", &e); got != "" {
		t.Errorf("no password and no id gave %q", got)
	}
}

// TestAnUnclassifiedErrorIsRedactedBeforeItReachesTheBrowser.
//
// The other decision extracted from the route, and for the same reason: reaching
// this arm needs a dial that fails in a way the classifier does not recognise,
// which no test can arrange against a closed port. A mutation sending the raw
// driver message to the browser survived the whole suite.
//
// The message below is the one that matters. A SQLite failure names the
// install's data directory, and the live app never sends one — every one of its
// write-error payloads reads `sanitizeErr(e)`.
func TestAnUnclassifiedErrorIsRedactedBeforeItReachesTheBrowser(t *testing.T) {
	got := testFailureReason(errors.New("unable to open database file: /data/mikrodash.db"), true)
	if strings.Contains(got, "/data/mikrodash.db") {
		t.Errorf("got %q — the path reached the browser", got)
	}
	if !strings.Contains(got, "[path]") {
		t.Errorf("got %q; safe.Message should have substituted [path]", got)
	}
	// A CLASSIFIED error is passed through intact: redaction is for the messages
	// this server did not write, and running it over its own sentences would eat
	// the host and port an operator is being asked to check.
	if got := testFailureReason(errors.New("dial tcp 10.0.0.2:8729: connect: connection refused"), true); got !=
		"Connection refused — check host and port" {
		t.Errorf("a classified error came back as %q", got)
	}
	// AND WHEN REDACTION LEAVES NOTHING, the generic sentence. `sanitizeErr`
	// returns an empty string for an empty message, and `done(false, error)`
	// falls back to `error || 'Connection failed'`.
	if got := testFailureReason(errors.New(""), true); got != "Connection failed" {
		t.Errorf("an empty error came back as %q, want the live fallback", got)
	}
}

// TestTheDialAndTheMatchUseOneCoercion.
//
// The route decides two things from one request: whether the stored password may
// be reused, and where to dial. If those coerced the fields independently they
// could disagree — compare port 8729 and dial 0, or compare TLS on and dial it
// off — and the disagreement would be SILENT, because each half is individually
// correct. `internal/routers` exports the three coercions for exactly this.
func TestTheDialAndTheMatchUseOneCoercion(t *testing.T) {
	src, err := os.ReadFile("routers_conntest.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	for _, want := range []string{
		"routers.EndpointPort(submitted.Port)",
		"routers.EndpointTLS(submitted.TLS)",
		"routers.EndpointInsecure(submitted.Insecure)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the dial config no longer calls %s. Coercing the field here instead "+
				"lets SameEndpoint approve one destination while the dial goes to another.", want)
		}
	}
	// And the config is built from `submitted`, the same struct the match was
	// given — not from `body`, which would re-read the raw request.
	if strings.Contains(body, "routers.EndpointPort(body.Port)") ||
		strings.Contains(body, "routers.EndpointTLS(body.TLS)") {
		t.Error("the dial config reads the raw request rather than the matched endpoint")
	}
}

// TestTheWallClockTimeoutSendsItsOwnSentence.
//
// The live route has TWO clocks and they answer differently: the driver's 8s
// bound produces an error that gets CLASSIFIED, and the route's own 9s
// `setTimeout` calls `done(false, 'Connection timed out after 8 seconds')`
// directly — that string never reaches the connectionError handler at all.
//
// Untested, the two collapse and nobody notices: both say something about a
// timeout. A mutation replacing the verbatim message with a classified one
// survived the rest of this file.
//
// The listener ACCEPTS AND THEN SAYS NOTHING, which is what makes the race
// deterministic rather than hopeful: the dial completes, the login read blocks
// for ever, and the only thing that can finish the request is the wall clock.
func TestTheWallClockTimeoutSendsItsOwnSentence(t *testing.T) {
	_, mux := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			// Held open, unread and unwritten, until the test ends.
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	old := testConnTimeout
	testConnTimeout = 50 * time.Millisecond
	t.Cleanup(func() { testConnTimeout = old })

	addr := l.Addr().(*net.TCPAddr)
	body, _ := json.Marshal(map[string]any{
		"host": "127.0.0.1", "port": addr.Port, "username": "u", "password": "p", "tls": false,
	})
	w := connTestPost(mux, string(body), "mikrodash_sid=tok")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] != testTimeoutMessage {
		t.Errorf("error = %q, want the verbatim %q. The live route sends this string "+
			"directly rather than classifying anything, and the eight is the DRIVER's "+
			"bound rather than this timer's nine", got["error"], testTimeoutMessage)
	}
}

// TestTheUsernameDefaultsToAdmin — `String(body.username || 'admin').trim()`.
//
// Both halves matter and neither is obvious from the call site: an absent
// username must not dial as the empty string (RouterOS answers a confusing
// "cannot log in" rather than "you sent no user"), and a username arriving with
// a stray space must be trimmed, because SameEndpoint trims before comparing and
// an untrimmed dial would go out as a different account from the one the match
// approved.
func TestTheUsernameDefaultsToAdmin(t *testing.T) {
	for in, want := range map[string]string{
		"":        "admin",
		"   ":     "admin",
		"\t\n":    "admin",
		"bob":     "bob",
		"  bob  ": "bob",
		"Bob":     "Bob", // NOT lowercased: RouterOS logins are case-sensitive
	} {
		if got := testUsername(in); got != want {
			t.Errorf("testUsername(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestThePasswordIsDecryptedOnlyAfterTheMatch.
//
// A SOURCE check, and honestly so: decrypting early and discarding the result
// changes nothing observable, so a behavioural test cannot distinguish the two —
// a mutation doing exactly that survived this whole file, correctly.
//
// The ordering is still worth holding. It is what makes a log line, a metric or
// an error wrapper added between those two statements harmless instead of a
// credential leak, and that is a property of the SHAPE rather than of any run.
func TestThePasswordIsDecryptedOnlyAfterTheMatch(t *testing.T) {
	src, err := os.ReadFile("routers_conntest.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	match := strings.Index(body, "routers.SameEndpoint(&stored, submitted)")
	dec := strings.Index(body, "s.store.Decrypt(rec.Encrypted)")
	if match < 0 || dec < 0 {
		t.Fatal("the endpoint match or the decrypt is gone from storedPasswordFor")
	}
	if dec < match {
		t.Error("the stored password is decrypted BEFORE the endpoint match. Nothing " +
			"observable changes today, which is the problem: the next line added between " +
			"them is a plaintext credential in a log")
	}
	if strings.Count(body, "s.store.Decrypt(") != 1 {
		t.Error("storedPasswordFor decrypts in more than one place; only the post-match " +
			"call may exist")
	}
}

// TestTheCoercedTlsFlagReachesTheClassifier — end to end, over a real socket.
//
// ── WHY `"true"` AS A STRING IS THE WHOLE POINT ─────────────────────────────
//
// It is the one input where the coerced flag and the raw request field disagree
// in a way this route can observe: `EndpointTLS("true")` is TRUE, and a Go
// comparison of the raw `any` against `true` is FALSE. So the sentence differs
// depending on which one the route hands the classifier, and a mutation swapping
// them survived every other test in this file.
//
// That distinction is defect 1 from f4ade9e, in the port rather than upstream —
// the live fix moved the branch onto `testTls`, and nothing here would have
// noticed the port failing to follow.
//
// The listener accepts, writes a few PLAINTEXT bytes and closes, which is what a
// RouterOS box with `api` on 8728 and no `api-ssl` does to a TLS client: Go says
// "first record does not look like a TLS handshake", which is precisely the
// condition the branch's first arm exists to explain.
func TestTheCoercedTlsFlagReachesTheClassifier(t *testing.T) {
	_, mux := connTestServer(t, &Session{Username: "alice", AuthMode: "none"})

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("!done\n"))
			_ = c.Close()
		}
	}()

	addr := l.Addr().(*net.TCPAddr)
	body, _ := json.Marshal(map[string]any{
		"host": "127.0.0.1", "port": addr.Port, "username": "u", "password": "p",
		"tls": "true", // the string, not the boolean
	})
	w := connTestPost(mux, string(body), "mikrodash_sid=tok")
	if w.Code != http.StatusOK {
		t.Fatalf("got %d, want 200", w.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	const want = "TLS handshake failed — check that RouterOS api-ssl is enabled"
	if got["error"] != want {
		t.Errorf("error = %q, want %q.\nThe attempt used TLS — EndpointTLS(\"true\") is true — "+
			"so the sentence must name api-ssl. Naming the plain api service sends the "+
			"operator to change a setting that was not the problem.", got["error"], want)
	}
}

// TestClassifiedSentencesAreNOTRedacted — and the reason is sharper than
// "redaction would be harmless here".
//
// A mutation running `safe.Message` over the CLASSIFIED sentences survived the
// suite. Written to explain WHY it was harmless, this test discovered it is not:
//
//	"Host not found — check router host/IP"
//	"Host not found — check router host[path]"
//
// `safe.Message`'s path pattern is `/[^\s\'"]{2,}`, and `/IP` is a slash
// followed by two non-space characters. The redactor cannot tell an operator's
// instruction from a filesystem path, and it eats the half of the sentence that
// says what to check.
//
// The live app has the same split and gets it right for the same reason:
// `done(false, reason === msg ? sanitizeErr(e) : reason)` sanitises ONLY the
// unmatched arm. Redaction is for messages this server did not write.
func TestClassifiedSentencesAreNotRedacted(t *testing.T) {
	b, err := os.ReadFile("../../testdata/test-reason-cases.json")
	if err != nil {
		t.Fatalf("corpus missing: %v — run tools/test-reason-cases.js", err)
	}
	var doc struct {
		Cases []struct {
			Condition string `json:"condition"`
			GoMessage string `json:"goMessage"`
			TLS       bool   `json:"tls"`
			Reason    string `json:"reason"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}

	// THE ROUTE'S OWN FUNCTION, over every case with a Go input, so this is the
	// path the browser gets rather than a restatement of the classifier.
	fed, mangled := 0, 0
	for _, c := range doc.Cases {
		if c.GoMessage == "" || c.Reason == "" {
			continue
		}
		fed++
		if got := testFailureReason(errors.New(c.GoMessage), c.TLS); got != c.Reason {
			t.Errorf("%s: got %q, want %q", c.Condition, got, c.Reason)
		}
		if safe.Message(c.Reason) != c.Reason {
			mangled++
		}
	}
	if fed < 6 {
		t.Errorf("only %d cases had a Go message; this proved almost nothing", fed)
	}
	// AT LEAST ONE SENTENCE IS DESTROYED BY REDACTION, which is what makes the
	// check above a real property rather than a coincidence. If this ever reaches
	// zero the sentences were reworded, and somebody should decide on purpose
	// whether the split still matters — not discover later that it stopped.
	if mangled == 0 {
		t.Error("no classified sentence is changed by safe.Message any more. The rule still " +
			"holds — these are our words, not a driver's — but the evidence for it is gone, " +
			"so re-read this test before trusting it.")
	}
}
