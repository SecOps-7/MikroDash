package mailer

import (
	"bufio"
	"encoding/base64"
	"net"
	"net/smtp"
	"strings"
	"testing"
)

// TestMicrosoftsMechanismListPicksLOGIN is the reported bug.
//
// `smtp-mail.outlook.com:587` with STARTTLS advertises `LOGIN XOAUTH2`, and
// this app always sent PLAIN — answered with "504 5.7.4 Unrecognized
// authentication type". Issue #126.
func TestMicrosoftsMechanismListPicksLOGIN(t *testing.T) {
	a, err := pickAuth("LOGIN XOAUTH2", "u@example.com", "pw", "smtp-mail.outlook.com")
	if err != nil {
		t.Fatalf("refused a server offering LOGIN: %v", err)
	}
	if _, ok := a.(*loginAuth); !ok {
		t.Fatalf("picked %T against a server that does not offer PLAIN", a)
	}
}

// TestPlainIsStillPreferred — every existing install uses PLAIN, and a server
// offering both must behave exactly as it did.
func TestPlainIsStillPreferred(t *testing.T) {
	for _, list := range []string{"PLAIN LOGIN", "LOGIN PLAIN", "PLAIN"} {
		a, err := pickAuth(list, "u", "p", "mail.example.com")
		if err != nil {
			t.Fatalf("%q: %v", list, err)
		}
		if _, ok := a.(*loginAuth); ok {
			t.Errorf("%q: chose LOGIN where PLAIN is offered", list)
		}
	}
}

// TestAnEmptyListFallsBackToPlain. A server advertising AUTH with no mechanisms
// is out of spec; refusing it would break a relay that works today.
func TestAnEmptyListFallsBackToPlain(t *testing.T) {
	a, err := pickAuth("", "u", "p", "mail.example.com")
	if err != nil || a == nil {
		t.Fatalf("an empty mechanism list was refused: %v", err)
	}
	if _, ok := a.(*loginAuth); ok {
		t.Error("chose LOGIN for a server that advertised nothing")
	}
}

// TestAnUnsupportedListIsNamed. XOAUTH2 alone needs a token this app cannot
// obtain, so it fails HERE with a sentence naming what the server wants, rather
// than as a bare 504 from the server.
func TestAnUnsupportedListIsNamed(t *testing.T) {
	_, err := pickAuth("XOAUTH2 GSSAPI", "u", "p", "mail.example.com")
	if err == nil {
		t.Fatal("accepted a server offering only mechanisms this app cannot use")
	}
	if !strings.Contains(err.Error(), "XOAUTH2") {
		t.Errorf("the refusal does not say what the server offers: %v", err)
	}
}

// TestLoginSendsTheUsernameThenThePassword, in that order and once each.
func TestLoginSendsTheUsernameThenThePassword(t *testing.T) {
	a := &loginAuth{user: "bob", pass: "s3cret", host: "mail.example.com"}
	mech, resp, err := a.Start(&smtp.ServerInfo{Name: "mail.example.com", TLS: true})
	if err != nil || mech != "LOGIN" || len(resp) != 0 {
		t.Fatalf("Start = %q %q %v", mech, resp, err)
	}
	got, err := a.Next([]byte("Username:"), true)
	if err != nil || string(got) != "bob" {
		t.Errorf("first challenge answered %q (%v)", got, err)
	}
	got, err = a.Next([]byte("Password:"), true)
	if err != nil || string(got) != "s3cret" {
		t.Errorf("second challenge answered %q (%v)", got, err)
	}
	// A third challenge is not part of this exchange, and answering it would
	// send a credential in reply to a prompt nobody understood.
	if _, err := a.Next([]byte("Something:"), true); err == nil {
		t.Error("a third challenge was answered rather than refused")
	}
}

// TestTheServerPromptIsNotMatchedOnItsText. Servers send "Username:",
// "User Name:" and localised variants; an implementation comparing strings
// works against whichever server it was written for.
func TestTheServerPromptIsNotMatchedOnItsText(t *testing.T) {
	a := &loginAuth{user: "bob", pass: "s3cret", host: "h"}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "h", TLS: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.Next([]byte("Benutzername:"), true); string(got) != "bob" {
		t.Errorf("a differently-worded prompt answered %q", got)
	}
	if got, _ := a.Next([]byte("Kennwort:"), true); string(got) != "s3cret" {
		t.Errorf("a differently-worded prompt answered %q", got)
	}
}

