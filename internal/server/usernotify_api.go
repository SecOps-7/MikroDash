package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/mailer"
	"mikrodash/internal/notify"
	"mikrodash/internal/safe"
)

const userNotifyPath = "/api/user-notify"

// registerUserNotify wires the My Alerts tab.
//
// TWO LIMITERS, and the difference is deliberate on the live side
// (`_userNotifyLimiter` and `_userNotifyTestLimiter` are separate instances).
// Reading and saving a form is cheap; a TEST makes this server connect to a host
// the USER chose, so it gets a tenth of the budget. A shared limiter would let
// someone spend the whole allowance on outbound requests.
func (s *Server) registerUserNotify(mux *http.ServeMux) {
	rw := newRateLimiter(60, time.Minute).limit
	test := newRateLimiter(10, time.Minute).limit

	mux.HandleFunc("GET "+userNotifyPath, rw(s.userNotifyGet))
	mux.HandleFunc("POST "+userNotifyPath, rw(s.userNotifySave))
	mux.HandleFunc("POST "+userNotifyPath+"/test-notification", test(s.userNotifyTest))
}

// requireUserNotify is the pair of gates every one of these endpoints passes.
//
// THE INSTALL SWITCH SHIPS OFF, and the live comment says why: "per-user ntfy
// and SMTP let the *user* choose a destination host, so enabling this widens
// what an ordinary account can make the server connect to." It is not a
// convenience flag; it is the boundary of what an unprivileged account can aim
// this process at.
//
// The two refusals are DIFFERENT STATUSES because they are different situations.
// 403 means the install has switched the feature off. 400 means there is no
// person to own a personal channel — `authMode: none` has no identity at all, so
// there is nothing to serve rather than something being denied.
func (s *Server) requireUserNotify(w http.ResponseWriter, r *http.Request) (*Session, string, bool) {
	if !s.userNotifyEnabled() {
		writeJSONErr(w, http.StatusForbidden,
			"Per-user notification channels are disabled for this install")
		return nil, "", false
	}
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return nil, "", false
	}
	uid := s.userIDFor(sess.Username)
	if uid == "" {
		writeJSONErr(w, http.StatusBadRequest,
			"Per-user notification channels require user accounts")
		return nil, "", false
	}
	return sess, uid, true
}

// userNotifyEnabled reads the install-wide switch.
//
// ABSENT MEANS OFF. A settings file written before this feature existed has no
// such key, and defaulting it on would enable the widest thing in this file on
// every install that upgraded.
func (s *Server) userNotifyEnabled() bool {
	if s.store == nil {
		return false
	}
	cfg, err := s.mergedSettings()
	if err != nil {
		return false
	}
	on, _ := cfg["userNotifyEnabled"].(bool)
	return on
}

// userNotifyGet answers with the user's own channels, credentials masked.
func (s *Server) userNotifyGet(w http.ResponseWriter, r *http.Request) {
	_, uid, ok := s.requireUserNotify(w, r)
	if !ok {
		return
	}
	stored, err := s.userNotifyLoad(uid)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, notify.Public(stored))
}

// userNotifySave stores the user's channels.
func (s *Server) userNotifySave(w http.ResponseWriter, r *http.Request) {
	sess, uid, ok := s.requireUserNotify(w, r)
	if !ok {
		return
	}
	body, ok := s.readUserNotifyBody(w, r)
	if !ok {
		return
	}
	stored, err := s.userNotifyRaw(uid)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}

	next, err := notify.Merge(body, stored, s.encryptSetting)
	if err != nil {
		// A malformed address is the CALLER's mistake to fix, not a server fault,
		// so it must not read as one. Sanitised even though the only error Merge
		// returns today is a fixed sentence: the rule here is that no raw error
		// value reaches a browser, and an exception granted on today's contents
		// stops holding the moment that function grows a second failure.
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}
	if s.auditDB != nil {
		if err := s.auditDB.SetUserNotifyConfig(uid, next); err != nil {
			writeJSONErrFrom(w, http.StatusInternalServerError, err)
			return
		}
	}

	// THE BODY CARRIES CREDENTIALS and the trail records only that a destination
	// changed. Nothing from `body` reaches the audit record — not redacted, not
	// truncated: absent.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "account.notify", TargetType: "user",
		TargetID: uid, TargetName: sess.Username,
		Note: "personal notification channels updated",
	})
	writeJSON(w, map[string]any{"ok": true, "config": notify.Public(next)})
}

