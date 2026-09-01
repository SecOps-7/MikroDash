package principals

// `_parseRolePages` — validating a submitted page matrix before it is written.
//
// ── THREE ANSWERS, NOT TWO ──────────────────────────────────────────────────
//
// The live function returns three distinguishable shapes and the caller treats
// each differently:
//
//	{ value: null }   the key was NOT SUBMITTED — leave the role's pages alone
//	{ value: [...] }  a validated matrix, which REPLACES the whole set
//	{ error: '...' }  a 400, and nothing is written
//
// `{ value: null }` and `{ value: [] }` are the pair that has to survive the
// port. An ABSENT `pages` key means "do not touch"; an EMPTY ARRAY means "this
// role now confers nothing". Collapsing them into one nil slice would either
// strip every page from a role on an unrelated rename, or silently ignore an
// operator revoking the last one — and both are silent.
//
// Hence `Submitted`, which carries the distinction Go's nil slice cannot.
//
// ── THE ERROR STRINGS ARE THE CONTRACT ──────────────────────────────────────
//
// They are rendered verbatim in the role editor, and two of them interpolate the
// offending key. The rolepages corpus compares them exactly rather than
// asserting "an error was returned".
//
// ── IT VALIDATES THE WHOLE LIST OR NONE OF IT ───────────────────────────────
//
// A bad entry anywhere refuses the request; nothing partial is written. The live
// loop returns on the first failure, and the corpus carries a good row followed
// by a bad one to pin that a port does not accept the prefix.

import "fmt"

// RolePage is one row of the matrix.
type RolePage struct {
	Page   string `json:"page"`
	Access string `json:"access"`
}

// RolePages is the parsed answer.
type RolePages struct {
	// Submitted is false when the `pages` key was absent, which the caller reads
	// as "leave the role's pages alone". When it is true, Pages REPLACES the
	// whole set — including when it is empty.
	Submitted bool
	Pages     []RolePage
}

// ParseRolePages validates `body["pages"]` against the page registry.
//
// `known` is the registry — `Pages.BY_KEY` on the live side. Passed in rather
// than imported so this package keeps no dependency on the page table, which is
// what lets it be tested against the corpus with the registry the corpus used.
func ParseRolePages(body map[string]any, known map[string]bool) (RolePages, error) {
	raw, present := body["pages"]
	// `if (body.pages === undefined) return { value: null }`. In Go a decoded
	// JSON body cannot hold `undefined`, so an ABSENT KEY is the whole of it —
	// but an explicit `null` IS present, and falls through to the array check
	// below, where it is refused. The corpus carries both.
	if !present {
		return RolePages{}, nil
	}

	arr, ok := raw.([]any)
	if !ok {
		return RolePages{}, fmt.Errorf("pages must be an array")
	}

	// NON-NIL even for an empty list, so `Submitted` and a zero-length `Pages`
	// together say "replace the set with nothing".
	out := make([]RolePage, 0, len(arr))
	seen := make(map[string]bool, len(arr))
	for _, row := range arr {
		m, ok := row.(map[string]any)
		if !ok {
			// `if (!row || typeof row !== 'object')`. A JSON null decodes to a
			// nil `any`, which is not a map, so both halves land here.
			return RolePages{}, fmt.Errorf("Each page entry must be an object")
		}
		// `String(row.page || '')` — a missing key, a null and an empty string
		// all become "", which is never in the registry.
		page := jsPageKey(m["page"])
		if !known[page] {
			return RolePages{}, fmt.Errorf("Unknown page: %s", page)
		}
		if seen[page] {
			return RolePages{}, fmt.Errorf("Duplicate page: %s", page)
		}
		access, _ := m["access"].(string)
		// EXACTLY these two, case-sensitively. "READ" is refused, which is what
		// stops a client inventing a third level by capitalisation.
		if access != "read" && access != "write" {
			return RolePages{}, fmt.Errorf("access must be read or write")
		}
		seen[page] = true
		out = append(out, RolePage{Page: page, Access: access})
	}
	return RolePages{Submitted: true, Pages: out}, nil
}

// jsPageKey is `String(row.page || ”)`.
//
// The `|| ”` matters for a FALSY value: `String(0)` is "0" and `String(false)`
// is "false", but `0 || ”` is "" and `false || ”` is "". So a zero and a false
// both become the empty string, where a non-zero number keeps its digits.
func jsPageKey(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if !t {
			return ""
		}
		return "true"
	case float64:
		if t == 0 {
			return ""
		}
		return trimNumber(t)
	default:
		return ""
	}
}

// trimNumber renders a JSON number the way `String()` does — no trailing zeros,
// no exponent for the range a page key could plausibly be typed as.
func trimNumber(f float64) string {
	s := fmt.Sprintf("%v", f)
	return s
}
