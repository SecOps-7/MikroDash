package mailer

import (
	"bufio"
	"encoding/base64"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// ── Compose ─────────────────────────────────────────────────────────────────

// TestBccNeverReachesTheHeaders is the one that matters most in this file.
//
// The whole reason `reports.MailEnvelope` puts recipients in BCC is that a
// scheduled report frequently goes to different customers, and a `to:` list
// would show each of them every other address. That protection survives only as
// far as the MIME document: a Bcc: header would disclose exactly the same set,
// and the header's name makes it look deliberate.
func TestBccNeverReachesTheHeaders(t *testing.T) {
	out, err := Compose("reports@example.com", Message{
		To:      []string{"reports@example.com"},
		Bcc:     []string{"alice@customer-a.example", "bob@customer-b.example"},
		Subject: "Monthly", Text: "hello",
	}, "BOUND")
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	doc := string(out)
	headers := doc
	if i := strings.Index(doc, "\r\n\r\n"); i >= 0 {
		headers = doc[:i]
	}
	if strings.Contains(strings.ToLower(headers), "bcc:") {
		t.Error("the message has a Bcc: header")
	}
	for _, a := range []string{"alice@customer-a.example", "bob@customer-b.example"} {
		if strings.Contains(doc, a) {
			t.Errorf("bcc recipient %q appears in the MIME document", a)
		}
	}
	if !strings.Contains(headers, "To: reports@example.com") {
		t.Error("the sending address is not in To:")
	}
}

// TestComposeRefusesAnAddressThatCouldInject covers what nodemailer used to do
// and net/smtp does not.
func TestComposeRefusesAnAddressThatCouldInject(t *testing.T) {
	bad := []string{
		"a@example.com\r\nBcc: evil@example.net",
		"a@example.com\nRCPT TO:<evil@example.net>",
		"a@example.com\rX: y",
		"a@example.com\x00",
		"a@example.com\x7f",
		"",
	}
	for _, addr := range bad {
		t.Run(strings.ReplaceAll(addr, "\r\n", "\\r\\n"), func(t *testing.T) {
			if _, err := Compose("from@example.com", Message{To: []string{addr}, Text: "x"}, "B"); !errors.Is(err, ErrUnsafeAddress) {
				t.Errorf("To: accepted %q (err %v)", addr, err)
			}
			if _, err := Compose("from@example.com", Message{Bcc: []string{addr}, Text: "x"}, "B"); !errors.Is(err, ErrUnsafeAddress) {
				t.Errorf("Bcc: accepted %q (err %v)", addr, err)
			}
			if _, err := Compose(addr, Message{To: []string{"ok@example.com"}, Text: "x"}, "B"); !errors.Is(err, ErrUnsafeAddress) {
				t.Errorf("From: accepted %q (err %v)", addr, err)
			}
		})
	}
	// And a normal address must still be accepted, or the check above proves
	// nothing except that everything is rejected.
	if _, err := Compose("from@example.com", Message{To: []string{"a+tag@sub.example.com"}, Text: "x"}, "B"); err != nil {
		t.Errorf("a normal address was rejected: %v", err)
	}
}

func TestComposeRefusesASubjectWithALineBreak(t *testing.T) {
	_, err := Compose("f@example.com", Message{
		To: []string{"a@example.com"}, Subject: "ok\r\nBcc: evil@example.net", Text: "x"}, "B")
	if err == nil {
		t.Error("a subject containing CRLF was accepted")
	}
}

// TestANonASCIISubjectIsEncoded, and an ASCII one is left alone: an install
// whose schedules are all plain English should produce a readable raw message.
func TestANonASCIISubjectIsEncoded(t *testing.T) {
	plain, err := Compose("f@example.com", Message{To: []string{"a@example.com"},
		Subject: "Monthly usage", Text: "x"}, "B")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plain), "Subject: Monthly usage\r\n") {
		t.Error("an ASCII subject was encoded when it did not need to be")
	}

	accented, err := Compose("f@example.com", Message{To: []string{"a@example.com"},
		Subject: "Café — Trafic", Text: "x"}, "B")
	if err != nil {
		t.Fatal(err)
	}
	line := headerLine(string(accented), "Subject:")
	if !strings.Contains(line, "=?utf-8?") {
		t.Errorf("a non-ASCII subject was not RFC 2047 encoded: %q", line)
	}
	for _, r := range line {
		if r > 0x7f {
			t.Errorf("the encoded subject still contains a raw non-ASCII rune: %q", line)
			break
		}
	}
}

