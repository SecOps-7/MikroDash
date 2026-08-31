package principals

// The name and description a GROUP or a ROLE is allowed to have — the port of
// `_parseName`.
//
// ── THIS DUPLICATES internal/sites/parse.go's FIRST HALF, ON PURPOSE ────────
//
// `_parseName` is byte-identical to the name/description half of
// `_parseSiteBody`. The obvious move is one shared parser, and it is the wrong
// one: two corpora lifted from two originals catch upstream changing ONE of
// them, where a single shared function would apply the change to both or to
// neither and stay green either way. `tools/name-cases.js` pins this one;
// `tools/site-body-cases.js` pins the other.
//
// ── ABSENT VERSUS NULL VERSUS SET ───────────────────────────────────────────
//
//	absent  not written. On a CREATE the name is required anyway.
//	null    for a description, an explicit clear.
//	set     validated, trimmed, bounded.
//
// Go's `map[string]any` cannot tell an absent key from a JSON null, so every
// read here is `v, ok := m[k]` and `Fields` carries explicit `Has` flags.

import (
	"fmt"
	"strings"

	"mikrodash/internal/jsval"
)

// NameMax and DescriptionMax are the live bounds, measured AFTER trimming.
const (
	NameMax        = 64
	DescriptionMax = 256
)

// Fields is what the writer may set.
type Fields struct {
	Name    string
	HasName bool

	// Description is nil for an explicit clear. Written only when
	// HasDescription — the difference between "leave it alone" and "empty it".
	Description    *string
	HasDescription bool
}

// Columns turns Fields into the column → value map a writer takes. A key is
// present only when the column is to be written; a nil VALUE is an explicit
// NULL, which is a different thing from an absent key.
func (f Fields) Columns() map[string]any {
	out := map[string]any{}
	if f.HasName {
		out["name"] = f.Name
	}
	if f.HasDescription {
		if f.Description == nil {
			out["description"] = nil
		} else {
			out["description"] = *f.Description
		}
	}
	return out
}

// ParseName validates a decoded request body.
//
// `partial` is true for an EDIT: an absent name is then simply not written,
// where a create requires one.
func ParseName(body map[string]any, partial bool) (Fields, error) {
	var f Fields

	if raw, ok := body["name"]; ok || !partial {
		name := strings.TrimSpace(jsName(raw, ok))
		if name == "" || len(name) > NameMax {
			return Fields{}, fmt.Errorf("Name must be 1-64 characters")
		}
		f.Name, f.HasName = name, true
	}

	if raw, ok := body["description"]; ok {
		// `String(b.description == null ? '' : b.description)` — LOOSE equality,
		// so null and undefined both become "". Then `d || null`, which makes an
		// empty string and an explicit null write the same NULL.
		d := ""
		if raw != nil {
			d = jsval.String(raw)
		}
		d = strings.TrimSpace(d)
		if len(d) > DescriptionMax {
			return Fields{}, fmt.Errorf("Description must be 256 characters or fewer")
		}
		f.HasDescription = true
		if d != "" {
			f.Description = &d
		}
	}

	return f, nil
}

// jsName is `String(v == null ? ” : v)`, LOOSE — a JSON null is a MISSING name.
//
// It used to be the strict `=== undefined`, and a null then fell through to
// `String(null)`: the four-character string "null", within 1-64, creating a
// record literally called "null". Posting a second one answered 409 "already
// exists", about a field the caller sent as empty — and `{"name": null}` is what
// a cleared form field serialises to, so it was reachable from the UI rather
// than only from curl. Filed as `ToDo.md` §6 and FIXED upstream on 2026-08-27;
// this port follows the fix rather than keeping the quirk, and
// `tools/name-cases.js` pins it.
//
// The `present` argument is still needed and is NOT the null question: it
// carries "the key was absent", which on a partial edit means do not write the
// field at all. Go's map cannot tell absent from null, so the caller checks.
func jsName(v any, present bool) string {
	if !present || v == nil {
		return ""
	}
	return jsval.String(v)
}
