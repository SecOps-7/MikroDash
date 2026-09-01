package server

// `POST /api/settings`, driven through the REAL mux.
//
// `settings_api_test.go` covers the GET's payload choice — its four tests are
// all read-side. This covers the write: the reset branch, what the audit row
// says, and who is allowed to do it.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/db"
	"mikrodash/internal/hub"
	"mikrodash/internal/store"
)

// settingsWriteServer builds a server whose store points at a throwaway /data,
// with an audit database and a primed session.
func settingsWriteServer(t *testing.T, sess *Session, settingsJSON string) (
	*Server, *http.ServeMux, string,
) {
	t.Helper()
	dir := t.TempDir()
	if settingsJSON == "" {
		settingsJSON = `{"topN":25,"pageWifi":true}`
	}
	for name, body := range map[string]string{
		"settings.json": settingsJSON,
		".secret":       "test-secret-value",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}

	// The audit trail, so the assertions about it are about ROWS rather than
	// about a log line saying the write failed.
	dbDir := t.TempDir()
	h, err := sql.Open("sqlite", filepath.Join(dbDir, "mikrodash.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(alertTestDDL); err != nil {
		t.Fatal(err)
	}
	_ = h.Close()
	d, err := db.Open(dbDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// AN HOUR, NOT A MINUTE. A minute is plenty of wall-clock for one test and is
	// NOT plenty of CPU time when the whole package runs: on 2026-08-26
	// `TestTheLimiterIsRegistered` sent its 130 requests over 61 seconds under
	// load, the cached session expired mid-loop, and every request after that
	// answered 401 — so the limiter never tripped and the test reported "no
	// limiter is registered". Nothing was wrong with the limiter; the suite had
	// simply grown. A TTL that is a function of how busy the machine is makes
	// every auth-dependent test a flake generator.
	auth := NewAuth("", time.Hour)
	auth.cache["tok"] = cached{session: sess, until: time.Now().Add(time.Minute)}

	s := &Server{store: st, auditDB: d, auth: auth, hub: hub.New()}
	mux := http.NewServeMux()
	s.registerSettings(mux)
	return s, mux, dir
}

func settingsPost(mux *http.ServeMux, body, cookie string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", settingsPrefix, strings.NewReader(body))
	req.RemoteAddr = "10.0.0.9:1234"
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// readSettingsFile is what actually landed on disk, before any merge.
func readSettingsFile(t *testing.T, dir string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}
	return out
}

func auditActions(t *testing.T, s *Server) map[string]string {
	t.Helper()
	page, err := s.auditDB.QueryAuditEvents(db.Query{IncludeApp: true, Limit: 50})
	if err != nil {
		t.Fatalf("read the trail: %v", err)
	}
	out := map[string]string{}
	for _, r := range page.Rows {
		detail := ""
		if r.Detail != nil {
			detail = *r.Detail
		}
		out[r.Action] = detail
	}
	return out
}

// TestASaveWritesOnlyWhatValidated.
func TestASaveWritesOnlyWhatValidated(t *testing.T) {
	_, mux, dir := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	// topN 30 is valid; maxConns 999 is out of range and must be REFUSED, not
	// clamped; `notASetting` is unknown and dropped.
	w := settingsPost(mux, `{"topN":30,"maxConns":999,"notASetting":"x"}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		OK              bool `json:"ok"`
		RequiresRestart bool `json:"requiresRestart"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Errorf("body = %s", w.Body.String())
	}
	// Always false on all three live exits. Vestigial, and reproduced.
	if body.RequiresRestart {
		t.Error("requiresRestart is true; the live route never sends true")
	}

	got := readSettingsFile(t, dir)
	if got["topN"] != float64(30) {
		t.Errorf("topN = %#v, want 30", got["topN"])
	}
	if got["maxConns"] == float64(999) {
		t.Error("maxConns = 999 was written -- an out-of-range value must be refused, " +
			"not clamped and not accepted")
	}
	if _, ok := got["notASetting"]; ok {
		t.Error("an unknown key was written to settings.json")
	}
}

// TestTheResetBranchIsAuditedSeparately.
//
// It returns EARLY, so a single audit hook at the end of the handler would miss
// the one settings write that replaces the entire file. The live comment says
// exactly that.
func TestTheResetBranchIsAuditedSeparately(t *testing.T) {
	s, mux, dir := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"topN":42,"pageWifi":false}`)

	// The reset has its OWN broadcast, and nothing watched it until a mutation
	// deleting that line survived the whole suite. It is a separate emit site
	// because the branch returns early.
	watcher := hub.NewClient("w", 8)
	s.hub.Add(watcher)

	// The route depends on the validator emptying the updates on a reset. Pinned
	// here because the assertion below — that `topN: 7` did not ride along —
	// would otherwise hold for a reason this file does not state.
	if u, reset := store.SettingsUpdate(map[string]any{"_reset": true, "topN": 7}); !reset ||
		len(u) != 0 {
		t.Fatalf("SettingsUpdate on a reset returned %d updates (reset=%v); the route "+
			"relies on it returning none", len(u), reset)
	}

	w := settingsPost(mux, `{"_reset":true,"topN":7}`, authed)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	got := readSettingsFile(t, dir)
	def := store.Defaults()
	if got["topN"] != def["topN"] {
		t.Errorf("topN = %#v after a reset, want the default %#v", got["topN"], def["topN"])
	}
	// A body carrying BOTH a reset and an update is a reset, and the rest of the
	// body is never examined.
	if got["topN"] == float64(7) {
		t.Error("the update rode along with the reset -- `_reset` returns early")
	}

	actions := auditActions(t, s)
	if _, ok := actions["settings.reset"]; !ok {
		t.Error("the reset was not recorded -- it returns early, and a hook at the end " +
			"of the handler would miss the one write that replaces the whole file")
	}
	if _, ok := actions["settings.update"]; ok {
		t.Error("a reset also recorded settings.update")
	}

	select {
	case b := <-watcher.Send:
		if !strings.Contains(string(b), "settings:pages") {
			t.Errorf("the reset emitted %s", b)
		}
		// The DEFAULTS, not the previous file: the fixture had pageWifi false.
		if !strings.Contains(string(b), `"pageWifi":true`) {
			t.Errorf("the reset broadcast does not carry the defaults: %s", b)
		}
	case <-time.After(time.Second):
		t.Error("the reset emitted nothing -- every browser would keep drawing pages " +
			"according to settings that no longer exist")
	}
}

// TestASignedInNonAdministratorIsRefused.
//
// Everything else here runs as `AuthMode: "none"`, where `maySaveSettings`
// short-circuits to true, and the anonymous case is stopped at the SESSION check
// before the permission one is reached. So a mutation deleting the admin check
// survived the whole file: no test had a caller who was signed in and not
// allowed.
func TestASignedInNonAdministratorIsRefused(t *testing.T) {
	_, mux, dir := settingsWriteServer(t,
		&Session{AuthMode: "modern", Username: "viewer"}, `{"topN":25}`)
	before := readSettingsFile(t, dir)

	w := settingsPost(mux, `{"topN":50}`, authed)
	if w.Code != http.StatusForbidden {
		t.Errorf("a signed-in non-administrator answered %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Administrator") {
		t.Errorf("the refusal does not say what is required: %s", w.Body.String())
	}
	if after := readSettingsFile(t, dir); after["topN"] != before["topN"] {
		t.Errorf("topN changed from %#v to %#v despite the 403",
			before["topN"], after["topN"])
	}
}

// TestTheAuditRowNamesTheChangedFieldsOnly.
//
// `before` is the WHOLE previous object and `after` is only the updates, which
// is safe because `audit.Diff` walks `after`'s keys — a partial update must not
// report every untouched field as removed.
func TestTheAuditRowNamesTheChangedFieldsOnly(t *testing.T) {
	s, mux, _ := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"topN":25,"pageWifi":true,"historyMinutes":60}`)

	if w := settingsPost(mux, `{"topN":30}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	detail := auditActions(t, s)["settings.update"]
	if detail == "" {
		t.Fatal("settings.update was not recorded, or recorded no detail")
	}
	if !strings.Contains(detail, "topN") {
		t.Errorf("the audit detail does not mention topN: %s", detail)
	}
	for _, untouched := range []string{"pageWifi", "historyMinutes"} {
		if strings.Contains(detail, untouched) {
			t.Errorf("the audit row names %s, which this save did not change -- the diff "+
				"is walking `before` and reporting every field as removed: %s",
				untouched, detail)
		}
	}
}

// TestACredentialValueNeverReachesTheTrail.
//
// The audit table is deliberately absent from PURGE_TABLES, so a row cannot be
// withdrawn short of age-based retention. A token written into it is written for
// good.
func TestACredentialValueNeverReachesTheTrail(t *testing.T) {
	s, mux, dir := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	const secret = "REDACTED-telegram-token-value"
	if w := settingsPost(mux, `{"telegramBotToken":"`+secret+`"}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	detail := auditActions(t, s)["settings.update"]
	if detail == "" {
		t.Fatal("settings.update recorded nothing, so this proves nothing")
	}
	if !strings.Contains(detail, "telegramBotToken") {
		t.Errorf("the field NAME should be recorded; detail is %s", detail)
	}
	if strings.Contains(detail, secret) {
		t.Errorf("THE TOKEN VALUE IS IN THE AUDIT TRAIL: %s", detail)
	}

	// And it is SEALED on disk, not stored in the clear.
	if raw := readSettingsFile(t, dir); raw["telegramBotToken"] == secret {
		t.Error("the token was written to settings.json in plaintext")
	}
}

// TestAnUnreadableCredentialSurvivesAnUnrelatedSave.
//
// ── THE `kept` ROUND TRIP, AND WHY IT IS NOT DEFENSIVE PROGRAMMING ──────────
//
// A credential this process cannot decrypt — written under a different
// `.secret`, or corrupted — comes back from `store.Merge` as the empty string,
// with its original ciphertext handed over separately as `Kept`. If a save then
// wrote the empty string, an operator changing `topN` would silently destroy a
// Telegram token that was merely unreadable, and the Settings page would show
// the channel as unconfigured with no explanation.
//
// The route passes `kept` through for exactly that reason. A mutation dropping
// it survived every other test in this file, because none of them had a
// credential the store could not read.
func TestAnUnreadableCredentialSurvivesAnUnrelatedSave(t *testing.T) {
	// Not valid ciphertext under this .secret, which is the whole point.
	const opaque = "NOT-DECRYPTABLE-CIPHERTEXT"
	_, mux, dir := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"topN":25,"telegramBotToken":"`+opaque+`"}`)

	// Believability: the store really cannot read it, or `kept` would be empty
	// and this test would pass for the wrong reason.
	if before := readSettingsFile(t, dir); before["telegramBotToken"] != opaque {
		t.Fatalf("the fixture did not land: %#v", before["telegramBotToken"])
	}

	if w := settingsPost(mux, `{"topN":30}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	after := readSettingsFile(t, dir)
	if after["topN"] != float64(30) {
		t.Errorf("topN = %#v; the save did not happen, so the check below is vacuous",
			after["topN"])
	}
	if after["telegramBotToken"] != opaque {
		t.Errorf("telegramBotToken = %#v after saving an UNRELATED key; want the "+
			"original ciphertext preserved. A credential this process cannot read must "+
			"not be destroyed by a save that never touched it", after["telegramBotToken"])
	}
}