func TestAttachmentsAreBase64AndBounded(t *testing.T) {
	payload := []byte(strings.Repeat("PDFDATA", 500))
	out, err := Compose("f@example.com", Message{
		To: []string{"a@example.com"}, Subject: "s", Text: "body",
		Attachments: []Attachment{{Filename: "ping-report.pdf", ContentType: "application/pdf", Content: payload}},
	}, "BOUND")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(out)

	if !strings.Contains(doc, `Content-Type: multipart/mixed; boundary="BOUND"`) {
		t.Error("no multipart container")
	}
	if !strings.Contains(doc, "--BOUND--\r\n") {
		t.Error("the multipart body is not terminated")
	}
	if !strings.Contains(doc, `filename="ping-report.pdf"`) {
		t.Error("the attachment has no filename")
	}
	if !strings.Contains(doc, "Content-Type: application/pdf") {
		t.Error("the attachment's content type was lost")
	}
	// The raw bytes must NOT appear: an unencoded 8-bit body is what makes a
	// message get mangled or rejected in transit.
	if strings.Contains(doc, "PDFDATAPDFDATA") {
		t.Error("the attachment was not encoded")
	}
	enc := base64.StdEncoding.EncodeToString(payload)
	if !strings.Contains(strings.ReplaceAll(doc, "\r\n", ""), enc) {
		t.Error("the attachment's base64 is not present or not contiguous once unwrapped")
	}
	// Every line must be within the 998-octet limit, which is what wrapping is
	// for. A single unbroken base64 line is legal to produce and rejected often
	// enough to matter.
	for i, line := range strings.Split(doc, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("line %d is %d octets", i, len(line))
		}
	}
}

// TestTheBoundaryIsRandomInProduction: a fixed boundary would let an attachment
// whose bytes contained it end the message early.
func TestTheBoundaryIsRandomInProduction(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		out, err := Compose("f@example.com", Message{To: []string{"a@example.com"}, Text: "x",
			Attachments: []Attachment{{Filename: "a.pdf", Content: []byte("z")}}}, "")
		if err != nil {
			t.Fatal(err)
		}
		b := headerLine(string(out), "Content-Type: multipart/mixed;")
		if b == "" {
			t.Fatal("no multipart header")
		}
		if seen[b] {
			t.Fatalf("the boundary repeated across two messages: %q", b)
		}
		seen[b] = true
	}
}

func TestAFilenameCannotEscapeItsParameter(t *testing.T) {
	out, err := Compose("f@example.com", Message{To: []string{"a@example.com"}, Text: "x",
		Attachments: []Attachment{{Filename: "a\".pdf\r\nX-Evil: 1", Content: []byte("z")}}}, "B")
	if err != nil {
		t.Fatal(err)
	}
	// The test is that it cannot START A LINE, not that the text is gone.
	// `X-Evil: 1` surviving as characters INSIDE `filename="..."` is inert -- it
	// is the CRLF that would make it a header, and that is what is removed. An
	// assertion that the substring vanished would have demanded the sanitiser
	// strip a colon, which is both unnecessary and would mangle real filenames.
	for _, line := range strings.Split(string(out), "\r\n") {
		if strings.HasPrefix(line, "X-Evil") {
			t.Errorf("a filename injected a header: %q", line)
		}
	}
	if strings.Contains(string(out), `a".pdf`) {
		t.Error("a quote in a filename was not removed -- it would end the parameter early")
	}
	// And the CR/LF really are gone, rather than the header just happening to
	// look right.
	ct := headerLine(string(out), "Content-Disposition:")
	if ct == "" || !strings.Contains(ct, "filename=") {
		t.Fatalf("no Content-Disposition with a filename: %q", ct)
	}
}

// ── Send ────────────────────────────────────────────────────────────────────

