// Package mailer sends one email over SMTP.
//
// It replaces nodemailer, and the substitution is not like-for-like: nodemailer
// assembled the MIME document and validated the addresses, and net/smtp does
// neither. Both of those jobs move to this file, and both are places where a
// mistake is a security bug rather than a rendering one — so each has an
// assertion of its own rather than being left to the shape of the code.
//
// What the live transport does, from `src/notifier.js`:
//
//	host, port (default 587), secure, auth only when a user or pass is set,
//	connectionTimeout 10s, greetingTimeout 10s, socketTimeout 15s
//
// The timeouts are NOT decoration. The live comment: "without these,
// nodemailer's defaults (~2 min) apply, and because send() awaits each channel
// in turn a black-holed SMTP host stalls every later channel behind it — and
// holds the test-notification HTTP request open for the same period."
package mailer

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// The three timeouts the live transport sets, and their live names.
const (
	ConnectTimeout  = 10 * time.Second // connectionTimeout
	GreetingTimeout = 10 * time.Second // greetingTimeout
	SocketTimeout   = 15 * time.Second // socketTimeout
)

// DefaultPort is `settings.smtpPort || 587`.
const DefaultPort = 587

// Config is the transport half of the install's settings.
type Config struct {
	Host string
	Port int
	// Secure is implicit TLS from the first byte, which is what `secure: true`
	// means to nodemailer — port 465's convention. When false the connection
	// starts in the clear and upgrades with STARTTLS if the server offers it.
	Secure bool
	User   string
	Pass   string
	From   string
}

// Attachment is one file in the message.
type Attachment struct {
	Filename    string
	ContentType string // defaults to application/octet-stream
	Content     []byte
}

// Message is one email.
//
// To and Bcc are kept apart all the way to the wire, and that separation is the
// point: BCC RECIPIENTS MUST NOT APPEAR IN THE HEADERS. They are named in the
// SMTP envelope (RCPT TO) and nowhere else. Writing them into a Bcc: header —
// which some libraries do, and which looks harmless because the header's name
// says "blind" — discloses every customer's address to every other customer on
// the same schedule.
type Message struct {
	To          []string
	Bcc         []string
	Subject     string
	Text        string
	Attachments []Attachment
}

// ErrUnsafeAddress is returned for an address that could inject headers or
// SMTP commands.
var ErrUnsafeAddress = errors.New("mailer: address contains a control character")

// checkAddress rejects an address that could break out of its line.
//
// nodemailer used to do this. net/smtp does not: it writes `RCPT TO:<addr>`
// with the string as given, so a CR or LF in an address is an SMTP command
// injection, and the same string in a To: header is a header injection. The
// check is on CONTROL CHARACTERS rather than on address SYNTAX on purpose —
// this is not the place to decide whether an address is deliverable, only
// whether it can escape the protocol.
func checkAddress(a string) error {
	if a == "" {
		return fmt.Errorf("%w: empty", ErrUnsafeAddress)
	}
	for _, r := range a {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q", ErrUnsafeAddress, a)
		}
	}
	return nil
}

