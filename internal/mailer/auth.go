package mailer

// Choosing an SMTP authentication mechanism from what the server actually
// offers.
//
// ── ONE MECHANISM WAS ASSUMED, AND MICROSOFT DOES NOT OFFER IT ─────────────
//
// This asked `c.Extension("AUTH")`, which returns the server's advertised
// mechanism list, threw the list away and always sent `AUTH PLAIN`. Against
// Microsoft 365 (`smtp-mail.outlook.com:587`, STARTTLS) that fails with
//
//	504 5.7.4 Unrecognized authentication type
//
// because those servers advertise `LOGIN XOAUTH2` and not PLAIN. Reported on
// issue #126. Every test relay in this project's own history offered PLAIN, so
// nothing here ever exercised the other branch — there was no other branch.
//
// ── WHY LOGIN HAD TO BE WRITTEN OUT ────────────────────────────────────────
//
// `net/smtp` ships PlainAuth and CRAMMD5Auth and no LOGIN, deliberately: LOGIN
// is not in any RFC, it is a de-facto mechanism, and the standard library does
// not implement de-facto protocols. It is also what Microsoft, and a good deal
// of shared hosting, actually requires. Fifteen lines here is the price of that
// gap.
//
// ── IT IS AS EXPOSED AS PLAIN, AND GUARDED THE SAME WAY ────────────────────
//
// LOGIN sends the username and password base64-encoded, which is encoding and
// not encryption. `PlainAuth` refuses to run on an unencrypted connection
// unless the server is localhost, and this mirrors that rule exactly rather
// than inventing a laxer one — a mechanism that is chosen automatically must
// not be a way to downgrade the protection the operator would have had.

import (
	"errors"
	"fmt"
	"net/smtp"
	"strings"
)

// pickAuth chooses a mechanism from the server's advertised list.
//
// `advertised` is the parameter string from `EHLO`'s AUTH line, e.g.
// "LOGIN XOAUTH2". An EMPTY list falls back to PLAIN: a server that advertises
// AUTH with no mechanisms is out of spec, and PLAIN is what this did before, so
// the fallback keeps a working relay working rather than refusing it on a
// technicality.
//
// PLAIN IS PREFERRED WHERE BOTH ARE OFFERED. It is one round trip instead of
// three, it is the RFC mechanism, and it is what every install of this app has
// been using — so a server offering both behaves exactly as it did.
func pickAuth(advertised, user, pass, host string) (smtp.Auth, error) {
	mechs := map[string]bool{}
	for _, m := range strings.Fields(strings.ToUpper(advertised)) {
		mechs[m] = true
	}
	switch {
	case len(mechs) == 0, mechs["PLAIN"]:
		return smtp.PlainAuth("", user, pass, host), nil
	case mechs["LOGIN"]:
		return &loginAuth{user: user, pass: pass, host: host}, nil
	}
	// NAMED, so the operator sees what the server wants instead of a bare 504.
	// XOAUTH2 lands here, and that is honest: it needs a token this app has no
	// way to obtain, and pretending otherwise would fail later and less clearly.
	return nil, fmt.Errorf("the server offers no authentication method this app "+
		"supports (it offers: %s; this app can use PLAIN or LOGIN)",
		strings.TrimSpace(advertised))
}

// loginAuth is the de-facto LOGIN mechanism: username then password, each
// base64 in its own step.
type loginAuth struct {
	user, pass string
	host       string
	// step counts the challenges answered. COUNTED RATHER THAN MATCHED on the
	// prompt text: servers send "Username:", "User Name:" and localised
	// variants, and a port that compared strings would work against whichever
	// server it was written for and fail elsewhere — which is this bug again.
	step int
}

func (a *loginAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	// The same two guards `net/smtp`'s PlainAuth applies, for the same reason:
	// this mechanism hands over credentials in the clear once base64 is undone.
	if !server.TLS && !isLocalhost(server.Name) {
		return "", nil, errors.New("mailer: refusing to send LOGIN credentials over " +
			"an unencrypted connection")
	}
	if server.Name != a.host {
		return "", nil, errors.New("mailer: server name does not match the configured host")
	}
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.user), nil
	case 2:
		return []byte(a.pass), nil
	}
	// A third challenge means the exchange is not the one this implements.
	// Failing is right: answering with something would send a credential in
	// reply to a prompt nobody understood.
	return nil, fmt.Errorf("mailer: unexpected LOGIN challenge %q", string(fromServer))
}

func isLocalhost(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}
