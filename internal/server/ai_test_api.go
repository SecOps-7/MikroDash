package server

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"mikrodash/internal/aiprovider"
	"mikrodash/internal/safe"
	"mikrodash/internal/store"
)

// `POST /api/settings/test-ai` — the Test button on Settings → AI Agent.
//
// ── GATED THE SAME THREE WAYS AS THE NOTIFICATION TEST, FOR THE SAME REASON ─
//
// Global admin, its own rate limiter, and a bounded body. This is the second
// route where the CALLER CHOOSES THE DESTINATION the server connects out to, and
// `test_notif_api.go` carries the argument in full. Nothing about a model
// endpoint weakens it: private addresses are deliberately reachable here, so the
// gate cannot be "where does it point" and has to be "who may ask".
//
// ── WHY A TEST BUTTON AT ALL ────────────────────────────────────────────────
//
// Three things can be wrong and only one of them is visible from the page: the
// endpoint may not answer, the key may be refused, and THE MODEL NAME MAY NOT
// EXIST. The last is the one operators actually hit, because model names share
// no vocabulary between providers — `gpt-4o`, `llama3.1:8b` and
// `mistral-small-latest` name the same kind of thing and none is guessable from
// another. Without this button the first sign of a typo is an assistant that
// answers nothing, on a page with no other diagnosis.
//
// ── THE CREDENTIALS COME FROM THE FORM, NOT ONLY FROM DISK ──────────────────
//
// An operator configuring this for the first time has typed an endpoint, a key
// and a model and has not saved yet. A Test that read only the stored settings
// would test the PREVIOUS configuration and report success for one that was
// never tried — which is worse than not offering the button. See aiConfigFor.
func (s *Server) registerAITest(mux *http.ServeMux) {
	// TEN A MINUTE, matching the notification test beside it. Reading a form is
	// cheap; an outbound connection to a host somebody named in the request is
	// not, and a model endpoint can hold one open for a long time before it
	// answers.
	test := newRateLimiter(10, time.Minute).limit
	mux.HandleFunc("POST /api/settings/test-ai", test(s.testAI))
}

func (s *Server) testAI(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if !s.isGlobalAdmin(sess) {
		writeJSONErr(w, http.StatusForbidden, "Administrator access required")
		return
	}
	if s.store == nil {
		writeJSONErr(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}

	// BOUNDED. The header block is operator text and is capped per field on save,
	// but this route reads the form directly — so without a limit on the whole
	// body a caller could make this process buffer an arbitrary amount before any
	// per-field cap ran.
	var body map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeJSONErr(w, http.StatusBadRequest, "malformed body")
		return
	}

	stored, err := s.mergedSettings()
	if err != nil {
		writeJSONErr(w, http.StatusInternalServerError, "could not read the settings")
		return
	}

	cfg := aiConfigFor(body, stored)
	if err := aiprovider.TestEndpoint(r.Context(), cfg.Client(), cfg); err != nil {
		// SANITISED ON BOTH PATHS. A transport error carries the host and port of
		// whatever the operator pointed us at, and this process's log is not a
		// place for either. The same rule `safe.Message` exists for, and the
		// reason the notification route applies it twice as well.
		msg := safe.Message(err.Error())
		log.Printf("[test-ai] %s", msg)
		w.WriteHeader(http.StatusInternalServerError)
		writeJSON(w, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// aiConfigFor overlays what the operator has typed onto what is stored.
//
// ── TWO GUARDS, AND THEY ARE NOT INTERCHANGEABLE ────────────────────────────
//
// The same distinction `notify.MergeForAdminTest` carries, for the same reason.
// A TEXT field falls back to the stored value when the form sends it empty, so a
// box the operator never touched tests what is saved. A BOOLEAN overrides even
// when false, because "off" is a value somebody sets deliberately — a truthiness
// guard would make it impossible to test with TLS verification turned back ON
// without saving first.
//
// ── AND THE MASK IS NOT A KEY ───────────────────────────────────────────────
//
// `populateSettings` renders a configured credential as eight bullets and the
// form hands whatever is in the box straight back. Sending that to the provider
// would produce an authentication failure the operator cannot explain, because
// the page shows the key as configured. `store.IsMasked` is the same test the
// save path uses to drop it, applied here to fall back instead.
func aiConfigFor(body map[string]any, stored store.Settings) aiprovider.Config {
	str := func(k string) string { v, _ := stored[k].(string); return v }

	text := func(key string) string {
		if v, ok := body[key].(string); ok && v != "" && !store.IsMasked(v) {
			return v
		}
		return str(key)
	}
	boolean := func(key string) bool {
		if v, ok := body[key]; ok {
			// The server's rule everywhere else: a real `true` or the string
			// "true", and nothing else. `1` and "on" are both false.
			return v == true || v == "true"
		}
		b, _ := stored[key].(bool)
		return b
	}
	number := func(key string) int {
		if v, ok := body[key].(float64); ok {
			return int(v)
		}
		switch v := stored[key].(type) {
		case float64:
			return int(v)
		case int:
			return v
		}
		return 0
	}

	return aiprovider.Config{
		BaseURL:     text("aiBaseUrl"),
		APIKey:      text("aiApiKey"),
		Model:       text("aiModel"),
		Headers:     text("aiHeaders"),
		TimeoutMs:   number("aiTimeoutMs"),
		TLSInsecure: boolean("aiTlsInsecure"),
		MaxTokens:   number("aiMaxTokens"),
	}
}
