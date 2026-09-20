package server

import (
	"net/http"
	"strings"
	"testing"

	"mikrodash/internal/audit"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
)

// A peer's client configuration is a CREDENTIAL, so what these hold is not that
// the feature works but that it cannot leak sideways: who may ask, what the
// trail records, and the order the two happen in.

// wgFake answers from a table and records every command, in order, into the
// same log the audit sink writes to — so "the row was written BEFORE the router
// was asked" is a comparison rather than an assumption.
type wgFake struct {
	rows map[string][]routeros.Reply
	log  *[]string
}

func (f *wgFake) Connected() bool { return true }

func (f *wgFake) Exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	*f.log = append(*f.log, "cmd "+cmd.Path)
	return f.rows[cmd.Path], nil
}

// A synthetic key, never a real one — the shape RouterOS stores, all 'A's.
const fakeKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAEA="

// fakeQR builds a grid in the router's own shape: a symbol inside a two-module
// light border, each module two characters wide, `#` light and a space dark.
func fakeQR(size int) string {
	side := size + 4
	var b strings.Builder
	for row := 0; row < side; row++ {
		for col := 0; col < side; col++ {
			ch := "#"
			r, c := row-2, col-2
			if r >= 0 && c >= 0 && r < size && c < size && (r+c)%2 == 0 {
				ch = " "
			}
			b.WriteString(ch + ch)
		}
	}
	return b.String()
}

func wgReaderFake(log *[]string, qr string) *wgFake {
	return &wgFake{
		log: log,
		rows: map[string][]routeros.Reply{
			"/interface/wireguard/peers/print": {
				{".id": "*1", "name": "someone-else", "interface": "wg0", "public-key": "OTHERKEY"},
				{".id": "*3", "name": "phone", "interface": "wg0", "public-key": fakeKey},
			},
			"/interface/wireguard/peers/show-client-config": {
				{"conf": "[Interface]\nPrivateKey = " + fakeKey + "\nAddress = 198.51.100.2/32\n", "qr": qr},
			},
		},
	}
}

// wgSink records whole events, so a test can assert on the fields that must
// stay EMPTY as well as the ones that must be filled.
type wgSink struct {
	log    *[]string
	events []audit.DBEvent
}

func (s *wgSink) InsertAuditEvent(ev audit.DBEvent) error {
	s.events = append(s.events, ev)
	*s.log = append(*s.log, "audit "+ev.Action)
	return nil
}

func wgRecorder(log *[]string) (*audit.Recorder, *wgSink) {
	sink := &wgSink{log: log}
	return audit.New(sink, audit.ForUser("u1", "boss", "198.51.100.9"), func() int64 { return 1 }), sink
}

// ── WHO MAY ASK ────────────────────────────────────────────────────────────
//
// Write on the WireGuard page, both gates. The three refusals below come free
// from that shape and every one of them is right for a secret.
func TestRevealingAConfigNeedsWriteOnTheWireguardPage(t *testing.T) {
	build := func(access string) (*Server, *Session) {
		cn := rawAdminConn(t, false)
		cn.srv.rbac = rbac.New(cn.srv.auditDB, func() []rbac.Router { return []rbac.Router{{ID: "r1"}} })
		sess := cn.sess
		sess.Readable = []string{"r1"}
		sess.Pages = map[string]string{"wireguard": access}
		return cn.srv, sess
	}

	srv, sess := build("write")
	if !srv.mayRevealWgConfig(sess, "r1") {
		t.Fatal("an administrator with write on WireGuard may not reveal a config; " +
			"every other case here would then prove nothing")
	}
	if srv.mayRevealWgConfig(sess, "r-other") {
		t.Error("a router outside this session's reach was allowed")
	}

	srv, sess = build("read")
	if srv.mayRevealWgConfig(sess, "r1") {
		t.Error("READ on the page revealed a private key; the operator chose write")
	}

	// SIGN-IN OFF cannot pass a write check: there would be no identity to put
	// in the audit row, and a key revealed to nobody in particular is worse
	// than one not revealed.
	srv, sess = build("write")
	sess.AuthMode = "none"
	if srv.mayRevealWgConfig(sess, "r1") {
		t.Error("a session with sign-in off revealed a private key")
	}

	// NO AUDIT DATABASE, NO REVEAL. Nothing may hand out a key on an install
	// that cannot record who asked for it.
	srv, sess = build("write")
	srv.auditDB = nil
	if srv.mayRevealWgConfig(sess, "r1") {
		t.Error("a config was revealed on an install with no audit trail")
	}
}

// ── THE ORDER, AND WHAT THE TRAIL HOLDS ────────────────────────────────────
func TestTheRevealIsAuditedBeforeTheRouterIsAsked(t *testing.T) {
	var log []string
	rec, _ := wgRecorder(&log)
	srv := &Server{}

	reply, status, msg := srv.revealWgConfig(wgReaderFake(&log, fakeQR(21)), rec, "r1", fakeKey)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, msg)
	}

	// THE PEER IS FOUND FIRST, THEN RECORDED, THEN ASKED FOR. A row written
	// after the command would leave a dropped connection unrecorded; one
	// written before the lookup could name the wrong peer.
	want := []string{
		"cmd /interface/wireguard/peers/print",
		"audit wireguard.config.reveal",
		"cmd /interface/wireguard/peers/show-client-config",
	}
	if strings.Join(log, "|") != strings.Join(want, "|") {
		t.Errorf("order was %v, want %v", log, want)
	}
	if reply.Peer != "phone" || reply.Interface != "wg0" {
		t.Errorf("named the wrong peer: %+v", reply)
	}
}

