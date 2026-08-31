package store

// What `POST /api/settings` accepts, and what it silently refuses.
//
// ── IT WAS PORTED BY READING. IT IS NOT CHECKED THAT WAY ANY MORE ──────────
//
// This file used to say: "Every other differential gate here RUNS the live
// implementation. This handler is inline in `src/index.js`, which calls
// `server.listen()` at require time and cannot be loaded by a test [...] So the
// RULES below are read-ported and covered by hand-written tests."
//
// The premise was true and stopped being true. `tools/alert-row-check.js` lifts
// a PRIVATE function out of that same file with `lib/lift.js` — the module is
// READ, never required, so `server.listen()` never runs — and the same technique
// works on a block that is not a function at all.
// `tools/settings-validate-check.js` slices the validator between two asserted
// anchors, evaluates it with `body` bound, and drives 63 bodies through it;
// `settings_validate_test.go` compares this function against those answers.
//
// So the rules below are no longer covered by a rewrite that agrees with its
// author's reading. They agree with what the live code DOES. Fifteen mutations
// were injected against that gate and all fifteen died.
//
// The TABLES are still generated (`tools/settings-write-tables.js`), because a
// page added means a new `page*` boolean and a hand-copied list would quietly
// stop accepting it.
//
// ── AN INVALID VALUE IS IGNORED, NOT CLAMPED ───────────────────────────────
//
// Out of range, unparseable, or not on the whitelist: the key is simply absent
// from the updates, so `save` leaves the stored value alone. Clamping instead
// would let a hand-crafted request move a setting to the edge of its range while
// looking like it was refused.

import (
	_ "embed"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"
	// Embedded so a timezone can be validated in a scratch container. The static
	// binary is the point — see the modernc.org/sqlite reasoning in CLAUDE.md —
	// and a settings form that rejected every zone because the image carries no
	// tzdata would be a difference nobody could explain from the screen.
	_ "time/tzdata"
	"unicode/utf16"
)

//go:embed settings_write_tables.json
var settingsWriteTablesJSON []byte

type writeTables struct {
	IntFields  map[string][2]int `json:"intFields"`
	StrFields  []string          `json:"strFields"`
	BoolFields []string          `json:"boolFields"`
	CredFields []string          `json:"credFields"`
	// SpecialCases are the keys the handler validates OUTSIDE the four tables.
	// The generator refuses to write this file if the handler names a setting
	// that is in neither a table nor this list, so a new special case upstream
	// stops the build rather than being silently dropped from every save.
	SpecialCases []string `json:"specialCases"`
}

var wtables = mustWriteTables()

func mustWriteTables() writeTables {
	var t writeTables
	if err := json.Unmarshal(settingsWriteTablesJSON, &t); err != nil {
		panic("store: settings_write_tables.json: " + err.Error())
	}
	return t
}

// IsMasked reports the mask sentinel.
//
// THE GUARD THIS EXISTS FOR: the form renders a configured credential as the
// mask, and submitting the form sends it straight back. Without this test, every
// save would replace the real token with eight bullet characters — the channel
// would stop working and the page would still show it as configured.
func IsMasked(v any) bool {
	s, ok := v.(string)
	return ok && s == Mask
}

