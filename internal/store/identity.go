package store

// What a router says it IS, written back onto its record.
//
// ── WHY THE PORT NEEDED THIS ────────────────────────────────────────────────
//
// The background pool learns model, serial and osVersion from a router's first
// `/system/resource` read and persists them; the Devices page renders `model`
// and `osVersion` straight out of the record. Without this the pool would take a
// nil identity hook and a router added through this app would show an empty
// model column forever — while one imported from a Node-managed /data would
// show a populated one, which reads as a per-router bug rather than a missing
// feature.
//
// ── NOT ROUTED THROUGH UpdateRouter'S CALLERS, DELIBERATELY ─────────────────
//
// The live comment on the sibling `updateGeoAuto` says it for both: "Deliberately
// NOT routed through update(): that re-validates host and port, recomputes the
// unique label, and its HTTP callers go on to bump RBAC and broadcast a
// permissions change. A background refresh must do none of those." This uses the
// same `UpdateRouter` primitive but is its own entry point, so no caller can
// mistake a poll for an edit.
//
// ── THE THREE RULES, EACH OF WHICH FAILS DIFFERENTLY ────────────────────────
//
//  1. A NON-STRING IS SKIPPED, NOT CLEARED. `typeof val !== 'string'` continues
//     past the field. A port reading a missing key as "" would blank a model the
//     router did not happen to report this time.
//  2. AN EMPTY RESULT IS SKIPPED TOO — `if (clean && ...)`. Trimming "   " to ""
//     does not clear what is stored.
//  3. FALSE MEANS NO WRITE HAPPENED, and the caller depends on it: the audit
//     event and the router-list broadcast are both gated on it. A port that
//     always wrote would rewrite routers.json on every poll of every router and
//     emit an audit event each time.
//
// ── THE 64-CAP COUNTS UTF-16 CODE UNITS ─────────────────────────────────────
//
// `String.prototype.slice(0, 64)`, not bytes. Go's `s[:64]` is bytes, so the two
// part company on the first non-ASCII character: forty accented characters are
// eighty bytes and forty code units — untouched by the live function and
// truncated by a byte slice. The identity-fields corpus carries the
// accented and CJK cases that separate them.
//
// A CAP THAT LANDS MID-SURROGATE-PAIR is left to `utf16.Decode`, which yields
// U+FFFD for a lone surrogate where JavaScript keeps the half. Recorded rather
// than chased: reproducing it would mean emitting invalid UTF-8 from a Go
// string, and the values are RouterOS model and serial strings — ASCII on every
// device this fleet has, and the failure is a replacement character in a name
// rather than a wrong decision.

import (
	"strings"
	"unicode/utf16"
)

// identityFields is the live `IDENTITY_FIELDS`, in its order. The order is not
// load-bearing — the result is a set of changes — but keeping it makes the two
// readable side by side.
var identityFields = []string{"model", "serial", "osVersion"}

// Identity is what a router reported about itself. Each field is optional; an
// empty one means "not reported", which is rule 1.
type Identity struct {
	Model     string
	Serial    string
	OSVersion string
}

// UpdateIdentity writes the changed identity fields onto a router record.
//
// Returns whether anything was written. FALSE is the common case — a router
// reports the same identity on every poll — and is what stops this from
// rewriting routers.json and emitting an audit event several times a minute.
func (s *Store) UpdateIdentity(id string, ident Identity) (bool, error) {
	if id == "" {
		return false, nil
	}
	all, _ := s.Routers()
	var current *Router
	for i := range all {
		if all[i].ID == id {
			current = &all[i]
			break
		}
	}
	if current == nil {
		// The live function returns null for an unknown id rather than raising.
		// A router deleted while its background session was mid-poll is ordinary,
		// not an error.
		return false, nil
	}

	reported := map[string]string{
		"model": ident.Model, "serial": ident.Serial, "osVersion": ident.OSVersion,
	}
	stored := map[string]string{
		"model": current.Model, "serial": current.Serial, "osVersion": current.OSVersion,
	}

	changed := map[string]any{}
	for _, key := range identityFields {
		clean := clampUTF16(strings.TrimSpace(reported[key]), 64)
		// RULE 2: an empty result is not a clear. RULE 1 is upstream of this —
		// a field the router did not report arrives as "" and lands here.
		if clean == "" || clean == stored[key] {
			continue
		}
		changed[key] = clean
	}
	if len(changed) == 0 {
		return false, nil
	}
	if err := s.UpdateRouter(id, changed); err != nil {
		return false, err
	}
	return true, nil
}

// clampUTF16 truncates to at most n UTF-16 code units, which is what
// `String.prototype.slice` counts.
func clampUTF16(s string, n int) string {
	// FAST PATH ON LENGTH, not on a rune scan: a string of n bytes cannot exceed
	// n code units, so anything this short is already under the cap whatever it
	// holds. Every real model and serial takes this path.
	if len(s) <= n {
		return s
	}
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}
