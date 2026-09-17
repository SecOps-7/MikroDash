package server

// `GET /api/settings`, staged under the port's prefix.
//
// ── THE WRITE IS DELIBERATELY NOT HERE ─────────────────────────────────────
//
// `store.SettingsUpdate` and `store.SaveSettings` are ported, tested and
// byte-compared against the live writer — and wiring them up now would still be
// wrong, because of what the OTHER process does.
//
// `src/settings.js` caches the whole settings object in `_cache` at first load
// and never re-reads the file. So during coexistence:
//
//   - a write from this side is invisible to the running Node app until it
//     restarts, and every page it serves keeps showing the old values; and
//   - the next save from THAT side writes `{ ...its cache, ...its updates }`,
//     which silently reverts every change this side made.
//
// The second is the dangerous one: the operator changes something here, sees it
// take effect here, and it disappears the next time anyone touches Settings in
// the live app. Nothing logs, and nothing on either screen explains it.
//
// This is the same family as the two decisions already recorded in
// the port record — the backup scheduler and the routers background pool — and it
// lands the same way: the write is a CUTOVER step. The read is not, and is here.

import (
	"log"
	"net/http"
	"os"

	"mikrodash/internal/store"
)

const settingsPrefix = "/api/settings"

func (s *Server) registerSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET "+settingsPrefix, s.settingsGet)
	s.registerSettingsWrite(mux)
}

// settingsGet answers with the caller's view of the settings.
//
// TWO PAYLOADS, ONE ROUTE, and which one is decided by a permission rather than
// by the route — matching the original, and for a reason its comment gives: "an
// administrator whose grant is held through a group has role 'viewer' on their
// user record", so keying on the stored role would have handed the reduced
// payload to a real administrator. The question asked is what they CAN do.
func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}

	writeJSON(w, s.settingsPayload(sess))
}

// settingsPayload is the decision, separated from the transport.
//
// NOT AN ABSTRACTION FOR ITS OWN SAKE: the handler validates a cookie, so a test
// that went through it would need a session store to exercise a rule that has
// nothing to do with sessions. With the choice here, the test drives the SAME
// code the handler runs — an earlier version of the test reimplemented this
// logic beside it, which meant a mutation to the handler would not have been
// caught at all.
// aiSystemPromptDefaultKey carries the built-in prompt to the Settings page.
//
// Spelled once here and read once in the browser; it is a payload key rather
// than a setting, so it lives with the payload rather than in the tables.
const aiSystemPromptDefaultKey = "aiSystemPromptDefault"

func (s *Server) settingsPayload(sess *Session) store.Settings {
	raw, err := s.store.Settings()
	if err != nil {
		// A MISSING FILE IS NOT AN ERROR. `load()` catches its own read failure
		// and starts from the defaults — "File missing or corrupt — start from
		// defaults" — so a fresh install serves a full payload rather than a 500.
		if !os.IsNotExist(err) {
			log.Printf("[settings] reading settings.json: %v", err)
		}
		raw = store.Settings{}
	}

	merged, _ := store.Merge(raw, os.LookupEnv, s.store)

	if s.maySeeAllSettings(sess) {
		out := merged.Public()
		// ── THE DEFAULT PROMPT RIDES ALONG, DERIVED AND NEVER STORED ────────
		//
		// Settings pre-fills the prompt box with it and Reset to Default
		// restores it, so the browser needs the same text the server falls back
		// to. Injected HERE rather than inside `Public()` for the reason
		// `pageSettingsFor` exists: that function is compared against seven
		// recorded payloads, and a derived key added inside it fails all seven
		// while describing nothing that changed about disclosure.
		//
		// It is not a setting. There is no default for it in the tables, the
		// save collector never sends it back (it is not in the form map), and
		// `Merge` would drop it from a settings.json that somehow acquired one.
		//
		// ADMINISTRATORS ONLY, by falling on this side of the branch: a viewer
		// has no Settings page to render it into.
		out[aiSystemPromptDefaultKey] = AIDefaultSystemPrompt
		return out
	}
	return merged.ViewerPublic()
}

// pageSettingsFor is the `settings:pages` payload: the visibility projection,
// with the derived flags added FIRST.
//
// ── WHY THE DERIVATION WRAPS THE PROJECTION ─────────────────────────────────
//
// `store.PageSettings` copies only the keys its source has, and a derived key is
// in no source by definition — so computing it inside would mean teaching that
// function about features, and it is compared against seven recorded payloads
// that predate this one. Wrapping leaves those recordings exact.
//
// ── AND WHY IT IS A FUNCTION RATHER THAN THREE WRAPPED CALLS ────────────────
//
// Three sites send this payload: the connect replay in ws.go, and the save and
// reset broadcasts in settings_write_api.go. A derived flag added at two of them
// is worse than one added at none — the browser would be told the feature is
// usable on connect and not after a save, so the page would appear and then
// vanish when the operator pressed Save, which reads as the save having failed.
func pageSettingsFor(s store.Settings) store.Settings {
	return store.PageSettings(store.WithAIReady(s))
}

// maySeeAllSettings answers `Rbac.can(session, 'system:settings')`.
//
// FAILS CLOSED. An unanswerable question yields the VIEWER payload, not the
// administrator's: the reduced view is the safe answer to "I do not know who
// this is", and it still renders a working dashboard.
func (s *Server) maySeeAllSettings(sess *Session) bool {
	if sess.AuthMode == "none" {
		// One local operator with full reach, the same short circuit rbac.js
		// makes and that auditScope documents.
		return true
	}
	if s.rbac == nil {
		return false
	}
	ok, err := s.rbac.Can(s.userIDFor(sess.Username), "system:settings", "")
	if err != nil {
		log.Printf("[settings] permission check failed for %s: %v", sess.Username, err)
		return false
	}
	return ok
}