// TestSendSpeaksSMTP drives the real Send against a real socket, because the
// envelope is the half Compose cannot check: the bcc recipients must reach the
// server as RCPT TO commands even though they are in no header.
func TestSendSpeaksSMTP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type result struct {
		mailFrom string
		rcpt     []string
		data     string
	}
	done := make(chan result, 1)
	go func() {
		var r result
		conn, err := ln.Accept()
		if err != nil {
			done <- r
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
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
				w("250 SIZE 20000000") // deliberately NO STARTTLS and NO AUTH
			case strings.HasPrefix(cmd, "MAIL FROM"):
				r.mailFrom = strings.TrimSpace(line)
				w("250 ok")
			case strings.HasPrefix(cmd, "RCPT TO"):
				r.rcpt = append(r.rcpt, strings.TrimSpace(line))
				w("250 ok")
			case cmd == "DATA":
				w("354 go")
				var b strings.Builder
				for {
					l, err := br.ReadString('\n')
					if err != nil {
						break
					}
					if l == ".\r\n" {
						break
					}
					b.WriteString(l)
				}
				r.data = b.String()
				w("250 queued")
			case cmd == "QUIT":
				w("221 bye")
				done <- r
				return
			default:
				w("250 ok")
			}
		}
		done <- r
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	cfg := Config{Host: host, Port: atoi(port), From: "reports@example.com"}
	err = Send(cfg, Message{
		To:      []string{"reports@example.com"},
		Bcc:     []string{"alice@customer-a.example", "bob@customer-b.example"},
		Subject: "Monthly usage", Text: "body text",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case r := <-done:
		if !strings.Contains(r.mailFrom, "reports@example.com") {
			t.Errorf("MAIL FROM was %q", r.mailFrom)
		}
		// THREE recipients on the wire: the To and both Bcc.
		if len(r.rcpt) != 3 {
			t.Fatalf("%d RCPT TO commands, want 3: %v", len(r.rcpt), r.rcpt)
		}
		for _, want := range []string{"reports@example.com", "alice@customer-a.example", "bob@customer-b.example"} {
			if !strings.Contains(strings.Join(r.rcpt, " "), want) {
				t.Errorf("%q was never sent as a RCPT TO", want)
			}
		}
		// ...and neither bcc address in the DATA.
		for _, a := range []string{"alice@customer-a.example", "bob@customer-b.example"} {
			if strings.Contains(r.data, a) {
				t.Errorf("bcc recipient %q was written into the message body", a)
			}
		}
		if !strings.Contains(r.data, "Subject: Monthly usage") {
			t.Error("the subject did not reach the server")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the server never saw a complete transaction")
	}
}

// TestSendDoesNotOfferCredentialsUnasked: the live condition is
// `(smtpUser || smtpPass) ? auth : undefined`, and a relay that advertises no
// AUTH must not be handed a password.
func TestSendDoesNotOfferCredentialsUnasked(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	sawAuth := make(chan bool, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		br := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		w("220 test ESMTP")
		auth := false
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				break
			}
			cmd := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(cmd, "EHLO"):
				w("250-test")
				w("250 SIZE 20000000")
			case strings.HasPrefix(cmd, "AUTH"):
				auth = true
				w("235 ok")
			case cmd == "DATA":
				w("354 go")
				for {
					l, err := br.ReadString('\n')
					if err != nil || l == ".\r\n" {
						break
					}
				}
				w("250 queued")
			case cmd == "QUIT":
				w("221 bye")
				sawAuth <- auth
				return
			default:
				w("250 ok")
			}
		}
		sawAuth <- auth
	}()

	host, port, _ := net.SplitHostPort(ln.Addr().String())
	err = Send(Config{Host: host, Port: atoi(port), From: "f@example.com",
		User: "user", Pass: "hunter2"},
		Message{To: []string{"a@example.com"}, Subject: "s", Text: "t"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case a := <-sawAuth:
		if a {
			t.Error("credentials were offered to a server that advertised no AUTH")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no transaction completed")
	}
}

func TestSendRefusesWithNoHostOrNoRecipients(t *testing.T) {
	if err := Send(Config{From: "f@example.com"}, Message{To: []string{"a@example.com"}}); err == nil {
		t.Error("a send with no host was accepted")
	}
	if err := Send(Config{Host: "127.0.0.1", From: "f@example.com"}, Message{}); err == nil {
		t.Error("a send with no recipients was accepted")
	}
}

func headerLine(doc, prefix string) string {
	for _, l := range strings.Split(doc, "\r\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	return ""
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}
