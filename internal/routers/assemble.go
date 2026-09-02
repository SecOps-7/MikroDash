package routers

// Assembling the whole `routers:stats` payload — the port of
// `_buildRoutersStats` (`src/index.js`).
//
// `BuildRow` maps ONE router's data to one row. This decides, for every router,
// WHICH data that is: the interactive session's if somebody has the router open,
// the background pool's otherwise. That choice is the whole of this file, and it
// has one trap in it worth the reading.
//
// ── THE TRAP: AN INTERACTIVE SESSION WINS EVEN WHEN IT KNOWS NOTHING ────────
//
// The original is `s ? s.system.lastPayload : (bg ? bg.systemPayload : null)`.
// The ternary tests whether a SESSION EXISTS, not whether it has a payload — so
// a router someone just opened reports nulls until its first poll lands, even
// though the background pool may still hold perfectly good numbers from a second
// ago.
//
// That reads like a bug and is not one to fix here. The two sources are
// different connections to the same router, and a row that mixed them would show
// a CPU figure from one and an uptime from the other. A port that "improved" it
// would also make the Routers page flicker differently from the live one, which
// is the line this project does not cross. Reproduced deliberately, and pinned.
//
// ── RESOLVED ONCE, NOT PER ROUTER ───────────────────────────────────────────
//
// Open alerts, the site list and the WAN-address permission are resolved once
// for the whole payload in the original, with a comment saying why: this runs on
// a 2-second timer for every socket with the page open. They arrive here already
// resolved, for the same reason.
//
// ── WHAT IS NOT HERE ────────────────────────────────────────────────────────
//
// The auto-geo refresh (`_refreshAutoGeo`, and the list re-broadcast when it
// writes) is a WRITE, and this stays a pure function over the state it is given.
// `AutoGeoAction` is already ported and pure; wiring it belongs to the handler
// that owns the store, not to the assembler.
//
// ── MUTATIONS (2026-08-25), six of six killed ───────────────────────────────
//
//   fall back to the pool when the session has no payload   the trap above
//   treat a nil Visible as an empty one                     2 tests
//   include disabled routers
//   global default interface beats the router's own
//   drop the "ether1" last resort
//   derive isActive from connectedness

import "mikrodash/internal/collect"

// StatsRouter is the slice of a router record this payload needs.
//
// Deliberately not `store.Router`, for the reason `RouterConfig` gives in
// pool.go: this package does no store I/O, and taking the record would drag the
// store in. `Geo` is the record's geo block as decoded JSON, which is what
// `geoplace.ResolveLocation` validates.
type StatsRouter struct {
	ID       string
	Label    string
	Host     string
	Disabled bool
	// SiteIDs is the device's site membership (#117). A record carrying only the
	// older singular `siteId` is normalised into a one-element slice by the
	// caller, exactly as the live `_rtrSiteIds` does.
	SiteIDs   []string
	DefaultIf string
	Geo       map[string]any
}

// MainSession is what an INTERACTIVE session knows. One exists only for a router
// somebody currently has open.
type MainSession struct {
	// Connected is the live `mainEntry.rosConnected`, not "a session object
	// exists" — a session is created before it connects.
	Connected bool
	// Known is whether `Connected` has been ANSWERED yet, which is the other
	// half of the sentence above: a session created and not yet connected reads
	// false here and must not be rendered as offline.
	Known      bool
	LastError  string
	System     *collect.SystemPayload
	IfStatus   *collect.IfStatusPayload
	DHCPLeases *collect.LeasesPayload
}

// StatsSources is everything the payload is built from, all resolved.
type StatsSources struct {
	Routers []StatsRouter
	// Main holds only routers with an interactive session. PRESENCE is what
	// `isActive` reports and what decides which payloads a row reads.
	Main map[string]MainSession
	// Background is the pool's cache, keyed by router id.
	Background map[string]Summary

	// DefaultIf is the GLOBAL setting, used when a router names no interface of
	// its own. The original falls back again to "ether1" after it.
	DefaultIf string

	OpenAlerts map[string]int
	Sites      map[string]Site
	// MaySeeWanIp is `system:settings`, resolved once per build. It withholds the
	// WAN address from the geo block for anyone without it.
	MaySeeWanIp bool
	// Visible is the RBAC-readable set. A NIL map means no restriction — which is
	// not the same as an EMPTY one, where a principal may read nothing. Getting
	// those two the same way round is the difference between a locked-down user
	// seeing the whole fleet and an unrestricted one seeing none of it.
	Visible map[string]bool
}

