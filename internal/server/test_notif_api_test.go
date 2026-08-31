package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The admin test-notification route.
//
// NOTHING HERE SENDS. Every case either stops before the transport (a refusal, a
// missing credential) or reaches a channel whose precondition fails — which is
// the honest way to test a send path from a suite that must never actually
// deliver. The MERGE, which is the part with the decisions in it, is pinned
// against the live expression in `internal/notify/admintest_test.go`.

func testNotifServer(t *testing.T, sess *Session) (*Server, *http.ServeMux) {
	t.Helper()
	s, mux, _ := usersWriteServer(t, sess, seedUsersJSON)
	s.registerTestNotification(mux)
	return s, mux
}

func TestTestNotificationRefusals(t *testing.T) {
	for _, c := range []struct {
		why  string
		sess *Session
		body string
		want int
		msg  string
	}{
		{"no channel", &Session{AuthMode: "none", Username: "admin"}, `{}`,
			400, "channel is required"},
		{"an empty channel", &Session{AuthMode: "none", Username: "admin"},
			`{"channel":""}`, 400, "channel is required"},
		{"a channel that is not a string", &Session{AuthMode: "none", Username: "admin"},
			`{"channel":42}`, 400, "channel is required"},
		{"a malformed body", &Session{AuthMode: "none", Username: "admin"},
			`{not json`, 400, "malformed body"},
		{"not an admin", &Session{AuthMode: "modern", Username: "nobody"},
			`{"channel":"telegram"}`, 403, "Administrator access required"},
	} {
		t.Run(c.why, func(t *testing.T) {
			_, mux := testNotifServer(t, c.sess)
			w := doJSON(mux, "POST", "/api/settings/test-notification", c.body, authed)
			if w.Code != c.want {
				t.Fatalf("status %d, want %d — %s", w.Code, c.want, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), c.msg) {
				t.Errorf("body is %s, want one containing %q", w.Body.String(), c.msg)
			}
		})
	}
}

func TestTestNotificationRequiresASession(t *testing.T) {
	_, mux := testNotifServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/settings/test-notification", `{"channel":"telegram"}`, "")
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401 — %s", w.Code, w.Body.String())
	}
}

// An UNCONFIGURED channel is refused by `Precondition` before any request is
// made — which is what lets this suite exercise the send path without sending.
func TestAnUnconfiguredChannelIsRefusedBeforeSending(t *testing.T) {
	for _, ch := range []string{"telegram", "pushbullet", "smtp", "ntfy"} {
		t.Run(ch, func(t *testing.T) {
			_, mux := testNotifServer(t, &Session{AuthMode: "none", Username: "admin"})
			w := doJSON(mux, "POST", "/api/settings/test-notification",
				`{"channel":"`+ch+`"}`, authed)
			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status %d, want 500 — %s", w.Code, w.Body.String())
			}
			var got struct {
				OK    bool   `json:"ok"`
				Error string `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.OK {
				t.Error("ok:true for a channel with no credentials")
			}
			if got.Error == "" {
				t.Error("a refusal with no message tells the operator nothing")
			}
		})
	}
}

func TestAnUnknownChannelIsRefused(t *testing.T) {
	_, mux := testNotifServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/settings/test-notification",
		`{"channel":"carrier-pigeon"}`, authed)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"ok":true`) {
		t.Error("an unknown channel reported success")
	}
}

// THE LIMITER IS ITS OWN. Ten a minute, matching `_testNotifLimiter` — this is
// the one settings route that makes an outbound connection to an address the
// caller supplied.
func TestTheTestRouteIsRateLimited(t *testing.T) {
	_, mux := testNotifServer(t, &Session{AuthMode: "none", Username: "admin"})
	limited := false
	for i := 0; i < 12; i++ {
		w := doJSON(mux, "POST", "/api/settings/test-notification", `{}`, authed)
		if w.Code == http.StatusTooManyRequests {
			limited = true
			break
		}
	}
	if !limited {
		t.Error("twelve requests in a row were all accepted — the limiter is not wired")
	}
}

// The response and the log must never carry a credential or an address. The
// route sanitises both; this pins the response half, which is the one an
// unprivileged reader could reach if the admin gate ever regressed.
//
// ── THE HOST IS `.invalid`, DELIBERATELY ───────────────────────────────────
//
// The first version of this test used 10.11.12.13, and it took TEN SECONDS —
// because the route did what it is supposed to do and tried to connect. That is
// an RFC1918 address: on a developer's machine it is unrouted, and on the
// operator's it could be a real host on their LAN. A test suite must not send a
// packet to an address that might answer.
//
// `.invalid` is reserved by RFC 2606 and never resolves, so this fails at DNS
// with no packet leaving the machine — and the error still names the host, which
// is what the sanitiser has to redact.
func TestTheFailureBodyIsSanitised(t *testing.T) {
	_, mux := testNotifServer(t, &Session{AuthMode: "none", Username: "admin"})
	w := doJSON(mux, "POST", "/api/settings/test-notification",
		`{"channel":"ntfy","ntfyUrl":"http://ntfy-host.invalid:8080/secret-topic","ntfyToken":"tk_synthetic"}`,
		authed)
	body := w.Body.String()
	// THE TOKEN AND THE TOPIC. The topic goes with the path, which `safe.Message`
	// replaces wholesale; the token never reaches the error at all.
	for _, leak := range []string{"tk_synthetic", "secret-topic"} {
		if strings.Contains(body, leak) {
			t.Errorf("the response carries %q: %s", leak, body)
		}
	}
	// ── THE HOSTNAME, CLOSED 2026-08-29 ─────────────────────────────────────
	//
	// This block used to assert the hostname was STILL PRESENT — a measured gap,
	// recorded rather than asserted away, so that a sanitiser learning about
	// hostnames would fail here and force the note to be deleted rather than left
	// standing quietly wrong. That is exactly what happened: reported from this
	// port, fixed upstream in `51aac86`, and `safe.Message` gained the rule.
	//
	// The assertion is now the opposite, and it is the one that matters. The per-
	// user route is deliberately not admin-gated and lets the caller choose an
	// ntfy URL, so a readable hostname there is a name oracle for the server's
	// DNS view: resolvable and unresolvable names give distinguishable errors.
	if strings.Contains(body, "ntfy-host.invalid") {
		t.Errorf("the hostname reached the response: %s", body)
	}
	if !strings.Contains(body, "[host]") {
		t.Errorf("nothing was redacted as [host] — the rule may not be running "+
			"on this path at all: %s", body)
	}
}
