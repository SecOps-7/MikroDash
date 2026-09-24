package server

import (
	"os"
	"path/filepath"
	"testing"

	"mikrodash/internal/db"
	"mikrodash/internal/notify"
	"mikrodash/internal/store"
)

// WHICH CHANNEL LENDS ITS MAIL SERVER.
//
// ── THIS USED TO DECIDE WHERE REPORTS WENT ────────────────────────────────
//
// It was `chooseReportChannel`, and it took a `want` id because the install
// named ONE channel that every scheduled report left through. A schedule names
// its own channel now, so nothing install-wide chooses for reports, and the
// parameter went with the setting that fed it.
//
// What is left is the one genuinely install-wide question: which mail server a
// user's personal email destination borrows, when they have configured an
// address but no server of their own. The rules that survived are the ones that
// were about SAFETY rather than about reports.
func TestWhichChannelLendsItsMailServer(t *testing.T) {
	smtp := func(id, owner string, enabled int) db.NotifyChannel {
		return db.NotifyChannel{ID: id, Owner: owner, Kind: notify.KindSMTP, Enabled: enabled}
	}
	hook := func(id string) db.NotifyChannel {
		return db.NotifyChannel{ID: id, Owner: db.InstallOwner, Kind: notify.KindWebhook, Enabled: 1}
	}

	for _, tc := range []struct {
		name string
		rows []db.NotifyChannel
		id   string
	}{
		{
			"the first enabled install SMTP channel",
			[]db.NotifyChannel{smtp("m1", db.InstallOwner, 1), smtp("m2", db.InstallOwner, 1)},
			"m1",
		},
		{
			"a webhook is never chosen, because it cannot carry mail",
			[]db.NotifyChannel{hook("w1"), smtp("m1", db.InstallOwner, 1)},
			"m1",
		},
		{
			// A user's personal mail channel must not become the server another
			// user's alerts go out through.
			"a user-owned mail channel is not the install's server",
			[]db.NotifyChannel{smtp("mine", "user:7", 1), smtp("m1", db.InstallOwner, 1)},
			"m1",
		},
		{
			"a disabled channel is skipped",
			[]db.NotifyChannel{smtp("off", db.InstallOwner, 0), smtp("m1", db.InstallOwner, 1)},
			"m1",
		},
		{
			"no SMTP channel at all means no mail server, not a webhook",
			[]db.NotifyChannel{hook("w1")},
			"",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := installMailChannel(tc.rows)
			id := ""
			if got != nil {
				id = got.ID
			}
			if id != tc.id {
				t.Errorf("lent %q, want %q", id, tc.id)
			}
		})
	}
}

// mailServer is a Server with a real database and a real settings store.
//
// A REAL DATABASE RATHER THAN A HAND-BUILT SLICE, because what the tests below
// check is that a channel's config survives sealing, storage and decoding. A
// recipient list lost inside the envelope looks exactly like one that was never
// set, and only the round trip tells them apart.
func mailServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "routers.json"), []byte(`[]`), 0o600); err != nil {
		t.Fatal(err)
	}
	return &Server{store: st, auditDB: d}
}

// writeChannel stores one channel with its config sealed the way the API seals
// it, so the read path under test is the real one.
func writeChannel(t *testing.T, s *Server, c db.NotifyChannel, cfg smtpConfigJSON) {
	t.Helper()
	sealed, err := s.sealChannelConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.Config = sealed
	c.Events, c.Routers = "[]", "[]"
	if err := s.auditDB.UpsertNotifyChannel(c); err != nil {
		t.Fatal(err)
	}
}

func TestAScheduleTakesItsServerAndItsRecipientsFromOneChannel(t *testing.T) {
	s := mailServer(t)
	writeChannel(t, s, db.NotifyChannel{
		ID: "ok", Owner: db.InstallOwner, Name: "Mail", Kind: notify.KindSMTP, Enabled: 1,
	}, smtpConfigJSON{
		Host: "mail.example.net", Port: 587, Secure: true, User: "u", Pass: "p",
		From: "md@example.net", To: "ops@example.net, noc@example.net",
	})

	cfg, from, to, ok := s.scheduleMail("ok")
	if !ok {
		t.Fatal("a fully configured mail channel was refused")
	}
	if cfg.Host != "mail.example.net" || cfg.Port != 587 || !cfg.Secure ||
		cfg.User != "u" || cfg.Pass != "p" || from != "md@example.net" {
		t.Errorf("the server config did not survive the round trip: %+v from=%q", cfg, from)
	}
	// THE To IS A LIST. It is one stored string, and a reader that took it whole
	// would try to mail a single address literally named
	// "ops@example.net, noc@example.net" — so the report would fail outright
	// rather than reach one of the two, and either way it does not arrive.
	if len(to) != 2 || to[0] != "ops@example.net" || to[1] != "noc@example.net" {
		t.Errorf("recipients came back as %#v, want both addresses split", to)
	}
}