// TestOnlyAGlobalAdministratorMaySave.
//
// These settings are fleet-wide, so a grant held on one router confers nothing.
// A missing resolver REFUSES here — unlike `mayAck`, where an install-wide
// condition must not lock every operator out of their own bell.
func TestOnlyAGlobalAdministratorMaySave(t *testing.T) {
	s := &Server{}
	if s.maySaveSettings(nil) {
		t.Error("a nil session was permitted")
	}
	if !s.maySaveSettings(&Session{AuthMode: "none"}) {
		t.Error("auth mode none was refused -- there is no identity to grant anything to")
	}
	if s.maySaveSettings(&Session{AuthMode: "modern", Username: "bob"}) {
		t.Error("a missing RBAC resolver PERMITTED a settings write -- rewriting the " +
			"fleet's configuration is not in the class of things that fail open")
	}
}

// TestAnAnonymousCallerCannotSave, through the mux.
func TestAnAnonymousCallerCannotSave(t *testing.T) {
	_, mux, dir := settingsWriteServer(t, &Session{AuthMode: "none"}, `{"topN":25}`)
	before := readSettingsFile(t, dir)

	if w := settingsPost(mux, `{"topN":50}`, ""); w.Code != http.StatusUnauthorized {
		t.Errorf("answered %d without a session, want 401", w.Code)
	}
	if after := readSettingsFile(t, dir); after["topN"] != before["topN"] {
		t.Errorf("topN changed from %#v to %#v despite the refusal",
			before["topN"], after["topN"])
	}
}