// TestLoginRefusesAnUnencryptedConnection. LOGIN base64-encodes; it does not
// encrypt. `PlainAuth` refuses in exactly this case and a mechanism chosen
// AUTOMATICALLY must not be a way to lose that protection.
func TestLoginRefusesAnUnencryptedConnection(t *testing.T) {
	a := &loginAuth{user: "bob", pass: "s3cret", host: "mail.example.com"}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "mail.example.com", TLS: false}); err == nil {
		t.Error("sent credentials over an unencrypted connection")
	}
	// ...but a localhost relay still works, matching PlainAuth exactly, so an
	// install pointing at a local sink is not broken by this change.
	l := &loginAuth{user: "bob", pass: "s3cret", host: "localhost"}
	if _, _, err := l.Start(&smtp.ServerInfo{Name: "localhost", TLS: false}); err != nil {
		t.Errorf("refused a localhost relay: %v", err)
	}
}

// TestLoginRefusesAMismatchedHost — the same guard PlainAuth carries, so a
// redirected connection cannot collect the credentials.
func TestLoginRefusesAMismatchedHost(t *testing.T) {
	a := &loginAuth{user: "bob", pass: "s3cret", host: "mail.example.com"}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "evil.example.net", TLS: true}); err == nil {
		t.Error("authenticated against a server other than the configured host")
	}
}

// ── END TO END, THROUGH Send, AGAINST A LOGIN-ONLY SERVER ──────────────────
//
// The cases above pin the decision. This one pins the WIRE: it runs the real
// `Send` against a server that advertises LOGIN and nothing else — Microsoft's
// shape — and asserts the credentials actually arrive, base64 as the mechanism
// requires. Before the fix this exchange ended at `AUTH PLAIN` with a 504.
//
// `localhost` is the host, which is what lets LOGIN run without TLS here: the
// mechanism refuses cleartext everywhere else, matching PlainAuth.
func TestSendAuthenticatesWithLOGIN(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type exchange struct {
		sawAuthLogin bool
		user, pass   string
		delivered    bool
	}
	done := make(chan exchange, 1)

	go func() {
		var e exchange
		conn, err := ln.Accept()
		if err != nil {
			done <- e
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		read := func() string {
			l, _ := br.ReadString('\n')
			return strings.TrimSpace(l)
		}
		w("220 test ESMTP")
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				break
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				w("250-test")
				// MICROSOFT'S SHAPE: LOGIN and XOAUTH2, no PLAIN.
				w("250 AUTH LOGIN XOAUTH2")
			case cmd == "AUTH LOGIN":
				e.sawAuthLogin = true
				w("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
				u, _ := base64.StdEncoding.DecodeString(read())
				e.user = string(u)
				w("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
				p, _ := base64.StdEncoding.DecodeString(read())
				e.pass = string(p)
				w("235 authenticated")
			case strings.HasPrefix(cmd, "AUTH PLAIN"):
				// THE BUG, answered as Microsoft answers it. If this fires the
				// test fails on `sawAuthLogin` below rather than hanging.
				w("504 5.7.4 Unrecognized authentication type")
			case cmd == "DATA":
				w("354 go")
				for {
					l, err := br.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
				}
				e.delivered = true
				w("250 queued")
			case cmd == "QUIT":
				w("221 bye")
				done <- e
				return
			default:
				w("250 ok")
			}
		}
		done <- e
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	_ = host
	cfg := Config{
		Host: "localhost", Port: atoi(port), From: "reports@example.com",
		User: "bob@example.com", Pass: "s3cret",
	}
	if err := Send(cfg, Message{
		To: []string{"someone@example.com"}, Subject: "hello", Text: "body",
	}); err != nil {
		t.Fatalf("Send against a LOGIN-only server: %v", err)
	}

	e := <-done
	if !e.sawAuthLogin {
		t.Error("never sent AUTH LOGIN — a server offering no PLAIN answers " +
			"504 5.7.4 Unrecognized authentication type, which is issue #126")
	}
	if e.user != "bob@example.com" || e.pass != "s3cret" {
		t.Errorf("credentials arrived as %q / %q", e.user, e.pass)
	}
	if !e.delivered {
		t.Error("the message was not delivered after authenticating")
	}
}
