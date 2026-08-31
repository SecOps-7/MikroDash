package server

import (
	"encoding/json"
	"log"
	"net/http"
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
// ── WHY IT IS SAFE DURING COEXISTENCE, UNLIKE THE ALERTER ──────────────────
//
// `PORT-QUEUE.md` blocker 5 keeps the notification transports unwired while Node
// runs, because both apps would evaluate the same conditions and send every
// alert twice. That reasoning does not reach this route: a test is ONE message,
// sent because a human pressed a button in one of the two apps. Nothing here is
// triggered by a condition both engines watch.
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
		cfg, to := smtpFromSettings(settings)
		if cfg.Host == "" || cfg.From == "" || to == "" {
			// Left to `Precondition` inside TestChannel rather than answered
			// here, so the refusal wording is the live module's own and stays in
			// one place. This branch only avoids building a mailer that cannot
			// be used.
			mail = nil
		} else {
			mail = func(title, text string) error {
				return mailer.Send(cfg, mailer.Message{
					To: []string{to}, Subject: title, Text: text,
				})
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
func smtpFromSettings(cfg notify.Settings) (mailer.Config, string) {
	str := func(k string) string { v, _ := cfg[k].(string); return v }
	port := 0
	switch p := cfg["smtpPort"].(type) {
	case float64:
		port = int(p)
	case int:
		port = p
	}
	secure, _ := cfg["smtpSecure"].(bool)
	return mailer.Config{
		Host: str("smtpHost"), Port: port, Secure: secure,
		User: str("smtpUser"), Pass: str("smtpPass"), From: str("smtpFrom"),
	}, str("smtpTo")
}