// TestABodyThatIsNotJSONSavesNothing.
//
// `req.body || {}` on the live side: an unreadable body is an empty one, which
// validates to no updates. It is not an error.
func TestABodyThatIsNotJSONSavesNothing(t *testing.T) {
	_, mux, dir := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"},
		`{"topN":25}`)

	w := settingsPost(mux, `not json at all`, authed)
	if w.Code != http.StatusOK {
		t.Errorf("answered %d for an unreadable body, want 200 -- the live route treats "+
			"it as an empty object", w.Code)
	}
	if got := readSettingsFile(t, dir); got["topN"] != float64(25) {
		t.Errorf("topN = %#v after an unreadable body", got["topN"])
	}
}

// TestTheSaveBroadcastsPageSettingsToEveryone.
//
// `io.emit`, not a room: which pages a browser may draw is a fleet-wide fact, so
// a viewer looking at any router — or at none — has to hear about it.
func TestTheSaveBroadcastsPageSettingsToEveryone(t *testing.T) {
	s, mux, _ := settingsWriteServer(t, &Session{AuthMode: "none", Username: "admin"}, "")

	// Deliberately in DIFFERENT rooms, and one in no room at all.
	watchers := map[string]*hub.Client{
		"r1": hub.NewClient("a", 8), "r2": hub.NewClient("b", 8), "": hub.NewClient("c", 8),
	}
	for room, c := range watchers {
		s.hub.Add(c)
		if room != "" {
			s.hub.Join(c, "router-"+room)
		}
	}

	if w := settingsPost(mux, `{"pageWifi":false}`, authed); w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}

	for room, c := range watchers {
		select {
		case b := <-c.Send:
			if !strings.Contains(string(b), "settings:pages") {
				t.Errorf("the client in room %q received %s", room, b)
			}
			if !strings.Contains(string(b), `"pageWifi":false`) {
				t.Errorf("the payload does not carry the change: %s", b)
			}
		case <-time.After(time.Second):
			t.Errorf("the client in room %q received nothing -- a room broadcast would "+
				"leave viewers of other routers with stale page visibility", room)
		}
	}
}

