// Package collection resolves the effective collection config for one router —
// the port of `src/collection.js`'s `resolveCollection`.
//
// ── WHAT IT IS FOR ──────────────────────────────────────────────────────────
//
// A fleet is not uniform. A hAP ac2 acting as an access point has no routed
// traffic, no DHCP clients and no wireless registrations, yet without this it is
// asked for the same concurrent channels as a 1 GB hAP ax3 — and the documented
// bottleneck is concurrent API channels on the MikroTik, not data volume. So
// per-router intervals, per-router stream-vs-poll, and turning a collector off
// entirely are the levers that matter (#105).
//
// ── THE PORT HAS BEEN RUNNING WITHOUT IT ────────────────────────────────────
//
// `session.Session` and `routers.Pool` both pass `0` for every interval and
// construct every collector, so every router in a fleet is served identically.
// This package is the decision; WIRING it into those two is a separate step,
// because a collector that must not be constructed cannot be handled by skipping
// `Start()` — eleven of the live sixteen open their streams from the
// constructor, and the port's own collectors will need the same treatment.
//
// ── THE REGISTRY IS EMBEDDED, NOT TYPED ─────────────────────────────────────
//
// Everything derives from the collector registry: intervals, stream keys, the
// disableable set, the dependency edges. It is 27 rows of data, and a Go literal
// copied by hand is a transcription error waiting to happen — one wrong
// `pollable` and a collector silently loses its poll path.
//
// `collection_tables.json` is GENERATED from the live module by
// `tools/collection-cases.js` and embedded here, the same arrangement
// `internal/store` uses for `settings_tables.json`. `--check` fails when it
// drifts, so the registry cannot rot quietly.
//
// ── MUTATIONS (2026-08-25), eight of eight killed ───────────────────────────
//
//	the global interval beats the router override      1
//	a stream override rejects the STRING "true"        1
//	an unknown mode is honoured                        1
//	a non-pollable collector may poll                 29
//	pollIfaces takes the wrong default                27
//	the pingEnabled kill switch is ignored             2
//	the dependency cascade never runs                  2
//	the clamp bounds are not applied                   2
package collection

import (
	_ "embed"
	"encoding/json"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
)

//go:embed collection_tables.json
var tablesJSON []byte

// Collector is one registry row, reduced to what a resolver consumes.
type Collector struct {
	Key string `json:"key"`
	// Label is the human name `GET /api/collectors` sends. Carried in the
	// generated tables rather than typed here, for the same reason as the rest of
	// the registry: it is the live app's own wording.
	Label         string `json:"label"`
	PollKey       string `json:"pollKey"`
	DefaultPollMs int    `json:"defaultPollMs"`
	StreamKey     string `json:"streamKey"`
	Pollable      bool   `json:"pollable"`
	Disableable   bool   `json:"disableable"`
	// EmptyKey names the payload list(s) whose emptiness means "nothing to
	// report here". Its PRESENCE, with Disableable, is what makes a collector
	// eligible for dormancy — the live filter is
	// `_COLLECTOR_DEFS.filter(c => c.emptyKey && c.disableable)`.
	//
	// Always a list here. The live value is a string for some collectors and an
	// array for others, and `payloadEmpty` immediately normalises; the generator
	// does it instead, and asserts the shape so a third form fails there rather
	// than arriving as an empty list that silently makes a collector ineligible.
	EmptyKey []string `json:"emptyKey"`
	// SessionProp is the property the collector hangs off on the live session
	// object. Carried for the same reason as Label: the same names, not a second
	// set typed here.
	SessionProp string   `json:"sessionProp"`
	Requires    []string `json:"requires"`
}

type tables struct {
	Registry   []Collector           `json:"registry"`
	PollBounds map[string][2]float64 `json:"pollBounds"`
}

var loaded = mustTables()

func mustTables() tables {
	var t tables
	if err := json.Unmarshal(tablesJSON, &t); err != nil {
		// A build-time asset that does not parse is not a runtime condition.
		panic("collection: collection_tables.json: " + err.Error())
	}
	return t
}

// Collectors is the registry, in the order the live module declares it.
func Collectors() []Collector { return loaded.Registry }

// The live defaults, spelled here because they are not registry rows.
const (
	defaultMode         = "stream"
	defaultPollIfacesMs = 60000
)

// Resolved is the effective config for one router.
type Resolved struct {
	Mode string
	// Poll is keyed by collector, plus `ifaces` — interfaceStatus's METADATA
	// interval, which has no registry row of its own but is override-able like
	// any other.
	Poll    map[string]int
	Stream  map[string]bool
	Enabled map[string]bool
	// Overrides is passed through so a caller can tell an INHERITED value from a
	// PINNED one. The live comment says why: a settings live-patch must not drag
	// a pinned router back to the fleet default.
	Overrides map[string]any
}

// Router is the record's `collection` block, already decoded.
type Router struct {
	Mode      string
	Off       []string
	Overrides map[string]any
}

