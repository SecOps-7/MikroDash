// Package geoplace validates places, formats them, and decides where a router
// is drawn on the map — the port of the live `src/geoPlace.js`.
//
// ── A SEPARATE PACKAGE FROM internal/geo, MIRRORING THE LIVE SPLIT ──────────
//
// `internal/geo` answers "where is this ADDRESS" by reading geoip-lite's own
// files. This answers "where is this ROUTER", which is a different question with
// a priority order and a validation contract. The live repo splits them the same
// way, and `geo.Location` already means something else — the two-field record
// the collectors read.
//
// ── EVERYTHING HERE IS PURE, AND THAT IS THE POINT ──────────────────────────
//
// The live module says so explicitly: index.js calls `server.listen()` at
// require time and cannot be loaded by a test, so validation living inside the
// request handler would be untestable. Every rule below renders plausibly when
// it is wrong, which is why `testdata/geoplace-cases.json` is generated from the
// live implementation rather than written from these comments.
//
// ── THE INPUT IS DECODED JSON, DELIBERATELY ─────────────────────────────────
//
// `NormalizePlace` takes `any` rather than a typed struct because its job is to
// coerce UNTRUSTED input — a browser body, or a record written by an older
// version. A typed struct would have done the coercion already, silently and by
// different rules, which is exactly what this function exists to prevent.

package geoplace

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// NameMax bounds a place name. Matches the live `NAME_MAX`.
const NameMax = 64