// TestThePageSettingsPayloadCarriesEveryKeyEvenWhenTheFileDoesNot.
//
// ── THE DEFECT THIS PINS ────────────────────────────────────────────────────
//
// `ws.go` sent `settings:pages` built from `store.Settings()` — settings.json AS
// IT IS ON DISK. `PageSettings` copies only the keys it finds, so every key the
// operator has never changed was ABSENT from the payload. The live
// `Settings.load()` merges DEFAULTS first, so they are always present.
//
// Found by the live-socket-diff tool against the operator's own install: six
// keys short, three of them (`pageBackups`, `pageDevices`, `pageWifi`) nav
// visibility flags, so the client read `undefined` and hid those entries. On a
// FRESH install, where settings.json is nearly empty, almost every page flag
// would have been missing.
//
// No test caught it because both paths agree completely on a fixture whose
// settings.json carries every key. So this one deliberately does NOT.
func TestThePageSettingsPayloadCarriesEveryKeyEvenWhenTheFileDoesNot(t *testing.T) {
	// A settings.json with ONE key in it. Everything else must come from the
	// defaults, which is exactly what the raw read failed to do.
	s, _, _ := settingsWriteServer(t, &Session{AuthMode: "none"}, `{"alertCpuThreshold":77}`)

	// THROUGH `sendPageSettings`, the function ws.go actually calls — not through
	// `mergedSettings`, which is where the first draft of this test looked.
	//
	// That draft PASSED with the bug reintroduced: it asserted the merge produces
	// every key, which was never in doubt, while the emit went on reading the raw
	// file beside it. A test that bypasses the path it stands in for tests
	// nothing, and the only reason this one does not is that reintroducing the
	// bug was tried.
	watcher := hub.NewClient("pages-watcher", 8)
	s.hub.Add(watcher)
	cn := &conn{srv: s, c: watcher}
	cn.sendPageSettings()

	var payload map[string]any
	select {
	case b := <-watcher.Send:
		var env struct {
			Event string         `json:"event"`
			Data  map[string]any `json:"data"`
		}
		if err := json.Unmarshal(b, &env); err != nil {
			t.Fatalf("the frame is not an envelope: %s", b)
		}
		if env.Event != "settings:pages" {
			t.Fatalf("the first frame was %q", env.Event)
		}
		payload = env.Data
	case <-time.After(time.Second):
		t.Fatal("sendPageSettings emitted nothing")
	}

	// Every key the projection declares must be present. The list is the
	// generated one, so this follows upstream rather than restating it.
	missing := []string{}
	for _, k := range store.PageSettingKeys() {
		if _, ok := payload[k]; !ok {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d of %d page-setting keys are missing from the EMITTED payload: %v\n"+
			"The client reads these to decide which nav entries to draw; an absent key is "+
			"`undefined`, which hides the page.", len(missing), len(store.PageSettingKeys()), missing)
	}
	// And the one key the file DID set survives the merge rather than being
	// replaced by its default.
	if payload["alertCpuThreshold"] != float64(77) {
		t.Errorf("the file's own value was lost: %v", payload["alertCpuThreshold"])
	}

	// THE RAW READ IS THE BUG, asserted so the fix cannot be quietly reverted:
	// the same projection over the UNMERGED file is short, and that is what
	// `ws.go` used to send.
	raw, err := s.store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(store.PageSettings(raw)); n >= len(payload) {
		t.Errorf("the unmerged projection has %d keys and the emitted one %d — this fixture "+
			"cannot show the difference the merge makes, so the check above proves nothing",
			n, len(payload))
	}
}
