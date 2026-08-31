package server

// `POST /api/routers/test` — try a connection without saving it.
//
// ── THE MOST DANGEROUS ROUTE IN THE APP, AND THE REASON IS NOT THE DIAL ─────
//
// It may reuse a STORED router password. The live comment states the attack: "A
// bare 'look it up by id' turns this route into a credential oracle: submit a
// stored id with an attacker-chosen host and the server posts the saved password
// to it."
//
// So the stored secret is reused only when `routers.SameEndpoint` says every
// field deciding WHERE it goes and HOW it travels is unchanged. Being a global
// administrator gates the route and is explicitly NOT sufficient: "the point is
// to stop a stored secret reaching a destination nobody stored it against,
// INCLUDING at the hands of an admin."
//
// ── WHY THE STORED PASSWORD IS REUSED AT ALL ────────────────────────────────
//
// Not convenience. The modal blanks the password on edit and its placeholder
// says "leave blank to keep current", while Save refuses to write until a test
// passes — so with no reuse that promise was false and NO field of an existing
// device could be saved without retyping the credential. Reported on #117 as
// sites not being removable; it was never about sites.
//
// NAMED `routers_conntest.go`, not `routers_test_api.go`: a reader skimming the
// directory should not have to work out whether this is production code.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/routers"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

// The two bounds the live route sets, and they are NOT the same clock.
//
//	testConnTimeout  the `setTimeout(..., 9000)` around the whole attempt
//	testDialTimeout  the driver's own `writeTimeoutMs: 8000`
//
// The outer one is a second longer so the DIAL is what normally gives up, and
// its message — which names the port and the address — is what gets classified.
// Reversing them would make every failure read "Connection timed out after 8
// seconds" and lose the reason.
// A VAR, not a const, and only because a test shortens it. Nine seconds of real
// waiting per case is the kind of cost that gets a test deleted, and without one
// the wall-clock arm is untested — a mutation replacing its verbatim message with
// a classified one survived a full suite.
//
// The seam does not bypass the path it stands in for: the shortened value goes
// through the same `select`, against a listener that accepts and then says
// nothing, so the timeout genuinely wins a race it could otherwise lose.
var testConnTimeout = 9 * time.Second

const testDialTimeout = 8 * time.Second

// testTimeoutMessage is what the live route sends when ITS timer wins, and it is
// sent VERBATIM rather than classified — `done(false, 'Connection timed out
// after 8 seconds')` never reaches the connectionError handler. The eight is
// the driver's bound, not this timer's nine; reproduced as written.
const testTimeoutMessage = "Connection timed out after 8 seconds"

func (s *Server) registerRouterTest(mux *http.ServeMux) {
	// TEN A MINUTE, matching `_testConnLimiter`, and far tighter than the 60 on
	// the write routes. Each request opens a TCP connection to a
	// caller-specified host, so an ungated version is a port scanner wearing this
	// server's source address.
	lim := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("POST /api/routers/test", lim(s.routerConnTest))
}