// Place is a validated place: a city or town from the gazetteer, or a fix
// derived from a WAN address. Both sources produce these same five fields —
// that is the point, so the manual picker and the automatic fix cannot disagree
// about where "Berlin, BE, DE" is.
type Place struct {
	Name   string  `json:"name"`
	Region string  `json:"region"`
	CC     string  `json:"cc"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
}

// coord is JavaScript's `Number(v)`, with absence rejected FIRST.
//
// THIS IS THE GULF OF GUINEA GUARD. `Number(null)`, `Number(undefined)` and
// `Number(”)` are 0, -0 and 0 — all finite — so a bare "is it a number" check
// accepts a MISSING coordinate as the equator and puts the router in the sea off
// west Africa. Absence is therefore rejected before any coercion, and a REAL
// zero still passes.
//
// The coercions that follow are JavaScript's, not Go's: a numeric string
// converts, and `Number(true)` is 1. Reproduced because the input is untrusted
// JSON and the original accepts both. Types JS would also coerce but that no
// caller here can produce — arrays and objects — are rejected rather than
// guessed at, and no case in the corpus reaches them.
func coord(v any) (float64, bool) {
	switch t := v.(type) {
	case nil:
		return 0, false
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			// `Number('')` is 0, but an empty string is ABSENCE here — the
			// original rejects it explicitly alongside null and undefined.
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

// str is `typeof x === 'string' ? x.trim() : ”`. A non-string is not coerced —
// `String(42)` would give "42" and the original does not do that.
func str(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// RegionMax is the cap on a subdivision string.
//
// 64, the same as NameMax. The longest region in the shipped gazetteer is
// "Municipality of Sveti Andraž v Slovenskih Goricah" at 50 bytes, measured
// across its 3,772 distinct regions, so this clears the real data with room and
// still bounds what an untrusted caller can store.
const RegionMax = 64

// isRegion accepts a subdivision as the app's OWN gazetteer spells it.
//
// ── THIS WAS `/^[A-Za-z0-9]{0,3}$/`, AND THE DATA MOVED OUT FROM UNDER IT ───
//
// Three characters was right for geoip-lite, whose location record carries a
// fixed 3-byte region field — see `internal/geo/geoip.go`, which still reads
// exactly `rec[locRegion : locRegion+3]`. The live app validated against the
// same data it searched, and the two agreed.
//
// The DB-IP migration replaced that source. `cmd/geogen` writes the English
// subdivision NAME (falling back to the ISO code only when the name is missing),
// so the gazetteer now says "North Rhine-Westphalia" where geoip-lite said "NW".
// 193,366 of its 194,077 rows — 99.6% — carry a region longer than three
// characters.
//
// Nothing connected the two halves, so `GET /api/cities` began handing the
// picker places that `NormalizePlace` refused. Choosing any town and saving a
// site answered 400 "Pick a town from the list, or clear the location", which is
// advice the user had already followed. Issue #120.
//
// The failure has a QUIETER twin, and it is the reason this is validated rather
// than merely tolerated: the router record's `geo` is stored as raw JSON and is
// NOT validated on write, so the device form saved happily and
// `ResolveLocation` — which normalises on READ — dropped the place again. That
// path reports nothing at all; the location simply never takes effect.
//
// So the rule now bounds the string instead of describing geoip-lite's encoding:
// a length cap, and no control characters. Regions are display metadata, never a
// key, and `FormatPlace` already decides which ones are worth showing.
func isRegion(s string) bool {
	if len(s) > RegionMax {
		return false
	}
	for _, r := range s {
		// C0 and DEL. Everything else is somebody's alphabet: the gazetteer
		// holds "Baden-Württemberg", "Provence-Alpes-Côte d'Azur" and
		// "Sveti Andraž v Slovenskih Goricah", so a Latin-only or
		// punctuation-free rule would reject real places all over again.
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// isAlpha2 is `/^[A-Z]{2}$/`, applied after upper-casing.
func isAlpha2(s string) bool {
	if len(s) != 2 {
		return false
	}
	return s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

// NormalizePlace coerces untrusted input into a Place, or nil if it is not one.
//
// Nil rather than an error or a partial value: a malformed place is DROPPED, so
// a bad value can never be written through to storage. Callers read nil as "no
// location".
//
// THE TOWN IS OPTIONAL, and that is not laxness. geoip genuinely does not always
// know one — an address it can place only to a country comes back with an empty
// city and a 1000 km accuracy radius. Requiring a name would drop exactly those
// fixes, the approximate ones the accuracy ring exists to show, and leave the
// router unlocated instead of roughly located.
func NormalizePlace(input any) *Place {
	m, ok := input.(map[string]any)
	if !ok || m == nil {
		// This is also the array case: a JSON array decodes to []any, not a map,
		// so it is rejected here exactly as `Array.isArray` rejects it there.
		return nil
	}

	name := str(m, "name")
	if len(name) > NameMax {
		return nil
	}

	// geoip-lite reports country as ISO-3166-1 alpha-2. Upper-cased so a client
	// sending "de" is accepted, but a three-letter code is not: that is a
	// different standard and would match nothing the gazetteer returns.
	cc := strings.ToUpper(str(m, "cc"))
	if !isAlpha2(cc) {
		return nil
	}

	// Region is the subdivision. Optional — several hundred places have none —
	// and it may be NUMERIC (Japan's Hiroshima is "34") or a full name
	// ("North Rhine-Westphalia"). All of that is valid data; FormatPlace decides
	// whether it is worth showing. See isRegion for why this is no longer the
	// 3-byte test the geoip-lite era needed.
	region := str(m, "region")
	if !isRegion(region) {
		return nil
	}

	lat, latOK := coord(m["lat"])
	lon, lonOK := coord(m["lon"])
	if !latOK || lat < -90 || lat > 90 {
		return nil
	}
	if !lonOK || lon < -180 || lon > 180 {
		return nil
	}

	return &Place{Name: name, Region: region, CC: cc, Lat: lat, Lon: lon}
}

// isAlphaFirst is `/^[A-Za-z]/`.
func isAlphaFirst(s string) bool {
	if s == "" {
		return false
	}
	c := s[0]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// FormatPlace is the human label: "Berlin, BE, DE".
//
// A NUMERIC REGION IS DROPPED. Tens of thousands of places carry a numeric
// subdivision code, so keeping it renders "Motomachi, 34, JP" — which reads as a
// typo rather than as information. An alphabetic code (BE, ENG, IDF, MD) is what
// people recognise, so that is what survives.
func FormatPlace(p *Place) string {
	if p == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if p.Name != "" {
		parts = append(parts, p.Name)
	}
	// A region on its own says nothing useful ("NW"), so it appears only beside
	// a town it qualifies.
	if p.Name != "" && p.Region != "" && isAlphaFirst(p.Region) {
		parts = append(parts, p.Region)
	}
	if p.CC != "" {
		parts = append(parts, p.CC)
	}
	return strings.Join(parts, ", ")
}

// Source is which tier placed the router.
const (
	SourceManual = "manual"
	SourceAuto   = "auto"
	SourceSite   = "site"
)

// Location is where a router is and how confidently we know it.
//
// AccuracyKm and WanIP belong to the `auto` tier only, and the wire format says
// so — see MarshalJSON. AccuracyKm is a POINTER because "no usable accuracy" is
// sent as an explicit null, not as zero: zero would draw a ring of no radius,
// which reads as a survey-grade fix.
type Location struct {
	Lat        float64
	Lon        float64
	Source     string
	Label      string
	AccuracyKm *float64
	WanIP      string
	// CC is the resolved country, and it is NOT part of the JSON — see
	// MarshalJSON, which enumerates its keys rather than reflecting the struct.
	//
	// It exists because the Connections map draws every arc FROM the local
	// country, and its only source was a live geo lookup of the WAN address. A
	// router behind another router has a private WAN, which geolocates to
	// nothing, so `localCC` stayed "ZZ" and the map coloured countries and drew
	// no arcs at all — with no setting that helped, because the town an operator
	// picked was never consulted. Issue #120.
	//
	// The label already carries the country as text ("Marl, North
	// Rhine-Westphalia, DE"), and parsing it back out would be a second,
	// lossier answer to a question this function has already answered.
	CC string
}

// MarshalJSON emits exactly the keys the original emits.
//
// The manual and site tiers return an object with FOUR keys; the auto tier adds
// `accuracyKm` and `wanIp`. Struct tags with `omitempty` cannot express this:
// the auto tier must send `"accuracyKm": null` when there is no usable radius,
// and `omitempty` would drop the key entirely. The page reads this payload, so
// the key set is part of the contract rather than a detail.
func (l Location) MarshalJSON() ([]byte, error) {
	base := map[string]any{
		"lat": l.Lat, "lon": l.Lon, "source": l.Source, "label": l.Label,
	}
	if l.Source == SourceAuto {
		base["accuracyKm"] = l.AccuracyKm
		base["wanIp"] = l.WanIP
	}
	return json.Marshal(base)
}

// SiteRow is the sites table row ResolveLocation consults, in the column names
// the database uses. Lat and Lon are `any` because the column is nullable and
// the absent-versus-zero distinction is the whole point — see coord.
type SiteRow struct {
	Name        string
	Lat         any
	Lon         any
	PlaceName   string
	PlaceRegion string
	PlaceCC     string
}

// ResolveLocation answers where a router is, and how confidently.
//
// THE PRIORITY ORDER IS THE WHOLE FEATURE, so it lives in one function rather
// than being re-derived in the renderer — a second implementation is one that
// can disagree:
//
//  1. the router's own picked place   — a person said so about this router
//  2. the cached fix from its WAN IP  — inferred, possibly a country centroid
//  3. its site's picked place         — a person said so about the group
//  4. nil                             — the map's "no location" tray
//
// A MALFORMED entry falls through rather than failing: a router whose manual
// place does not validate is placed by its automatic fix, not left unlocated.
// That is what makes the tiers a fallback chain rather than a switch.
//
// THE RETURNED WanIP IS DISCLOSURE-CONTROLLED. `/api/localcc` withholds the WAN
// address from callers without `system:settings`, so every caller must strip it
// under the same condition. It is returned here rather than omitted so the
// decision stays at the boundary that knows who is asking.
func ResolveLocation(geo map[string]any, site *SiteRow) *Location {
	if manual := NormalizePlace(mapAt(geo, "place")); manual != nil {
		return &Location{
			Lat: manual.Lat, Lon: manual.Lon, CC: manual.CC,
			Source: SourceManual, Label: FormatPlace(manual),
		}
	}

	if autoRaw := mapAt(geo, "auto"); autoRaw != nil {
		if a := NormalizePlace(autoRaw); a != nil {
			am, _ := autoRaw.(map[string]any)
			loc := &Location{
				Lat: a.Lat, Lon: a.Lon, CC: a.CC,
				Source: SourceAuto, Label: FormatPlace(a),
			}
			// `Number.isFinite(km) && km > 0 ? km : null` — zero, negative and
			// unparseable all become null.
			if km, ok := coord(am["accuracyKm"]); ok && km > 0 {
				loc.AccuracyKm = &km
			}
			// `typeof geo.auto.ip === 'string' ? geo.auto.ip : ''`. NOT trimmed,
			// unlike the other string reads, because it is an address this app
			// stored rather than something a person typed.
			if ip, ok := am["ip"].(string); ok {
				loc.WanIP = ip
			}
			return loc
		}
	}

	if site != nil {
		lat, latOK := coord(site.Lat)
		lon, lonOK := coord(site.Lon)
		// BOTH OR NEITHER. A row with one coordinate set is not a location, and
		// must not be read as the other one being zero.
		if latOK && lonOK && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			sp := NormalizePlace(map[string]any{
				"name": site.PlaceName, "region": site.PlaceRegion, "cc": site.PlaceCC,
				"lat": lat, "lon": lon,
			})
			// Migration 4 reserved lat/lon before there was a picker, so a row
			// may carry coordinates with no place name — the label then falls
			// back to the site's own name rather than being empty.
			label := site.Name
			cc := ""
			if sp != nil {
				label = FormatPlace(sp)
				cc = sp.CC
			}
			return &Location{Lat: lat, Lon: lon, CC: cc, Source: SourceSite, Label: label}
		}
	}

	return nil
}

// mapAt reads a nested value, tolerating a nil parent.
func mapAt(m map[string]any, k string) any {
	if m == nil {
		return nil
	}
	return m[k]
}

// ── the automatic fix ────────────────────────────────────────────────────────

// Actions for AutoGeoAction.
const (
	ActionKeep  = "keep"
	ActionClear = "clear"
	ActionSet   = "set"
)

// Lookup is a geoip-lite result, in the shape AutoGeoAction reads it.
//
// NOT `geo.Location`: that is the two-field record the collectors read, and this
// needs the coordinate pair and the accuracy radius, which the port's geoip
// reader does not decode yet. Taken as a PARAMETER so the decision stays pure
// and testable without a database — the reader can be widened separately.
type Lookup struct {
	City    string
	Region  string
	Country string
	// LL is geoip-lite's `[lat, lon]`. Nil, short, or holding an unusable value
	// all mean "could not place it".
	LL []any
	// Area is the accuracy radius in km: about 5 for a real city fix, 1000 for a
	// country centroid.
	Area any
}

// AutoDecision is the three-way answer.
type AutoDecision struct {
	Action string
	Auto   map[string]any
}

// AutoGeoAction decides what to do with a router's cached automatic location.
//
// ── WHY THREE ANSWERS AND NOT TWO ───────────────────────────────────────────
//
// This is pure because getting it wrong is INVISIBLE. The live module records
// that its first implementation folded two different situations together and
// cleared the cache whenever there was no address to look at — which emptied the
// map of every OFFLINE router, the ones the view exists to show. It took driving
// a browser to notice.
//
//	keep   no address to work from — the router is offline, or its interface
//	       status has not arrived. Nothing new was learned, so nothing already
//	       known may be forgotten.
//	clear  there IS an address and it cannot be placed (RFC1918, CGNAT,
//	       unallocated). The router has moved somewhere unresolvable, so a fix
//	       from its previous address is now a lie.
//	set    a usable fix.
//
// Persisting the answer rather than resolving it live is what lets an offline
// router still appear at its last known position.
func AutoGeoAction(wanIP string, g *Lookup, now int64) AutoDecision {
	if wanIP == "" {
		return AutoDecision{Action: ActionKeep}
	}
	if g == nil || len(g.LL) < 2 {
		return AutoDecision{Action: ActionClear}
	}
	lat, latOK := coord(g.LL[0])
	lon, lonOK := coord(g.LL[1])
	if !latOK || !lonOK {
		return AutoDecision{Action: ActionClear}
	}

	// `Number(g.area) || 0` — unparseable and absent both become 0, which
	// ResolveLocation then reports as a null accuracy.
	area, _ := coord(g.Area)

	return AutoDecision{Action: ActionSet, Auto: map[string]any{
		"name":       g.City,
		"region":     g.Region,
		"cc":         g.Country,
		"lat":        lat,
		"lon":        lon,
		"ip":         wanIP,
		"accuracyKm": area,
		"ts":         now,
	}}
}