// Resolve computes one router's effective config.
//
// Precedence, lowest to highest:
//
//	interval : global setting  <  router override
//	delivery : router mode     <  router per-collector override
//
// Delivery takes NO global input by design: stream-vs-poll is a property of the
// router, not of the installation. Mode switches delivery only and never touches
// intervals, so choosing Poll cannot silently also mean "slower" — which would
// be unrecoverable from the UI.
func Resolve(settings map[string]any, r *Router) Resolved {
	mode := defaultMode
	var off []string
	ovr := map[string]any{}
	if r != nil {
		// AN UNKNOWN MODE IS NOT HONOURED. `MODES.includes(coll.mode)` on the
		// live side; a hand-edited routers.json saying "sideways" gets the
		// default rather than a third behaviour nothing implements.
		if r.Mode == "stream" || r.Mode == "poll" {
			mode = r.Mode
		}
		off = r.Off
		if r.Overrides != nil {
			ovr = r.Overrides
		}
	}

	out := Resolved{
		Mode:      mode,
		Poll:      map[string]int{},
		Stream:    map[string]bool{},
		Enabled:   map[string]bool{},
		Overrides: ovr,
	}

	for _, c := range loaded.Registry {
		// INTERVAL: override, then global, then the collector's own default.
		var raw any
		if c.PollKey != "" {
			if v, ok := ovr[c.PollKey]; ok {
				raw = v
			} else if v, ok := settings[c.PollKey]; ok {
				raw = v
			} else {
				raw = c.DefaultPollMs
			}
		} else {
			raw = c.DefaultPollMs
		}
		if c.PollKey != "" {
			if n, ok := clamp(c.PollKey, raw); ok {
				out.Poll[c.Key] = n
			} else {
				// `clampPollValue` returns null for anything non-finite, and the
				// live line reads `clamped === null ? c.defaultPollMs : clamped`.
				out.Poll[c.Key] = c.DefaultPollMs
			}
		} else {
			// No pollKey: `Math.trunc(Number(raw) || 0)`, with no bounds.
			out.Poll[c.Key] = trunc(raw)
		}

		// DELIVERY.
		switch {
		case !c.Pollable:
			// logs, traffic: a stream is the only path they have.
			out.Stream[c.Key] = true
		case c.StreamKey != "":
			if v, ok := ovr[c.StreamKey]; ok {
				// THE STRING FORM IS ACCEPTED TOO. The live test is
				// `=== true || === 'true'`, because a form submits strings; a
				// port reading only the boolean would ignore a real setting.
				out.Stream[c.Key] = v == true || v == "true"
			} else {
				out.Stream[c.Key] = mode != "poll"
			}
		default:
			// bandwidth: timer-driven, never a stream.
			out.Stream[c.Key] = false
		}

		out.Enabled[c.Key] = !c.Disableable || !slices.Contains(off, c.Key)
	}

	// `pollIfaces` is interfaceStatus's metadata interval — override-able, but
	// with its own default rather than a registry row.
	var ifRaw any
	if v, ok := ovr["pollIfaces"]; ok {
		ifRaw = v
	} else if v, ok := settings["pollIfaces"]; ok {
		ifRaw = v
	}
	if n, ok := clamp("pollIfaces", ifRaw); ok {
		out.Poll["ifaces"] = n
	} else {
		out.Poll["ifaces"] = defaultPollIfacesMs
	}

	// A separate GLOBAL kill switch, applied after the per-router off list and
	// still winning over it.
	if v, ok := settings["pingEnabled"]; ok && v == false {
		out.Enabled["ping"] = false
	}

	// DEPENDENCIES CASCADE, in a loop.
	//
	// Done here rather than in the UI so a hand-edited routers.json cannot
	// produce a combination that silently breaks a card: bandwidth has no fetch
	// of its own and reads the table only `conns` fills.
	//
	// The loop matters even though today's registry is one edge deep — see
	// `tools/collection-cases.js`, which records that a single pass would pass
	// every case, and that a second edge is a one-line registry change away.
	for changed := true; changed; {
		changed = false
		for _, c := range loaded.Registry {
			if !out.Enabled[c.Key] {
				continue
			}
			for _, dep := range c.Requires {
				if !out.Enabled[dep] {
					out.Enabled[c.Key] = false
					changed = true
					break
				}
			}
		}
	}
	return out
}

// clamp is `clampPollValue`: a non-finite input has NO value (the caller
// substitutes a default), and a known key is bounded.
func clamp(key string, raw any) (int, bool) {
	f, ok := num(raw)
	if !ok {
		return 0, false
	}
	n := math.Trunc(f)
	if b, has := loaded.PollBounds[key]; has {
		n = math.Max(b[0], math.Min(b[1], n))
	}
	return int(n), true
}