// SettingsUpdate turns a request body into the updates `save` should apply.
//
// `reset` reports the `_reset` branch, which replaces every setting with its
// default and returns early there — so a body carrying both is a reset, and the
// rest of the body is not examined.
func SettingsUpdate(body map[string]any) (updates Settings, reset bool) {
	if b, ok := body["_reset"]; ok && truthy(b) {
		return Settings{}, true
	}
	updates = Settings{}

	for f, r := range wtables.IntFields {
		if raw, ok := body[f]; ok {
			if n, ok := parseIntLike(raw); ok && n >= r[0] && n <= r[1] {
				updates[f] = n
			}
		}
	}
	for _, f := range wtables.StrFields {
		if raw, ok := body[f]; ok {
			updates[f] = cut(strings.TrimSpace(asString(raw)), 256)
		}
	}
	for _, f := range wtables.BoolFields {
		if raw, ok := body[f]; ok {
			// `body[f] === true || body[f] === 'true'` — nothing else is true,
			// so the string "1" and the number 1 are both FALSE here.
			updates[f] = raw == true || raw == "true"
		}
	}
	for _, f := range wtables.CredFields {
		if raw, ok := body[f]; ok && !IsMasked(raw) {
			// NOT TRIMMED, unlike the string fields, and the limit is 512. A
			// token with a trailing space is a token the operator pasted, and
			// silently trimming it produces an authentication failure that
			// nothing on the page explains.
			updates[f] = cut(asString(raw), 512)
		}
	}

	// authMode is a whitelist of two.
	if raw, ok := body["authMode"]; ok {
		if s := asString(raw); s == "none" || s == "modern" {
			updates["authMode"] = s
		}
	}

	// sessionTimeoutMs: zero means NEVER and must not be clamped to a minimum,
	// so the accepted set is {0} plus one hour to one day.
	if raw, ok := body["sessionTimeoutMs"]; ok {
		if n, ok := parseIntLike(raw); ok && (n == 0 || (n >= 3600000 && n <= 86400000)) {
			updates["sessionTimeoutMs"] = n
		}
	}

	for _, f := range []string{"notifBody", "notifBodyUp"} {
		if raw, ok := body[f]; ok {
			updates[f] = cut(strings.TrimSpace(asString(raw)), 512)
		}
	}

	// customPollProfile is either cleared or a JSON OBJECT. `typeof
	// JSON.parse(v) === 'object'` — which in JavaScript is also true of an
	// array and of null, and both are accepted there, so both are accepted
	// here rather than tightened.
	if raw, ok := body["customPollProfile"]; ok {
		v := cut(strings.TrimSpace(asString(raw)), 512)
		if v == "" {
			updates["customPollProfile"] = v
		} else {
			var any1 any
			if err := json.Unmarshal([]byte(v), &any1); err == nil && jsObject(any1) {
				updates["customPollProfile"] = v
			}
		}
	}

	// displayTimezone: cleared explicitly, or a zone the runtime recognises.
	if raw, ok := body["displayTimezone"]; ok {
		tz := cut(strings.TrimSpace(asString(raw)), 64)
		if tz == "" {
			updates["displayTimezone"] = ""
		} else if _, err := time.LoadLocation(tz); err == nil {
			updates["displayTimezone"] = tz
		}
	}

	return updates, false
}

// jsObject is `typeof x === 'object'`: true for objects, arrays AND null.
func jsObject(x any) bool {
	switch x.(type) {
	case map[string]any, []any, nil:
		return true
	}
	return false
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0 && !math.IsNaN(t)
	case nil:
		return false
	}
	return true
}

// asString is `String(v)` for the shapes a JSON body can hold.
func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// parseIntLike is `parseInt(v, 10)` over a JSON value, reporting whether the
// result is a number at all. A float is truncated toward zero, as parseInt
// truncates the digits it reads.
func parseIntLike(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return int(math.Trunc(t)), true
	// THE REAL PATH IS float64 — a body arrives through json.Unmarshal into
	// map[string]any, where every number is one. `int` and `int64` are accepted
	// as well because this is exported: a Go caller building the map by hand
	// would otherwise have every numeric setting silently dropped, which is the
	// same shape of failure as a value being refused for being out of range and
	// far harder to see.
	case int:
		return t, true
	case int64:
		return int(t), true
	case string:
		f := leadingInt(t)
		if math.IsNaN(f) {
			return 0, false
		}
		return int(f), true
	}
	return 0, false
}

// cut is JavaScript's `slice(0, n)`, which counts UTF-16 CODE UNITS.
//
// Go's `s[:n]` counts BYTES, and the two disagree for anything outside ASCII —
// a limit of 256 would cut a notification template of accented text a third of
// the way short, and could split a rune and produce invalid UTF-8 in the stored
// file. Counting the way the original counts keeps the boundary in the same
// place for the same input.
func cut(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}