// EVERY REFUSAL, because each is a report that silently does not arrive and they
// are individually easy to get wrong.
func TestAScheduleWithNowhereToSendIsRefused(t *testing.T) {
	s := mailServer(t)
	full := smtpConfigJSON{Host: "h", Port: 25, From: "f@example.net", To: "ops@example.net"}

	writeChannel(t, s, db.NotifyChannel{ID: "off", Owner: db.InstallOwner,
		Kind: notify.KindSMTP, Enabled: 0}, full)
	writeChannel(t, s, db.NotifyChannel{ID: "nohost", Owner: db.InstallOwner,
		Kind: notify.KindSMTP, Enabled: 1},
		smtpConfigJSON{From: "f@example.net", To: "o@example.net"})
	writeChannel(t, s, db.NotifyChannel{ID: "nofrom", Owner: db.InstallOwner,
		Kind: notify.KindSMTP, Enabled: 1}, smtpConfigJSON{Host: "h", To: "o@example.net"})
	writeChannel(t, s, db.NotifyChannel{ID: "noto", Owner: db.InstallOwner,
		Kind: notify.KindSMTP, Enabled: 1}, smtpConfigJSON{Host: "h", From: "f@example.net"})
	if err := s.auditDB.UpsertNotifyChannel(db.NotifyChannel{ID: "hook",
		Owner: db.InstallOwner, Kind: notify.KindWebhook, Enabled: 1,
		Config: `{"urls":["tgram://t/1"]}`, Events: "[]", Routers: "[]"}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, id string }{
		{"no channel named at all", ""},
		{"a channel that does not exist", "gone"},
		{"a channel that is switched off", "off"},
		{"a webhook, which cannot carry a PDF", "hook"},
		{"a mail channel with no host", "nohost"},
		{"a mail channel with no From", "nofrom"},
		{"a mail channel with no recipients", "noto"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, ok := s.scheduleMail(tc.id); ok {
				t.Error("accepted — the schedule would try to send, and the failure " +
					"would surface as a mail error a month later, if at all")
			}
		})
	}
}

// THE WRITE-TIME GUARD REFUSES THE SAME THINGS, while the operator is looking at
// the form. It is not a substitute for the send-time check — a channel can be
// deleted or switched to a webhook afterwards — so both exist and both are here.
func TestAScheduleCannotBeSavedAgainstAChannelThatCannotSendIt(t *testing.T) {
	s := mailServer(t)
	usable := smtpConfigJSON{Host: "h", From: "f@example.net", To: "o@example.net"}
	writeChannel(t, s, db.NotifyChannel{ID: "mail", Owner: db.InstallOwner,
		Kind: notify.KindSMTP, Enabled: 1}, usable)
	// A USER'S OWN MAIL CHANNEL IS REFUSED TOO. A report can cover every router
	// its schedule reaches, so routing one through a personal channel would hand
	// that user routers they were never granted.
	writeChannel(t, s, db.NotifyChannel{ID: "mine", Owner: "user:7",
		Kind: notify.KindSMTP, Enabled: 1}, usable)
	if err := s.auditDB.UpsertNotifyChannel(db.NotifyChannel{ID: "hook",
		Owner: db.InstallOwner, Kind: notify.KindWebhook, Enabled: 1,
		Config: `{"urls":["tgram://t/1"]}`, Events: "[]", Routers: "[]"}); err != nil {
		t.Fatal(err)
	}

	if err := s.channelCanSendReports("mail"); err != nil {
		t.Errorf("a usable mail channel was refused: %v", err)
	}
	for _, tc := range []struct{ name, id string }{
		{"nothing chosen", ""},
		{"a channel that does not exist", "gone"},
		{"a webhook", "hook"},
		{"another user's mail channel", "mine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.channelCanSendReports(tc.id); err == nil {
				t.Error("accepted at write time, so the operator is told it saved and " +
					"finds out at the end of the month that it did not send")
			}
		})
	}
}
