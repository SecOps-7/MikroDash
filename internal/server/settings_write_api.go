package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"mikrodash/internal/audit"
	"mikrodash/internal/store"
)

// `POST /api/settings`.
//
// ── THE WRITE'S RECORDED BLOCKER IS GONE ────────────────────────────────────
//
// `settings_api.go` explains at length why the read shipped and the write did
// not: `src/settings.js` caches the whole object at first load and never
// re-reads the file, so during coexistence a write from this side was invisible
// to the running Node app AND silently reverted by its next save. That reasoning
// held while both processes owned the file. The operator lifted the strangler
// rule (the port record, 2026-08-25) — everything remaining cuts over — so Go owns
// settings and the hazard does not arise.
//
// ── FOUR THINGS HAPPEN HERE AND THE ORDER MATTERS ───────────────────────────
//
//  1. `_reset` returns EARLY, and is therefore audited on its own. The live
//     comment says why in as many words: "Recorded here, not after the normal
//     save below: this branch returns early, so a single hook at the end of the
//     handler would miss the one settings write that replaces the entire file."
//  2. The previous settings are read BEFORE the write, or "before" is just
//     "after" again.
//  3. The audit entry is deliberately ASYMMETRIC — `before` is the whole
//     previous object and `after` is only the updates. That is safe because
//     `audit.Diff` walks `after`'s keys only, so a partial update does not
//     report every untouched field as removed.
//  4. The poll re-tune reads `updates` for the DECISION and the merged file for
//     the VALUE. See `collection.PollRetunes`.

func (s *Server) registerSettingsWrite(mux *http.ServeMux) {
	// The live route has no rate limiter of its own; it is gated on being a
	// global administrator, which is a much narrower door than a budget.
	mux.HandleFunc("POST "+settingsPrefix, s.settingsSave)
}

func (s *Server) settingsSave(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if !s.maySaveSettings(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}

	var body map[string]any
	// `req.body || {}` — an unreadable body is an empty one, which validates to
	// no updates and saves nothing. It is not an error on the live side.
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}

	updates, reset := store.SettingsUpdate(body)

	if reset {
		// AUDITED HERE, not at the end. This branch returns early, and it is the
		// one write that replaces the entire file.
		s.httpRecorder(r, sess).Record(audit.Event{
			Action: "settings.reset", TargetType: "settings",
			Note: "all settings restored to defaults",
		})
		// BOTH arguments are the defaults, and that is not a shortcut: the live
		// `Settings.save(DEFAULTS)` passes them as the updates too, so every
		// credential field counts as explicitly written and its preserved
		// ciphertext is dropped. A reset CLEARS the credentials, deliberately.
		if err := s.writeSettings(store.Defaults(), store.Defaults()); err != nil {
			log.Printf("[settings] reset: %v", err)
			writeJSONErr(w, http.StatusInternalServerError, "could not save the settings")
			return
		}
		s.hub.BroadcastAll("settings:pages", store.PageSettings(store.Defaults()))
		writeJSON(w, map[string]any{"ok": true, "requiresRestart": false})
		return
	}

	// BEFORE the write, or "before" is just "after" again.
	prev, err := s.mergedSettings()
	if err != nil {
		log.Printf("[settings] read: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not read the settings")
		return
	}

	next := make(store.Settings, len(prev)+len(updates))
	for k, v := range prev {
		next[k] = v
	}
	for k, v := range updates {
		next[k] = v
	}
	if err := s.writeSettings(next, updates); err != nil {
		log.Printf("[settings] save: %v", err)
		writeJSONErr(w, http.StatusInternalServerError, "could not save the settings")
		return
	}

	// ASYMMETRIC ON PURPOSE: the whole previous object against only the changes.
	// `audit.Diff` walks `after`'s keys, so nothing untouched reads as removed —
	// and credential VALUES never reach the row, in either direction.
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "settings.update", TargetType: "settings",
		Before: prev, After: updates,
	})

	// THE LIVE POLL INTERVALS, applied to every session that is already running.
	//
	// Each router resolves against its OWN overrides inside the manager, which is
	// what keeps #105 true: a key this router pinned is saved to the file and NOT
	// applied here, so a fleet-wide save does not silently un-pin whichever router
	// the operator had configured specially.
	//
	// A nil manager is an ordinary state in tests and before the first router is
	// acquired; there is simply nothing running to re-tune.
	if s.sessions != nil {
		s.sessions.ApplyPollRetunes(updates, next)
	}

	s.hub.BroadcastAll("settings:pages", store.PageSettings(next))
	writeJSON(w, map[string]any{"ok": true, "requiresRestart": false})
}