func (s *Server) routerConnTest(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.routerWriteSession(w, r)
	if !ok {
		return
	}
	// GLOBAL ADMINISTRATOR, the same helper `routerCreate` uses and for the same
	// reason: there is no router to scope to — the device may not exist yet — so
	// `router:manage` would have nothing to gate on, and granting it fleet-wide
	// is the same as granting administration.
	if !s.mayManagePrincipals(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}

	// DECODED AS `any` FOR THREE FIELDS, because their COERCION is part of the
	// rule rather than a detail of parsing: `port` may be a numeric string, and
	// `tls`/`tlsInsecure` may be the STRINGS "true"/"false" with results that do
	// not match their spelling. See internal/routers/endpoint.go.
	var body struct {
		ID          string `json:"id"`
		Host        string `json:"host"`
		Port        any    `json:"port"`
		Username    string `json:"username"`
		Password    string `json:"password"`
		TLS         any    `json:"tls"`
		TLSInsecure any    `json:"tlsInsecure"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body)
	if strings.TrimSpace(body.Host) == "" {
		writeJSONErr(w, http.StatusBadRequest, "host is required")
		return
	}

	submitted := routers.Endpoint{
		Host: strings.TrimSpace(body.Host), Port: body.Port,
		Username: testUsername(body.Username), TLS: body.TLS, Insecure: body.TLSInsecure,
	}

	password := s.testPassword(body.Password, body.ID, &submitted)

	cfg := routeros.Config{
		Host:     submitted.Host,
		Username: submitted.Username,
		Password: password,
		// THE SAME COERCIONS SameEndpoint COMPARED. Re-deriving them here would
		// let the match approve one destination while the dial went to another,
		// silently — see the note on the exported helpers.
		Port:        routers.EndpointPort(submitted.Port),
		TLS:         routers.EndpointTLS(submitted.TLS),
		InsecureTLS: routers.EndpointInsecure(submitted.Insecure),
		DialTimeout: testDialTimeout,
	}

	board, err, timedOut := s.tryRouter(cfg)
	switch {
	case timedOut:
		// ALWAYS 200, here and below. The live route answers `{ok:false, error}`
		// for every failure — the request succeeded, the connection did not, and
		// the modal renders the reason inline. A 4xx or 5xx would make the
		// browser's error handling swallow the text, which is the whole value of
		// the button.
		writeJSON(w, map[string]any{"ok": false, "error": testTimeoutMessage})
	case err != nil:
		// `cfg.TLS`, THE COERCED FLAG — the one the connection was actually
		// attempted with. The live branch read the raw request field until
		// f4ade9e and named the wrong RouterOS service in both directions; see
		// the note on routeros.TestConnReason.
		writeJSON(w, map[string]any{"ok": false, "error": testFailureReason(err, cfg.TLS)})
	default:
		writeJSON(w, map[string]any{"ok": true, "boardName": board})
	}
}

// testPassword decides WHICH SECRET the attempt is made with.
//
// ── EXTRACTED BECAUSE THE ROUTE CANNOT TEST IT ──────────────────────────────
//
// Inline, the mask arm is unreachable from any route test: whether the mask is
// blanked or sent as a literal, the observable is the same failed connection, so
// a mutation making the route log in with eight bullet characters survived a
// full suite. Three lines with a security consequence and no way to see it.
//
// ── THE MASK IS NOT A PASSWORD ──────────────────────────────────────────────
//
// The payload sends it wherever one is stored — that is what the field looks
// like in the form the operator is editing. Taking it literally attempts a login
// with the mask itself, which fails as "check username and password" and sends
// the operator hunting a credential that was never wrong.
//
// ── AND AN EMPTY ONE FALLS THROUGH TO THE STORED SECRET ─────────────────────
//
// Only then, and only for the endpoint it was stored against. See
// storedPasswordFor, which is where the whole security property of this route
// lives.
func (s *Server) testPassword(submitted, id string, e *routers.Endpoint) string {
	if submitted == store.Mask {
		submitted = ""
	}
	if submitted != "" || id == "" {
		return submitted
	}
	return s.storedPasswordFor(id, e)
}

// testFailureReason is the sentence a failed attempt answers with.
//
// ── ALSO EXTRACTED FOR A REASON THE ROUTE HID ───────────────────────────────
//
// Inline, the UNCLASSIFIED arm needs a dial that fails in a way the classifier
// does not recognise, which no test can arrange against a closed port — so a
// mutation sending the raw driver message to the browser survived. That message
// is the one that names the install's data directory.
//
// The live code answers `sanitizeErr(e)` there and falls back to a generic
// sentence when redaction leaves nothing, which is the third case below.
func testFailureReason(err error, tls bool) string {
	if reason, matched := routeros.TestConnReason(err, tls); matched {
		return reason
	}
	if reason := safe.Message(err.Error()); reason != "" {
		return reason
	}
	return "Connection failed"
}

// testUsername is `String(body.username || 'admin').trim()`.
func testUsername(v string) string {
	if strings.TrimSpace(v) == "" {
		return "admin"
	}
	return strings.TrimSpace(v)
}

// storedPasswordFor returns a stored router's password ONLY when the submitted
// endpoint is the one it was stored against.
//
// Every early return is a refusal, and each is silent on purpose: telling the
// caller WHY no password was reused would confirm which ids exist and which
// endpoints they name, which is the oracle in a quieter form. The connection
// simply fails to authenticate, exactly as it would with a wrong password.
func (s *Server) storedPasswordFor(id string, submitted *routers.Endpoint) string {
	if s.store == nil {
		return ""
	}
	all, _ := s.store.Routers()
	for _, rec := range all {
		if rec.ID != id {
			continue
		}
		if rec.Encrypted == "" {
			return ""
		}
		stored := routers.Endpoint{
			Host: rec.Host, Port: rec.Port, Username: rec.Username,
			TLS: rec.TLS, Insecure: rec.TLSInsecure,
		}
		if !routers.SameEndpoint(&stored, submitted) {
			return ""
		}
		// DECRYPTED ONLY AFTER THE MATCH, which is a departure from the live
		// side: `Routers.loadAll()` decrypts every password eagerly when it fills
		// its cache, so the plaintext is already in hand there before the
		// comparison happens. Same observable answer, one less place a secret
		// exists — and the ordering here is what makes a log line added between
		// these two statements harmless rather than a leak.
		pw, err := s.store.Decrypt(rec.Encrypted)
		if err != nil {
			return ""
		}
		return pw
	}
	return ""
}

// tryRouter dials, reads the board name, and always closes.
//
// The board name is BEST EFFORT: the live route answers `ok:true` with an empty
// name when `/system/resource` fails, because the question asked was whether the
// credentials work and they demonstrably did.
//
// The third return says the WALL CLOCK won rather than the dial, because those
// two answer differently — see testTimeoutMessage.
func (s *Server) tryRouter(cfg routeros.Config) (board string, err error, timedOut bool) {
	type result struct {
		board string
		err   error
	}
	// BUFFERED, so the goroutine's send never blocks on a reader that has already
	// given up below. Unbuffered, every timed-out attempt would leak a goroutine
	// holding an open connection to a caller-named host.
	done := make(chan result, 1)
	go func() {
		c, derr := routeros.Dial(cfg)
		if derr != nil {
			done <- result{"", derr}
			return
		}
		defer c.Close()
		reply, rerr := c.Do(routeros.Cmd{
			Path:    "/system/resource/print",
			Args:    []string{"=.proplist=board-name,version"},
			Timeout: testDialTimeout,
		})
		if rerr != nil || len(reply) == 0 {
			done <- result{"", nil}
			return
		}
		// `r['board-name'] || r.platform || ''` — the second is what a CHR or an
		// x86 build answers with, having no board.
		name := reply[0]["board-name"]
		if name == "" {
			name = reply[0]["platform"]
		}
		done <- result{name, nil}
	}()

	select {
	case res := <-done:
		return res.board, res.err, false
	case <-time.After(testConnTimeout):
		// The goroutine is left to finish and close its own connection. It cannot
		// hang for ever: both the dial and the command carry their own timeout,
		// each shorter than this one.
		return "", nil, true
	}
}