// trunc is `Math.trunc(Number(raw) || 0)` — the no-bounds path.
func trunc(raw any) int {
	f, ok := num(raw)
	if !ok {
		return 0
	}
	return int(math.Trunc(f))
}

// num is JavaScript's `Number(x)` for the shapes a settings file holds. A JSON
// decode gives float64 for every number, so that is the case that matters; the
// others are here because a caller may hand over a typed value.
func num(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// ParseRouter decodes a router record's `collection` block.
//
// IT NEVER FAILS, by design. The block is operator-editable and the live side
// tolerates rubbish in it — a non-array `off` and a non-object `overrides` are
// both silently ignored rather than honoured — so this mirrors that: anything it
// cannot make sense of is dropped, and a nil result resolves to the fleet
// defaults.
//
// The alternative, returning an error, would push the caller into deciding what
// to do with a router whose config is malformed, and the original has already
// decided: ignore the field, serve the router.
func ParseRouter(raw []byte) *Router {
	if len(raw) == 0 {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not an object at all (`"collection": "yes"`, or malformed JSON).
		return nil
	}
	r := &Router{}
	if s, ok := doc["mode"].(string); ok {
		r.Mode = s
	}
	// `off` must be an ARRAY; a bare string names no collector even when it
	// spells one, because the live `includes` runs on an array.
	if list, ok := doc["off"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok {
				r.Off = append(r.Off, s)
			}
		}
	}
	if m, ok := doc["overrides"].(map[string]any); ok {
		r.Overrides = m
	}
	return r
}

// Fingerprint answers "would this router's session be built differently?".
//
// A router edit rebuilds its session, and a rebuild is a reconnect. Comparing
// fingerprints lets a LABEL-ONLY edit cost nothing, which is what the live
// `collectionFingerprint` exists for. Key and slice order are normalised so a
// cosmetic re-save produces an identical string.
//
// ── IT FOLDS IN SIX FIELDS THAT ARE NOT PART OF `collection` ────────────────
//
// Two from the record (`defaultIf`, `pingTarget`) and four from settings
// (`topN`, `topTalkersN`, `maxConns`, `historyMinutes`). None of them changes
// what `Resolve` returns, and all of them change how the session is built — so
// two routers that resolve identically can still need different sessions.
//
// ── THE TWO GROUPS COERCE DIFFERENTLY, AND THAT IS NOT A TYPO ───────────────
//
// The record fields are `rec[k] || ”`, so `0` and `""` both render empty. The
// settings are `cfg[k] === undefined ? ” : cfg[k]`, so a `0` renders as `0`. A
// port that spelled both the same way would agree on every value except zero —
// and zero is a real setting for `topN`.
func Fingerprint(settings map[string]any, r *Router, rec RecordExtras) string {
	res := Resolve(settings, r)

	pickInt := func(m map[string]int) string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strconv.Itoa(m[k]))
		}
		return strings.Join(parts, ",")
	}
	pickBool := func(m map[string]bool) string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+strconv.FormatBool(m[k]))
		}
		return strings.Join(parts, ",")
	}

	// `rec[k] || ''` — falsy becomes empty, which is why an empty string and a
	// zero are indistinguishable here.
	recPart := "defaultIf=" + truthy(rec.DefaultIf) + ",pingTarget=" + truthy(rec.PingTarget)

	// `cfg[k] === undefined ? '' : cfg[k]` — ABSENT becomes empty, present keeps
	// its value, including zero and false.
	cfgKeys := []string{"topN", "topTalkersN", "maxConns", "historyMinutes"}
	cfgParts := make([]string, 0, len(cfgKeys))
	for _, k := range cfgKeys {
		v, ok := settings[k]
		if !ok {
			cfgParts = append(cfgParts, k+"=")
			continue
		}
		cfgParts = append(cfgParts, k+"="+jsString(v))
	}

	return strings.Join([]string{
		"mode=" + res.Mode,
		"poll:" + pickInt(res.Poll),
		"stream:" + pickBool(res.Stream),
		"enabled:" + pickBool(res.Enabled),
		recPart,
		strings.Join(cfgParts, ","),
	}, "|")
}

// RecordExtras are the router-record fields the fingerprint folds in that are
// not part of the `collection` block.
type RecordExtras struct {
	DefaultIf  any
	PingTarget any
}

// truthy is JavaScript's `x || ”` for the shapes a record holds: anything falsy
// renders empty.
func truthy(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if !x {
			return ""
		}
		return "true"
	case float64:
		if x == 0 {
			return ""
		}
		return jsString(x)
	case int:
		if x == 0 {
			return ""
		}
		return strconv.Itoa(x)
	default:
		return jsString(v)
	}
}

// jsString renders a value the way JavaScript's string concatenation does. A
// JSON decode gives float64 for every number, and `1500 + ”` is "1500" rather
// than "1500.0" — so an integral float must lose its fraction.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case int:
		return strconv.Itoa(x)
	default:
		return ""
	}
}
