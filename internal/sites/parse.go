package sites

// What a site CREATE or EDIT is allowed to write — the port of `_parseSiteBody`.
//
// ── THE WHOLE DECISION IS ABSENT VERSUS NULL VERSUS SET ─────────────────────
//
// Three distinct inputs with three distinct outcomes:
//
//	absent  the caller did not touch it. LEAVE IT ALONE — which is what stops a
//	        rename from blanking a site's location.
//	null    an explicit "no location" / "no description".
//	set     a value, validated.
//
// AND GO'S `map[string]any` CANNOT TELL THE FIRST TWO APART: `m["place"]` is nil
// for a missing key and for a JSON null alike. This port has been caught by
// exactly that twice — `collection.PollRetunes` and `store.PageSettings` — so
// every read here asks `v, ok := m[k]` and branches on BOTH, never on `v == nil`
// alone. `Patch` carries the same distinction outward, as pointers-to-pointers
// would if they read tolerably; they do not, so it carries explicit `Has` flags.
//
// ── AND THE LOCATION MOVES AS FIVE COLUMNS OR NONE ──────────────────────────
//
// A site's location is a PICKED PLACE, not typed coordinates (#96). `lat`/`lon`
// survive as the plotted values but are DERIVED from the choice, which is why
// all five columns move together and a half-set location is unreachable. Top-
// level `lat`/`lon` in the body are ignored outright rather than written; a port
// that honoured them would make a half-set location reachable and put a site on
// the map at coordinates no gazetteer ever produced.
//
// Validation goes through `geoplace.NormalizePlace` — the same function the
// router store uses — so a site and a router cannot disagree about what a
// well-formed place is.

import (
	"fmt"
	"strings"

	"mikrodash/internal/geoplace"
	"mikrodash/internal/jsval"
)

// NameMax and DescriptionMax are the live bounds, measured AFTER trimming.
const (
	NameMax        = 64
	DescriptionMax = 256
)

// Patch is what the writer may set, with absence distinguished from null.
//
// The `Has*` flags are not defensive noise: they are the only thing separating
// "leave the description alone" from "clear the description", and the two write
// different rows.
type Patch struct {
	// Name is written only when HasName. It is never empty when it is written —
	// an empty name is an error, not a clear.
	Name    string
	HasName bool

	// Description is nil for an explicit clear. Written only when HasDescription.
	Description    *string
	HasDescription bool

	// HasPlace covers ALL FIVE location columns at once. Place is nil for an
	// explicit clear, which writes NULL to all five.
	Place    *geoplace.Place
	HasPlace bool
}

// ParseSiteBody validates a decoded request body.
//
// `partial` is true for an EDIT: an absent name is then simply not written,
// where a create requires one. Every other field behaves the same either way —
// the live function only branches on `partial` for the name.
func ParseSiteBody(body map[string]any, partial bool) (Patch, error) {
	var p Patch

	if raw, ok := body["name"]; ok || !partial {
		// `String(b.name == null ? '' : b.name).trim()` — LOOSE, so an explicit
		// null becomes "" and is refused. It used to be strict; see the note
		// below for what that produced.
		name := strings.TrimSpace(jsString(raw, ok))
		if name == "" || len(name) > NameMax {
			return Patch{}, fmt.Errorf("Name must be 1-64 characters")
		}
		p.Name, p.HasName = name, true
	}

	if raw, ok := body["description"]; ok {
		// `String(b.description == null ? '' : b.description)` — LOOSE equality,
		// so null AND undefined both become "". Then `d || null`, which makes an
		// empty string and an explicit null write the same NULL.
		d := ""
		if raw != nil {
			d = jsval.String(raw)
		}
		d = strings.TrimSpace(d)
		if len(d) > DescriptionMax {
			return Patch{}, fmt.Errorf("Description must be 256 characters or fewer")
		}
		p.HasDescription = true
		if d != "" {
			p.Description = &d
		}
	}

	if raw, ok := body["place"]; ok {
		p.HasPlace = true
		if raw != nil {
			place := geoplace.NormalizePlace(raw)
			if place == nil {
				return Patch{}, fmt.Errorf("Pick a town from the list, or clear the location")
			}
			p.Place = place
		}
	}

	return p, nil
}

// jsString is `String(v == null ? ” : v)`, LOOSE — a JSON null is a MISSING name.
//
// It used to be the strict `=== undefined`, and a null then fell through to
// `String(null)`: the four-character string "null", within 1-64, creating a
// record literally called "null". Posting a second one answered 409 "already
// exists", about a field the caller sent as empty — and `{"name": null}` is what
// a cleared form field serialises to, so it was reachable from the UI rather
// than only from curl. Filed as `ToDo.md` §6 and FIXED upstream on 2026-08-27;
// this port follows the fix rather than keeping the quirk, and
// `tools/site-body-cases.js` pins it.
//
// The `present` argument is still needed and is NOT the null question: it
// carries "the key was absent", which on a partial edit means do not write the
// field at all. Go's map cannot tell absent from null, so the caller checks.
func jsString(v any, present bool) string {
	if !present || v == nil {
		return ""
	}
	return jsval.String(v)
}

// Columns turns a Patch into the column -> value map the writer takes.
//
// A KEY IS PRESENT ONLY WHEN THE COLUMN IS TO BE WRITTEN, which is what makes
// `UpdateSite` a partial update: a caller renaming a site sends one key and
// cannot blank a description or a location it never mentioned. A nil VALUE is an
// explicit NULL and is a different thing from an absent key.
//
// The five location columns appear together or not at all - see the note at the
// top of this file. `UpdateSite` whitelists the names again on its side; that is
// deliberate duplication, because the whitelist there is what keeps a column
// name out of SQL text and this map is not the only thing that could reach it.
func (p Patch) Columns() map[string]any {
	out := map[string]any{}
	if p.HasName {
		out["name"] = p.Name
	}
	if p.HasDescription {
		if p.Description == nil {
			out["description"] = nil
		} else {
			out["description"] = *p.Description
		}
	}
	if p.HasPlace {
		if p.Place == nil {
			for _, c := range []string{"lat", "lon", "place_name", "place_region", "place_cc"} {
				out[c] = nil
			}
		} else {
			out["lat"] = p.Place.Lat
			out["lon"] = p.Place.Lon
			out["place_name"] = p.Place.Name
			out["place_region"] = p.Place.Region
			out["place_cc"] = p.Place.CC
		}
	}
	return out
}