// userNotifyTest sends one channel's test notification.
func (s *Server) userNotifyTest(w http.ResponseWriter, r *http.Request) {
	_, uid, ok := s.requireUserNotify(w, r)
	if !ok {
		return
	}
	body, ok := s.readUserNotifyBody(w, r)
	if !ok {
		return
	}
	channel, _ := body["channel"].(string)
	if channel == "" {
		writeJSONErr(w, http.StatusBadRequest, "channel is required")
		return
	}

	// DECRYPTED here, unlike the save path: a test actually sends, so it needs the
	// credential rather than the ciphertext.
	stored, err := s.userNotifyLoad(uid)
	if err != nil {
		writeJSONErrFrom(w, http.StatusInternalServerError, err)
		return
	}
	settings, err := notify.MergeForTest(body, stored, channel)
	if err != nil {
		writeJSONErr(w, http.StatusBadRequest, safe.Message(err.Error()))
		return
	}

	ch := notify.Channel(channel)
	var mail notify.Mailer
	if channel == "email" {
		// Email is the INSTALL's mail server plus THIS user's address, so a test
		// composes the same thing delivery would — including an address typed but
		// not yet saved. `notify` still calls the channel "smtp" internally;
		// "email" is what it is called to a user, who never sees a mail server.
		cfg, from, configured := s.smtpConfig()
		if !configured {
			writeJSONErr(w, http.StatusBadRequest,
				"No mail server is configured for this install")
			return
		}
		to, _ := settings["emailTo"].(string)
		if to == "" {
			writeJSONErr(w, http.StatusBadRequest, "Enter an email address first")
			return
		}
		ch = notify.SMTP
		settings["smtpEnabled"] = true
		settings["smtpHost"] = cfg.Host
		settings["smtpFrom"] = from
		settings["smtpTo"] = to
		mail = func(title, text string) error {
			// ONE recipient, and it is this user's own address — never a list, and
			// never anybody else's. A per-user test that could name another
			// mailbox would be a way to send mail as the install.
			return mailer.Send(cfg, mailer.Message{
				To: []string{to}, Subject: title, Text: text,
			})
		}
	}

	if err := notify.TestChannel(r.Context(), notify.DefaultClient, settings, ch, mail); err != nil {
		// 500 WITH A BODY, matching the live route — which returns
		// `res.status(500).json({ ok: false, error: sanitizeErr(e) })`.
		//
		// (An earlier draft of this file said "200 with ok:false, matching the
		// live route". That reasoning belongs to the REPORTS Send-now route,
		// which does answer 200; this one does not. The live source settles it.)
		//
		// SANITISED, and here it is not a formality. The failure can be a
		// transport error naming the host the USER chose — so an unsanitised body
		// hands an ordinary account a probe: point ntfy at an internal address,
		// press Test, and read the connection error.
		//
		// ── BOTH HALVES ARE CLOSED AS OF 2026-08-29 ───────────────────────
		//
		// This paragraph has been wrong twice and the history is worth keeping,
		// because each version was plausible.
		//
		// It first said `safe.Message` "redacts addresses and paths, which is
		// what closes that". Measured on 2026-08-28: it redacted IPv4 addresses
		// and paths and had NO HOSTNAME RULE, so
		//
		//   http://intranet-wiki.corp.local/x  ->  "lookup intranet-wiki.corp.local
		//                                           on [addr]: no such host"
		//
		// still told an ordinary account whether an internal NAME resolves. The
		// IP probe was closed; the name probe was not.
		//
		// It was not fixed here unilaterally, because `safe.Message` is shared
		// with every route and mirrors a live function — widening it was a
		// decision about the live app. Filed in ../MikroDash/ToDo.md, fixed
		// upstream in `51aac86`, and `safe.Message` now carries the hostname rule
		// AFTER the email rule (ordering matters: run first it half-matches an
		// address into `user@[host]`). `internal/safe` pins both.
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"ok": false, "error": safe.Message(err.Error())})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// readUserNotifyBody decodes a bounded JSON object.
//
// BOUNDED because the fields are operator text and the caps in `notify` apply
// per FIELD — without a limit on the whole body, a caller could still make the
// server buffer an arbitrary amount before any of them ran.
func (s *Server) readUserNotifyBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed body")
		return nil, false
	}
	if body == nil {
		body = map[string]any{}
	}
	return body, true
}

// userNotifyRaw reads a user's stored channels through the allowlist, leaving
// credentials as stored.
//
// `notify.Pick` is the security boundary, not tidying: the blob is a database
// row and the sender decides where to send by inspecting FIELD NAMES, so an
// injected `smtpHost` would point one user's alerts at a server of somebody
// else's choosing.
func (s *Server) userNotifyRaw(userID string) (notify.Settings, error) {
	if s.auditDB == nil {
		return notify.Defaults(), nil
	}
	raw, err := s.auditDB.UserNotifyConfig(userID)
	if err != nil {
		return nil, err
	}
	stored := notify.Settings{}
	for k, v := range raw {
		stored[k] = v
	}
	// No decryptor: a SAVE must keep the ciphertext it did not touch.
	return notify.Pick(stored, nil), nil
}

// userNotifyLoad is userNotifyRaw with the credentials decrypted, for a path
// that is about to send.
func (s *Server) userNotifyLoad(userID string) (notify.Settings, error) {
	if s.auditDB == nil {
		return notify.Defaults(), nil
	}
	raw, err := s.auditDB.UserNotifyConfig(userID)
	if err != nil {
		return nil, err
	}
	stored := notify.Settings{}
	for k, v := range raw {
		stored[k] = v
	}
	return notify.Pick(stored, s.decryptSetting), nil
}

// decryptSetting turns stored ciphertext back into a credential.
//
// A FAILURE READS AS EMPTY rather than propagating: one unreadable credential
// should cost that channel, not the whole page. The channel then reports "not
// configured", which is what an operator can act on.
func (s *Server) decryptSetting(b64 string) string {
	if s.store == nil || b64 == "" {
		return ""
	}
	// The explicit check is BELT AND BRACES today and deliberately kept:
	// `store.Decrypt` returns "" on all five of its error paths, so dropping it
	// is an equivalent mutation and mutation testing says so. It is retained
	// because that is a property of the DECRYPTOR, not a contract — a future
	// version returning partial plaintext alongside an authentication failure
	// would make this the only thing standing between corrupt bytes and an
	// outbound request.
	v, err := s.store.Decrypt(b64)
	if err != nil {
		return ""
	}
	return v
}

// encryptSetting is the other half. A missing store is an ERROR here, not an
// empty string: writing a credential in the clear because encryption was
// unavailable is worse than refusing the save.
func (s *Server) encryptSetting(v string) (string, error) {
	if s.store == nil {
		return "", errors.New("settings storage is unavailable")
	}
	return s.store.Encrypt(v)
}
