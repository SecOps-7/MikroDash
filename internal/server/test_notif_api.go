package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"mikrodash/internal/mailer"
	"mikrodash/internal/notify"
	"mikrodash/internal/safe"
)

// `POST /api/settings/test-notification` — the four Test buttons on Settings.
//
// ── IT SENDS, AND THAT IS WHY IT IS GATED THREE WAYS ───────────────────────
//
// Global admin, a rate limiter of its own, and a bounded body. The live route
// has all three (`Rbac.requireGlobalAdmin`, `_testNotifLimiter` at 10/minute),
// and the reason is that this endpoint makes the server connect OUT to a host
// named in the request — so it is the one settings route where a caller chooses
// the destination.
//
// ── NOT GATED BY -alert-dispatch ────────────────────────────────────────────
//
// That switch decides whether alert CONDITIONS send. A test is one message, sent
// because a human pressed a button, so it is answered whatever the switch says.
//
// ── THE CREDENTIALS COME FROM THE FORM, NOT ONLY FROM DISK ─────────────────
//
// The live comment: "Include any credentials the user has currently typed so
// Test works without requiring a Save first." That is what `MergeForAdminTest`
// does, and its two guards are not interchangeable — see its header.
func (s *Server) registerTestNotification(mux *http.ServeMux) {
	// TEN A MINUTE, matching `_testNotifLimiter`, and separate from every other
	// settings limiter for the same reason `userNotifyTest` has its own: reading
	// a form is cheap, and an outbound connection is not.
	test := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("POST /api/settings/test-notification", test(s.testNotification))
}

func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if !s.isGlobalAdmin(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}

	// BOUNDED. The fields are operator text and `MergeForAdminTest` caps them per
	// field, but without a limit on the whole body a caller could still make this
	// process buffer an arbitrary amount before any cap ran.
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed body")
		return
	}
	channel, _ := body["channel"].(string)
	if channel == "" {
		writeJSONErr(w, http.StatusBadRequest, "channel is required")
		return
	}

	// MERGED, not raw: the credentials in settings.json are sealed, and a Test
	// button that posts the ciphertext gets HTTP 404 from Telegram.
	stored, err := s.mergedSettings()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the settings")
		return
	}
	settings := notify.MergeForAdminTest(body, notify.Settings(stored))

	// THE MAILER IS BUILT FROM THE MERGED SETTINGS, not from `s.smtpConfig()`.
	// That difference is the whole point of the merge: `smtpConfig` reads the
	// file, so testing a mail server the operator has typed but not saved would
	// silently test the OLD one and report success for a configuration that was
	// never tried.
	var mail notify.Mailer
	if channel == string(notify.SMTP) {
		cfg, who := smtpFromSettings(settings)
		if cfg.Host == "" || cfg.From == "" ||
			len(who.To)+len(who.Cc)+len(who.Bcc) == 0 {
			// Left to `Precondition` inside TestChannel rather than answered
			// here, so the refusal wording is the live module's own and stays in
			// one place. This branch only avoids building a mailer that cannot
			// be used.
			mail = nil
		} else {
			// THE TEST REACHES EVERYONE THE CHANNEL WOULD, To, Cc and Bcc alike.
			// A test that only mailed To would report a channel working while
			// half its recipients never receive anything from it.
			mail = func(title, text string) error {
				who.Subject, who.Text = title, text
				return mailer.Send(cfg, who)
			}
		}
	}

	if err := notify.TestChannel(r.Context(), notify.DefaultClient,
		settings, notify.Channel(channel), mail); err != nil {
		// LOGGED WITHOUT THE CREDENTIALS. The live route logs `e.message`; the
		// same sanitiser is applied here as well as on the response, because a
		// transport error can carry the host, the account or the token and this
		// process's log is not a place for any of them.
		log.Printf("[test-notification] %s: %s", channel, safe.Message(err.Error()))
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"ok": false, "error": safe.Message(err.Error())})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// smtpFromSettings builds the mail config out of an already-merged settings map.
//
// Separate from `s.smtpConfig()` on purpose: that one reads the file and is
// right for the scheduler, which has no form in front of it. This one must
// honour what the operator typed.
// IT RETURNS THE WHOLE RECIPIENT SET, not one To string. An SMTP channel names
// To, Cc and Bcc separately, and an alert that only ever honoured To would
// silently drop everyone an operator had copied — while the channel's own page
// showed them configured.
func smtpFromSettings(cfg notify.Settings) (mailer.Config, mailer.Message) {
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	// A STRING IS A REAL CASE, and leaving it out was a live bug.
	//
	// `notify.DecodeChannel` writes an SMTP channel's port with `strconv.Itoa`,
	// because the flat `smtp*` keys it mirrors were form values and those are
	// strings. Parsing only the numeric forms left every SMTP channel on port 0,
	// so a channel configured for 465 dialled somewhere else entirely — silently,
	// since the connection error reads the same as an unreachable server. Caught
	// by TestAnEmailAlertDialsTheMailServer once it was pointed at a channel.
	port := 0
	switch p := cfg["smtpPort"].(type) {
	case float64:
		port = int(p)
	case int:
		port = p
	case string:
		port, _ = strconv.Atoi(p)
	}
	secure, _ := cfg["smtpSecure"].(bool)
	return mailer.Config{
		Host: str("smtpHost"), Port: port, Secure: secure,
		User: str("smtpUser"), Pass: str("smtpPass"), From: str("smtpFrom"),
	}, mailer.Message{
		To:  splitList(str("smtpTo")),
		Cc:  splitList(str("smtpCc")),
		Bcc: splitList(str("smtpBcc")),
	}
}