// Compose builds the MIME document.
//
// `boundary` is a parameter so a test can be deterministic; production passes
// the empty string and gets a random one. A FIXED boundary in production would
// be a real bug: an attachment whose bytes happened to contain it would end the
// message early.
func Compose(from string, m Message, boundary string) ([]byte, error) {
	if err := checkAddress(from); err != nil {
		return nil, err
	}
	for _, a := range append(append([]string{}, m.To...), m.Bcc...) {
		if err := checkAddress(a); err != nil {
			return nil, err
		}
	}
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, fmt.Errorf("mailer: subject contains a line break")
	}
	if boundary == "" {
		var b [18]byte
		if _, err := rand.Read(b[:]); err != nil {
			return nil, err
		}
		boundary = "mikrodash-" + base64.RawURLEncoding.EncodeToString(b[:])
	}

	var h strings.Builder
	h.WriteString("From: " + from + "\r\n")
	if len(m.To) > 0 {
		h.WriteString("To: " + strings.Join(m.To, ", ") + "\r\n")
	}
	// NO Bcc HEADER. See Message.
	//
	// `mime.QEncoding` leaves an all-ASCII subject exactly as it is and encodes
	// anything else, so a plain subject stays readable in a raw message and an
	// accented one does not arrive as mojibake. It also folds, which is what
	// keeps a long subject from running past the line limit.
	h.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	h.WriteString("MIME-Version: 1.0\r\n")

	if len(m.Attachments) == 0 {
		h.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
		h.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		h.WriteString(wrap76(base64.StdEncoding.EncodeToString([]byte(m.Text))))
		return []byte(h.String()), nil
	}

	h.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")
	h.WriteString("--" + boundary + "\r\n")
	h.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	h.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	h.WriteString(wrap76(base64.StdEncoding.EncodeToString([]byte(m.Text))))
	h.WriteString("\r\n")

	for _, a := range m.Attachments {
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		h.WriteString("--" + boundary + "\r\n")
		h.WriteString("Content-Type: " + ct + "\r\n")
		// The filename goes through RFC 2047 too, and a quote in it would end the
		// parameter early — so it is encoded, not interpolated.
		h.WriteString("Content-Disposition: attachment; filename=\"" +
			mime.QEncoding.Encode("utf-8", sanitiseFilename(a.Filename)) + "\"\r\n")
		h.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		h.WriteString(wrap76(base64.StdEncoding.EncodeToString(a.Content)))
		h.WriteString("\r\n")
	}
	h.WriteString("--" + boundary + "--\r\n")
	return []byte(h.String()), nil
}

// sanitiseFilename removes what cannot appear inside a quoted parameter.
func sanitiseFilename(f string) string {
	f = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, f)
	if f == "" {
		return "attachment"
	}
	return f
}

// wrap76 breaks a base64 blob into RFC 2045 lines. A single unbroken line is
// legal to produce and rejected by enough MTAs to matter.
func wrap76(s string) string {
	var b strings.Builder
	for len(s) > 76 {
		b.WriteString(s[:76])
		b.WriteString("\r\n")
		s = s[76:]
	}
	b.WriteString(s)
	return b.String()
}

// Send delivers one message.
func Send(cfg Config, m Message) error {
	if cfg.Host == "" {
		return errors.New("mailer: no SMTP host configured")
	}
	port := cfg.Port
	if port == 0 {
		port = DefaultPort
	}
	body, err := Compose(cfg.From, m, "")
	if err != nil {
		return err
	}

	// Every recipient the envelope names, whether or not it is in a header.
	rcpt := append(append([]string{}, m.To...), m.Bcc...)
	if len(rcpt) == 0 {
		return errors.New("mailer: no recipients")
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))
	conn, err := net.DialTimeout("tcp", addr, ConnectTimeout)
	if err != nil {
		return err
	}
	// The SOCKET deadline covers everything after the greeting. It is reset
	// before the data phase, which is the one step whose length depends on the
	// message rather than on the server.
	_ = conn.SetDeadline(time.Now().Add(GreetingTimeout))

	if cfg.Secure {
		conn = tls.Client(conn, &tls.Config{ServerName: cfg.Host})
	}

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()

	_ = conn.SetDeadline(time.Now().Add(SocketTimeout))

	if !cfg.Secure {
		// STARTTLS when the server offers it. Opportunistic, matching
		// nodemailer's `secure: false` default — an install pointing at a
		// localhost relay must keep working, and demanding TLS here would be a
		// behaviour change rather than a port.
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return err
			}
		}
	}

	// Auth ONLY when a user or a password is set, which is the live condition —
	// `(settings.smtpUser || settings.smtpPass) ? {...} : undefined`. Offering
	// credentials to a relay that did not ask is how they end up in a log.
	if cfg.User != "" || cfg.Pass != "" {
		// THE ADVERTISED LIST IS USED, not discarded. `Extension` returns the
		// mechanisms after the AUTH keyword; this asked for them and then always
		// sent PLAIN, which Microsoft 365 answers with "504 5.7.4 Unrecognized
		// authentication type" because it offers LOGIN and XOAUTH2. See pickAuth.
		if ok, mechs := c.Extension("AUTH"); ok {
			auth, err := pickAuth(mechs, cfg.User, cfg.Pass, cfg.Host)
			if err != nil {
				return err
			}
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}

	if err := c.Mail(cfg.From); err != nil {
		return err
	}
	for _, r := range rcpt {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}

	_ = conn.SetDeadline(time.Now().Add(SocketTimeout))
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
