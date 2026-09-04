package store

import "encoding/json"

// CoerceRouterPatch types a `PUT /api/routers/:id` patch the way the live
// `updateRouter` does, for the keys the patch actually names.
//
// ── WITHOUT IT, ONE BAD FIELD MAKES THE WHOLE FLEET DISAPPEAR ──────────────
//
// `UpdateRouter` is a shallow patch that writes what it is given. `Router` has
// typed fields — `port int`, `tls`/`tlsInsecure`/`disabled`/`alertsEnabled bool`,
// the two bandwidth ints — and `Routers()` decodes routers.json into
// `[]Router` in ONE Unmarshal. So a single string where a bool belongs does not
// spoil one record; it fails the decode and returns ZERO ROUTERS.
//
// Measured 2026-08-29: writing `{"disabled":"false"}` through the update path
// left routers.json holding the string, after which `Routers()` answered
// `0 routers` and `json: cannot unmarshal string into Go struct field
// Router.disabled of type bool`. Every page, session and collector reads that
// call. A well-formed API request from an authorised operator takes the fleet
// out until somebody hand-edits the file.
//
// The live app cannot do this: every field on its update path goes through a
// coercion (`!!(data.disabled)`, `parseInt(data.port, 10)`, …), so routers.json
// only ever holds the right types. This is that field block, for the keys
// present in the patch — an ABSENT key is untouched, which is what makes it a
// patch rather than a rewrite.
//
// ── THE EXPRESSIONS ARE LIVE'S, NOT TIDIER VERSIONS OF THEM ────────────────
//
// `tls` is true unless the value is literal false or the string "false". Every
// OTHER boolean here is `_isTrue` — the exact word or the string "true", and
// nothing else.
//
// ── AND THAT CHANGED UPSTREAM ON 2026-08-29, IN THIS PORT'S FAVOUR ─────────
//
// This block used to reproduce THREE different coercions, because live had
// three: `tlsInsecure` was `=== true || === 'true'` (`dccbf62`) while `disabled`
// and `alertsEnabled` were plain `!!` truthiness — under which the four
// characters "false" are TRUE. The note here said reproducing all three was
// deliberate and that the divergence was reported rather than fixed
// unilaterally.
//
// `dd6173b` is the reply: the port enumerated the class after `dccbf62` fixed
// the instance, and found three survivors. Upstream's own summary of the worst
// of them — "PUT with disabled: 'false' is an operator ENABLING a router. The
// truthiness form read it as true and disabled it, tearing the session down —
// the opposite of what was asked, on the field that decides whether a device is
// monitored at all." All six live sites now go through one `_isTrue`, so this
// block follows and there is one rule again.
//
// `tls` still does not, on both sides, and the reason is not tidiness: it
// defaults to ON, so its question is "not false and not 'false'" — a different
// question whose SAFE direction is the opposite one. Two rules, deliberately.
//
// Junk (`'1'`, `'yes'`, `'on'`, an object) is false everywhere, which for
// `disabled` means the conservative direction is leaving the router in service.
func CoerceRouterPatch(patch map[string]any) map[string]any {
	if patch == nil {
		return nil
	}
	out := make(map[string]any, len(patch))
	for k, v := range patch {
		out[k] = v
	}
	// `data.tls !== false && data.tls !== 'false'`
	if v, ok := out["tls"]; ok {
		out["tls"] = !jsIsFalse(v)
	}
	// `data.tlsInsecure === true || data.tlsInsecure === 'true'`
	if v, ok := out["tlsInsecure"]; ok {
		out["tlsInsecure"] = jsIsTrue(v)
	}
	// `_isTrue(data.disabled)` and `_isTrue(data.alertsEnabled)` — the SAME rule
	// as tlsInsecure since `dd6173b`, not truthiness. See the header.
	if v, ok := out["disabled"]; ok {
		out["disabled"] = jsIsTrue(v)
	}
	if v, ok := out["alertsEnabled"]; ok {
		out["alertsEnabled"] = jsIsTrue(v)
	}
	if v, ok := out["reportingEnabled"]; ok {
		out["reportingEnabled"] = jsIsTrue(v)
	}
	// `parseInt(data.port, 10)`. An unparseable port becomes 0 on the live side
	// too — `parseInt('abc')` is NaN and JSON.stringify writes it as null — so
	// this is not made stricter than the thing it mirrors.
	if v, ok := out["port"]; ok {
		n, _ := jsInt(v)
		out["port"] = n
	}
	// `Math.max(1, parseInt(x, 10) || 1000)`
	if v, ok := out["bwDownMbps"]; ok {
		out["bwDownMbps"] = bwOr(v)
	}
	if v, ok := out["bwUpMbps"]; ok {
		out["bwUpMbps"] = bwOr(v)
	}
	// `(n >= 0 && n <= 300) ? n : 30`
	if v, ok := out["connDownThresholdSec"]; ok {
		out["connDownThresholdSec"] = connDownOr(v)
	}
	return out
}

// jsonUnmarshalString is a test helper kept beside the code it exercises, so the
// fixtures can express "the JSON string \"false\"" rather than a Go string that
// happens to look like one.
func jsonUnmarshalString(raw string, v any) error { return json.Unmarshal([]byte(raw), v) }

// normalizeStoredRouterBools rewrites the four boolean fields of every record in
// a raw routers.json, using the SAME rules `CoerceRouterPatch` applies to an
// incoming patch — `_isTrue` for three of them, and `tls`'s opposite
// default-on rule for the fourth.
//
// It is the READ half of upstream `dd6173b`, and it exists because the two
// halves fail differently. A bad incoming value writes one wrong field; a bad
// STORED value fails the typed decode of the whole file and returns no routers
// at all. `Routers()` calls this only after that decode has already failed.
//
// It reports whether it changed anything, so a file that failed to decode for
// some OTHER reason is handed back unrepaired rather than being retried with an
// identical copy and a second, more confusing error.
func normalizeStoredRouterBools(b []byte) ([]byte, bool) {
	var recs []map[string]any
	if err := json.Unmarshal(b, &recs); err != nil {
		// Not an array of objects at all. Nothing here can help; the caller
		// returns the original decode error.
		return nil, false
	}
	changed := false
	for _, r := range recs {
		if r == nil {
			continue
		}
		// `reportingEnabled` is here for the reason the header gives: a key
		// MISSING from this list is not a dropped field, it is a stored string
		// that fails the whole-file decode with no repair pass — which returns
		// ZERO routers, not one bad one.
		for _, k := range []string{"disabled", "alertsEnabled", "reportingEnabled", "tlsInsecure"} {
			v, ok := r[k]
			if !ok {
				continue
			}
			// ONLY a non-bool is touched. A stored `true` must stay `true`
			// without being re-derived, so an honest record cannot be changed by
			// a bug in the rule.
			if _, isBool := v.(bool); isBool {
				continue
			}
			r[k] = jsIsTrue(v)
			changed = true
		}
		if v, ok := r["tls"]; ok {
			if _, isBool := v.(bool); !isBool {
				r["tls"] = !jsIsFalse(v)
				changed = true
			}
		}
	}
	if !changed {
		return nil, false
	}
	out, err := json.Marshal(recs)
	if err != nil {
		return nil, false
	}
	return out, true
}