// maySaveSettings is `Rbac.requireGlobalAdmin`.
//
// GLOBAL, not per-router: these settings are fleet-wide, so a grant held on one
// router confers nothing here. `system:settings` is in `GlobalOnly`, which is
// what makes `rbac.Can` ignore the router argument rather than fail closed on an
// empty one.
//
// A missing resolver REFUSES here, unlike `mayAck`. The asymmetry is deliberate:
// acknowledging an alert is an operator action whose worst case is a cleared
// bell, and locking every operator out of it over an install-wide condition
// would be worse than allowing it. Rewriting the fleet's configuration is not in
// that class.
func (s *Server) maySaveSettings(sess *Session) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if s.rbac == nil || !s.rbac.Available() {
		return false
	}
	ok, err := s.rbac.Can(s.userIDFor(sess.Username), "system:settings", "")
	if err != nil {
		log.Printf("[rbac] system:settings: %v", err)
		return false
	}
	return ok
}

// mergedSettings is `Settings.load()`: the file over the defaults, with the
// environment on top and credentials decrypted.
//
// ── IT EXISTED AND THREE CONSUMERS DID NOT CALL IT ────────────────────────
//
// `store.Settings()` reads settings.json and nothing else — `disclose.go` says
// so outright. The six fields the live app seals (`routerPass`,
// `telegramBotToken`, `pushbulletApiKey`, `smtpUser`, `smtpPass`, `ntfyToken`)
// come out of the raw file as AES-GCM ciphertext, and three call sites used them
// as if they were the secret:
//
//	test_notif_api.go   the four Test buttons  -> Telegram answered HTTP 404
//	alert_wire.go       every dispatched alert -> would have failed identically
//	reports_run.go      scheduled report email -> SMTP auth with a sealed password
//
// `notify.WithInstallMail` would be a fourth, but it is called by NOBODY today —
// the per-user mail path is built and unwired. A hazard for whoever wires it,
// not a bug: hand it a merged map.
//
// FOUND BY SENDING ONE, on 2026-08-29, the first time `-alert-dispatch` ran
// against a real recipient. No gate could have caught it and none did: every
// unit fixture puts plaintext in the map, which is exactly what makes the
// ciphertext path unreachable from a test and reachable from production.
//
// ANYTHING THAT NEEDS A REAL SETTING VALUE CALLS THIS. `store.Settings()` stays
// raw on purpose — the save path needs the file as written.
func (s *Server) mergedSettings() (store.Settings, error) {
	raw, err := s.store.Settings()
	if err != nil {
		return nil, err
	}
	merged, _ := store.Merge(raw, os.LookupEnv, s.store)
	return merged, nil
}

// writeSettings is `Settings.save()`'s file half.
//
// ── `updates` IS NOT `next`, AND PASSING ONE FOR THE OTHER DESTROYS DATA ────
//
// `kept` is the ciphertext that could not be decrypted on the way in, and it has
// to survive the round trip: a credential this process cannot read — written
// under a different `.secret`, or corrupted — must not be blanked by a save that
// never touched it.
//
// `SaveSettings` decides that per field, by asking whether the field is in
// `updates`. An explicit write supersedes preserved ciphertext; anything else
// keeps it. So the two arguments have to be DIFFERENT: `next` is the whole
// merged object and `updates` is only what the operator changed.
//
// The first version of this function passed `next` for both. Every credential
// field then counted as explicitly written, the preserved ciphertext was
// discarded, and the empty string that an unreadable credential merges to was
// written in its place — so changing `topN` silently destroyed a Telegram token
// that was merely unreadable, and the page showed the channel as unconfigured
// with nothing logged. Found by a mutation that "discarded kept" surviving,
// which prompted the test that then failed against the real code.
func (s *Server) writeSettings(next, updates store.Settings) error {
	raw, err := s.store.Settings()
	if err != nil {
		return err
	}
	_, kept := store.Merge(raw, os.LookupEnv, s.store)
	return store.SaveSettings(s.store.Dir, next, updates, kept, s.store)
}
