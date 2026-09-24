package server

// The alert send path reads settings the way the Settings page and the Test
// buttons do. Three faults, found investigating issue #130, each pinned below.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"testing"
	"time"

	"mikrodash/internal/alert"
	"mikrodash/internal/alertdispatch"
	"mikrodash/internal/collect"
	"mikrodash/internal/notify"
)

// A SETTINGS SAVE REACHES BOTH HALVES OF ALERTING, and so does a reset.
//
// Nothing called `refreshAlertSettings`, so the evaluator kept its startup
// thresholds and the dispatcher its startup channels: turning Telegram off kept
// sending to it until a restart.
func TestASettingsSaveReachesTheAlerts(t *testing.T) {
	s, mux, _ := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, `{}`)
	s.alerts = s.buildAlertWire()
	s.dispatch = s.buildAlertDispatch(true)
	r := alert.Router{ID: "r-1", AlertsEnabled: true}

	if got := s.alerts.Evaluate(r, "system:update", collect.SystemPayload{CPULoad: 60}); len(got) != 0 {
		t.Fatalf("60%% fired %v under the default threshold", got)
	}
	// ── WHAT IS LEFT TO PROPAGATE ────────────────────────────────────────
	//
	// This used to save a TRANSPORT and check the dispatcher saw it. No
	// transport lives in the settings any more: Telegram, Pushbullet and ntfy
	// became notification channels, and the mail server followed when reports
	// began subscribing to an email channel. What a settings save must still
	// reach the live evaluator is the THRESHOLD, which is the half that decides
	// when an event exists at all.
	w := settingsPost(mux, `{"alertCpuThreshold":50}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("save: status %d: %s", w.Code, w.Body.String())
	}
	if got := s.alerts.Evaluate(r, "system:update", collect.SystemPayload{CPULoad: 61}); len(got) != 1 {
		t.Errorf("61%% fired %v after the threshold was saved as 50; the evaluator kept "+
			"its startup settings", got)
	}

	if w := settingsPost(mux, `{"_reset":true}`, authed); w.Code != http.StatusOK {
		t.Fatalf("reset: status %d: %s", w.Code, w.Body.String())
	}
	if notify.HasConfigured(s.dispatch.Recipients("", nil)[0].Settings) {
		t.Error("the dispatcher still has a channel after a reset")
	}
}

// A USER RECIPIENT IS READ THE WAY THE MY ALERTS TEST BUTTON READS IT: the
// allowlist, the credentials decrypted, and an email opt-in folded onto the
// install's mail server. The raw row was handed to the sender.
func TestAUserRecipientIsReadLikeItsTestButton(t *testing.T) {
	s := alertSettingsServer(t, `{}`)
	sealed, err := s.store.Encrypt("123:abc")
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{
		"telegramEnabled": true, "telegramBotToken": sealed, "telegramChatId": "42",
		"emailEnabled": true, "emailTo": "me@example.com",
		// Not on the allowlist: a row must not choose its own mail server.
		"smtpHost": "elsewhere.example",
	}
	install := notify.Settings{"smtpHost": "mail.example", "smtpFrom": "md@example.com",
		"smtpTo": "admin@example.com"}

	got := s.userRecipientSettings(row, install)
	if got["telegramBotToken"] != "123:abc" {
		t.Errorf("token %q reached the sender; it must be decrypted", got["telegramBotToken"])
	}
	if got["smtpHost"] != "mail.example" {
		t.Errorf("smtpHost %q; the row's own must be dropped for the install's", got["smtpHost"])
	}
	if got["smtpTo"] != "me@example.com" {
		t.Errorf("smtpTo %q; a user's alerts go to the user", got["smtpTo"])
	}
	if ch := notify.Channels(got); !reflect.DeepEqual(ch, []notify.Channel{notify.Telegram, notify.SMTP}) {
		t.Errorf("channels %v, want telegram and smtp", ch)
	}

	if s.alertMailer(got) == nil {
		t.Error("no mailer for settings that name a mail server and an address")
	}
	if s.alertMailer(notify.Settings{"smtpTo": "me@example.com"}) != nil {
		t.Error("a mailer was built with no mail server")
	}
}

// AN EMAIL CHANNEL REALLY DIALS THE MAIL SERVER.
//
// It used to build the mailer from the install-wide `smtp*` settings. Those are
// gone: a mail server is an SMTP CHANNEL now, and reports subscribe to one. The
// behaviour under test is unchanged and is the one that matters — a channel
// whose kind is smtp must reach `internal/mailer` and open a socket, not fail
// quietly the way a webhook with no scheme would.
func TestAnEmailAlertDialsTheMailServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	dialled := make(chan struct{}, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		dialled <- struct{}{}
		_, _ = c.Write([]byte("554 not today\r\n"))
		_ = c.Close()
	}()

	port := ln.Addr().(*net.TCPAddr).Port
	s, _, _ := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, `{}`)
	s.dispatch = s.buildAlertDispatch(true)

	// The recipient `channelRecipients` would build for an SMTP channel.
	spec := notify.DecodeChannel("c1", "Email", notify.KindSMTP, true,
		fmt.Sprintf(`{"host":"127.0.0.1","port":%d,"from":"md@example.com",`+
			`"to":"ops@example.com"}`, port), `["high_cpu"]`, `[]`)
	rec := alertdispatch.Recipient{ID: "chan:c1", Settings: spec.Settings}
	s.dispatch.Deliver(context.Background(), &rec, "cpu",
		alertdispatch.Message{Title: "T", Body: "B"})

	select {
	case <-dialled:
	case <-time.After(3 * time.Second):
		t.Error("an email channel never dialled the mail server; the dispatcher has no mailer")
	}
}
