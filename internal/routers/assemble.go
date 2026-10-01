package routers

// Assembling the whole `routers:stats` payload — the port of
// `_buildRoutersStats` (`src/index.js`).
//
// `BuildRow` maps ONE router's data to one row. This decides, for every router,
// WHICH data that is: its session's.
//
// ── ONE SOURCE, SINCE 2026-10-01 ────────────────────────────────────────────
//
// There used to be two - the interactive session's if somebody had the router
// open, the background pool's otherwise - and the trap worth reading was that a
// session WON even when it knew nothing yet, because the original tests whether
// a session exists rather than whether it has a payload. That rule existed so a
// row never mixed two connections' readings, a CPU figure from one and an uptime
// from the other.
//
// The overview pool is gone. Every enabled router is held WARM from startup, so
// it has exactly one session and one connection, and there is nothing to choose
// between or mix. A router with no session yet - added a moment ago, before the
// next fleet sync holds it - is the only other case, and it is reported as not
// yet known rather than as offline.

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
// ── MUTATIONS (2026-08-25), five of the original six still apply, all killed ─
//
//   treat a nil Visible as an empty one                     2 tests
//   include disabled routers
//   global default interface beats the router's own
//   drop the "ether1" last resort
//   derive isActive from connectedness
//
// `isActive` is the VIEWER'S selected router, not presence in `Main` (see
// StatsSources.ActiveID); it moved on 2026-09-19.

import "mikrodash/internal/collect"

// StatsRouter is the slice of a router record this payload needs.
//
// Deliberately not `store.Router`: this package does no store I/O, and taking
// the record would drag the store in. `Geo` is the record's geo block as decoded JSON, which is what
// `geoplace.ResolveLocation` validates.
type StatsRouter struct {
	ID       string
	Label    string
	Host     string
	Disabled bool
	// SiteIDs is the device's site membership (#117). A record carrying only the
	// older singular `siteId` is normalised into a one-element slice by the
	// caller, exactly as the live `_rtrSiteIds` does.
	SiteIDs []string
	Geo     map[string]any
}

// MainSession is what a router's session knows. Since 2026-10-01 every enabled
// router has one - each is held WARM from startup - so this is the one source a
// row reads; the overview pool's `Background` summaries are gone.
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
	DHCPLeases *collect.LeasesPayload
}

// StatsSources is everything the payload is built from, all resolved.
type StatsSources struct {
	Routers []StatsRouter
	// Main holds every router with a live session. PRESENCE decides which
	// payloads a row reads; it no longer says which router is active.
	Main map[string]MainSession
	// ActiveID is the router THIS viewer has selected in the header, and the
	// only row marked `isActive`.
	//
	// ── IT WAS PRESENCE IN `Main`, AND THAT MARKED THE WHOLE FLEET ──────────
	//
	// In the original one session existed, for the one active router, so `!!s`
	// named it. Here the warm holds that replaced `internal/alertpool` keep a
	// session for every enabled router, `Main` is filled from all of them, and
	// every card on the Devices page said "active". The selection is per
	// connection, so the caller passes it rather than this package guessing it
	// from who holds what.
	ActiveID string
	// Online is the DEBOUNCED verdict per router — `internal/connstate`, driven
	// by each router's own "Offline threshold". An absent entry means the
	// debounce has not judged this router yet, which is NOT the same as down:
	// the row then falls back to the live socket state.
	//
	// A map rather than a field on `MainSession`, because the verdict is not the
	// socket's to report: it is the debounce's, one answer per router. When there
	// were two sources here, `Main` and the pool's `Background` disagreeing about
	// `Connected` is exactly the confusion this separation stopped.
	Online map[string]bool

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

		main, hasMain := src.Main[r.ID]

		in := Input{
			ID:       r.ID,
			Label:    r.Label,
			Host:     r.Host,
			IsActive: src.ActiveID != "" && r.ID == src.ActiveID,
			SiteIDs:  r.SiteIDs,
			Geo:      r.Geo,
		}

		if hasMain {
			in.Known = main.Known
			in.Connected = main.Connected
			in.LastError = main.LastError
			in.System, in.DHCPLeases = main.System, main.DHCPLeases
		} else {
			// Known to the fleet and held by NO session yet - a router added a
			// moment ago, before the next fleet sync has held it - so nothing has
			// asked it anything, and `Connected = false` is the zero value rather
			// than an observation.
			//
			// `Known` is what carries that difference to the page. The original
			// produces `connected: false, lastError: null` here and the card
			// rendered it as a red "Offline", which is a claim the server is in
			// no position to make.
			in.Known = false
			in.Connected = false
		}

		// THE BADGE'S FACT, after the switch above has settled the socket's.
		// Falling back to `Connected` keeps a fresh install honest: a router
		// nothing has judged yet reports what its socket says, not Offline.
		in.Online = in.Connected
		if v, ok := src.Online[r.ID]; ok {
			in.Online = v
		}

		out = append(out, BuildRow(in, src.OpenAlerts, src.Sites, src.MaySeeWanIp))
	}
	return out
}

// DefaultIfFor is `r.defaultIf || cfg.defaultIf || 'ether1'` — the router's
// choice, then the global setting, then the fallback.
//
// EXPORTED because the background recorders need the SAME answer the Devices
// page shows. They took `r.DefaultIf` raw, so a router with none streamed
// nothing at all when no browser was attached, while the interactive session
// substituted "WAN1" and the page displayed "ether1" — three answers to one
// question, and the two that mattered were invisible. See `syncPool`.
func DefaultIfFor(router, global string) string {
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