// The original's last fallback when neither the router nor the settings names an
// interface. Spelled here rather than assumed, because a port that only ever
// used a fallback would watch the wrong link on every router.
const fallbackDefaultIf = "ether1"

// BuildStats produces the `routers:stats` payload, in the order the routers were
// given — the original maps over its own filtered list and does not sort.
func BuildStats(src StatsSources) []Row {
	out := make([]Row, 0, len(src.Routers))
	for _, r := range src.Routers {
		// DISABLED ROUTERS ARE NOT IN THE PAYLOAD AT ALL. They are not offline
		// rows; the original filters them out before anything else.
		if r.Disabled {
			continue
		}
		if src.Visible != nil && !src.Visible[r.ID] {
			continue
		}

		main, isActive := src.Main[r.ID]
		bg, hasBG := src.Background[r.ID]

		in := Input{
			ID:        r.ID,
			Label:     r.Label,
			Host:      r.Host,
			IsActive:  isActive,
			SiteIDs:   r.SiteIDs,
			Geo:       r.Geo,
			DefaultIf: defaultIfFor(r.DefaultIf, src.DefaultIf),
		}

		// ONE SOURCE PER ROW, chosen by whether a session EXISTS — see the
		// header. Mixing them would put one connection's CPU beside another's
		// uptime.
		switch {
		case isActive:
			in.Known = main.Known
			in.Connected = main.Connected
			in.LastError = main.LastError
			in.System, in.IfStatus, in.DHCPLeases = main.System, main.IfStatus, main.DHCPLeases
		case hasBG:
			// FROM THE SUMMARY, not hardcoded. A pool session exists before it
			// has dialled; see Input.Known.
			in.Known = bg.Known
			in.Connected = bg.Connected
			in.LastError = bg.LastError
			in.System, in.IfStatus, in.DHCPLeases = bg.System, bg.IfStatus, bg.DHCPLeases
		default:
			// Known to the fleet, served by NEITHER pool — so nothing has asked
			// this router anything yet, and `Connected = false` is the zero
			// value rather than an observation.
			//
			// `Known` is what carries that difference to the page. The original
			// produces `connected: false, lastError: null` here and the card
			// rendered it as a red "Offline", which is a claim the server is in
			// no position to make: on first open of the Devices page every
			// router but the selected one lands in this branch until the
			// overview pool has dialled it.
			in.Known = false
			in.Connected = false
		}

		out = append(out, BuildRow(in, src.OpenAlerts, src.Sites, src.MaySeeWanIp))
	}
	return out
}

// defaultIfFor is `r.defaultIf || cfg.defaultIf || 'ether1'` — the router's
// choice, then the global setting, then the fallback.
func defaultIfFor(router, global string) string {
	if router != "" {
		return router
	}
	if global != "" {
		return global
	}
	return fallbackDefaultIf
}

// SiteIDsOf normalises a record's site membership, the port of `_rtrSiteIds`.
//
//	if (Array.isArray(r.siteIds)) return r.siteIds;
//	return r.siteId ? [r.siteId] : [];
//
// THE ARRAY WINS OUTRIGHT when present, even if EMPTY. A record carrying both an
// empty `siteIds` and a non-empty `siteId` belongs to no site — the array is the
// newer field and an explicit empty one is a deliberate "none", not an absence
// to fall back from. Falling through to the singular there would resurrect a
// membership the operator had just cleared.
func SiteIDsOf(siteIDs []string, siteID string) []string {
	if siteIDs != nil {
		return siteIDs
	}
	if siteID != "" {
		return []string{siteID}
	}
	return nil
}