// ── THE PEER IS FOUND BY ITS PUBLIC KEY ────────────────────────────────────
//
// A `.id` is a position in the router's table and moves. The public key is what
// `WgPeer.Identity` already uses, and a reveal that trusted an id from the
// browser could hand over the wrong device's key after a row was deleted.
func TestTheRevealFindsThePeerByItsPublicKey(t *testing.T) {
	var log []string
	rec, _ := wgRecorder(&log)
	srv := &Server{}

	_, status, _ := srv.revealWgConfig(wgReaderFake(&log, fakeQR(21)), rec, "r1", "NOSUCHKEY")
	if status != http.StatusNotFound {
		t.Errorf("an unknown public key returned %d, want 404", status)
	}
	// AND NOTHING WAS RECORDED OR ASKED. A peer that is not there is not a
	// reveal, so the trail must not carry one.
	for _, line := range log {
		if strings.HasPrefix(line, "audit") {
			t.Errorf("an unknown peer wrote an audit row: %v", log)
		}
		if strings.Contains(line, "show-client-config") {
			t.Errorf("an unknown peer still asked the router for a config: %v", log)
		}
	}
}

// ── AND THE TRAIL HOLDS NO SECRET ──────────────────────────────────────────
//
// `audit.IsCredentialField` does not match a field called `config`, so the
// masking that protects an ordinary write would not save this. What protects it
// is that the key is never put in the event at all — which is a claim only a
// test of the whole event can make.
func TestTheAuditRowForARevealCarriesNoSecret(t *testing.T) {
	var log []string
	rec, sink := wgRecorder(&log)
	srv := &Server{}

	reply, status, msg := srv.revealWgConfig(wgReaderFake(&log, fakeQR(21)), rec, "r1", fakeKey)
	if status != http.StatusOK {
		t.Fatalf("status %d: %s", status, msg)
	}
	// The control: the secret really is in the reply, so "absent from the
	// audit row" means something.
	if !strings.Contains(reply.Config, fakeKey) {
		t.Fatal("the config carries no key at all; this test would pass vacuously")
	}
	if len(sink.events) == 0 {
		t.Fatal("no audit row was written; there is nothing to check")
	}
	for _, ev := range sink.events {
		// EVERY TEXT FIELD THE ROW CARRIES, not a chosen few: the point is
		// that the secret is nowhere in it.
		blob := ev.Action + "|" + ev.Scope + "|" + ev.TargetType + "|" + ev.TargetID +
			"|" + ev.TargetName + "|" + ev.Outcome + "|" + ev.Detail
		if strings.Contains(blob, fakeKey) || strings.Contains(blob, "[Interface]") {
			t.Errorf("the audit row carries the configuration: %q", blob)
		}
	}
}

// ── A ROUTER WITH NO QR STILL HANDS OVER THE CONFIG ────────────────────────
//
// `show-sensitive` is RouterOS 7.21 and later, and it is what makes the router
// draw the symbol. An older router answers with the configuration and nothing
// else; an empty box the operator cannot explain is worse than a sentence.
func TestAConfigWithoutAQRStillArrivesAndSaysWhy(t *testing.T) {
	var log []string
	rec, _ := wgRecorder(&log)
	srv := &Server{}

	reply, status, _ := srv.revealWgConfig(wgReaderFake(&log, ""), rec, "r1", fakeKey)
	if status != http.StatusOK {
		t.Fatalf("a router with no QR refused the whole request (%d)", status)
	}
	if reply.Config == "" {
		t.Error("the configuration went missing with the QR")
	}
	if reply.QR != "" {
		t.Error("a QR was rendered from nothing")
	}
	if reply.Note == "" {
		t.Error("nothing explains the missing QR, so the dialog shows an empty box")
	}

	// AND A QR THIS APP CANNOT READ IS THE SAME CASE, not a failure: the
	// configuration is what the operator actually needs.
	reply, status, _ = srv.revealWgConfig(wgReaderFake(&log, "####not-a-symbol####"), rec, "r1", fakeKey)
	if status != http.StatusOK || reply.Config == "" || reply.QR != "" || reply.Note == "" {
		t.Errorf("an unreadable QR did not degrade to the config alone: %+v (status %d)", reply, status)
	}
}

// A QR THE ROUTER DID DRAW REACHES THE BROWSER AS SHAPES.
func TestAQRIsRenderedAsAnSVG(t *testing.T) {
	var log []string
	rec, _ := wgRecorder(&log)
	srv := &Server{}

	reply, _, _ := srv.revealWgConfig(wgReaderFake(&log, fakeQR(25)), rec, "r1", fakeKey)
	if !strings.HasPrefix(reply.QR, "<svg ") || !strings.Contains(reply.QR, "<rect") {
		t.Errorf("the QR did not render as an SVG: %.80s", reply.QR)
	}
	if strings.Contains(reply.QR, fakeKey) {
		t.Error("the SVG echoes the payload; it must be built from the matrix alone")
	}
}

// A DOWNLOADED FILE CANNOT CARRY A HEADER. A peer is named from a router
// comment, so its name is the operator's text and reaches Content-Disposition.
func TestADownloadedConfigCannotInjectAHeader(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"phone", "phone.conf"},
		{`a" ; drop`, "a----drop.conf"},
		{"kate's iPhone", "kate-s-iPhone.conf"},
		{`..\..\etc\passwd`, "------etc-passwd.conf"},
		{"", "wireguard.conf"},
	} {
		got := confFilename(c.in)
		if got != c.want {
			t.Errorf("confFilename(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "\"\r\n;/\\") {
			t.Errorf("confFilename(%q) = %q, which can break out of the header", c.in, got)
		}
	}
}
