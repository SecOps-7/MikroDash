// Package routers builds the Routers page's payloads.
//
// ── PURE, WITH THE SOURCES INJECTED ────────────────────────────────────────
//
// The live `_buildRoutersStats` reaches into three globals: the interactive
// session map, the background session pool, and the store. That is what makes
// it untestable there and what it is worth not reproducing here — the same
// argument the live repo makes for its own guards, and the same shape already
// used by `backups.RunFor` (which takes `Connect`) and `geoplace.AutoGeoAction`
// (which takes its lookup).
//
// So this takes ROWS IN and gives a payload out. Where a row's numbers came
// from — an interactive session or a background one — is the caller's problem,
// and is the part that is still a cutover decision.
//
// ── ABSENT IS null, NOT ZERO, AND THE PAGE CAN TELL ────────────────────────
//
// Every payload-derived field in the original is `sysPay ? sysPay.cpuLoad :
// null`. A router whose system payload has not arrived renders "—"; one
// genuinely idle renders "0%". Flattening the two to 0 would report every
// unreachable router as a healthy one at rest, which is the opposite of what
// the page is for. Hence pointers throughout.
package routers

import (
	"mikrodash/internal/collect"
	"mikrodash/internal/geoplace"
)

// Input is one router as the builder needs it: its record, its reachability,
// and whichever collector payloads are available.
type Input struct {
	ID    string
	Label string // already defaulted to Host by the caller if empty
	Host  string

	// IsActive is `!!s` in the original: whether an INTERACTIVE session exists,
	// not whether the router is reachable. The page uses it to mark the router
	// someone is currently looking at.
	IsActive  bool
	Connected bool
	// Known is whether `Connected` is an OBSERVATION or just its zero value. See
	// the Row field of the same name for why the page needs to be able to tell.
	//
	// HOLDING A SESSION IS NOT AN OBSERVATION. This was set from "some source
	// answered for this router", which is a different and much weaker claim: a
	// pool session exists the instant `Sync` builds it and reports
	// `Connected: false` until its first dial returns. That is what put a red
	// Offline on every card for the first seconds of the Devices page even after
	// the flag was added — the flag was there, and it was being told yes by a
	// source that had not looked.
	Known bool
	// LastError explains why it is offline, so the card can speak for itself.
	// Ignored while connected, exactly as the original ignores it.
	LastError string

	// SiteIDs is the device's site membership (#117). A device may belong to
	// SEVERAL sites; the live builder normalises the record's `siteIds` array
	// against the older singular `siteId`, and `SiteID` below is the mirror the
	// callers still read.
	SiteIDs []string
	// Geo is the router record's `geo` block, decoded. Passed as a map because
	// that is what geoplace.ResolveLocation validates — see its header on why
	// the input is untrusted JSON rather than a struct.
	Geo map[string]any

	// DefaultIf is the interface whose rates the card shows, already resolved
	// through the router record and then the global setting by the caller.
	DefaultIf string

	System     *collect.SystemPayload
	IfStatus   *collect.IfStatusPayload
	DHCPLeases *collect.LeasesPayload
}

// Row is one entry of the `routers:stats` payload.
//
// The JSON names and the null-versus-zero behaviour are the contract: this is
// rendered by a page that must not change.
type Row struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Host      string `json:"host"`
	IsActive  bool   `json:"isActive"`
	Connected bool   `json:"connected"`
	// Known is whether anything has actually LOOKED at this router yet.
	//
	// ── FALSE IS NOT THE SAME CLAIM AS OFFLINE ──────────────────────────────
	//
	// `Connected` is a bool, so a router nothing has reached yet is
	// indistinguishable from one that is genuinely down — and the page said
	// "Offline", in red, about devices it had simply not asked about. On first
	// open that is every device except the selected one, for as long as the pool
	// takes to dial, which is where "they all go online at once after three
	// seconds" comes from.
	//
	// A separate flag rather than making `Connected` tri-state: the client
	// filters on `connected` in four places and validates the payload shape, so
	// widening a boolean everything already reads is a larger change than adding
	// a field old code ignores.
	Known     bool    `json:"known"`
	LastError *string `json:"lastError"`
	// OpenAlerts is a plain int: the original spells it `openAlerts[r.id] || 0`,
	// so a router with nothing open reports 0 rather than null. Independent of
	// `connected` — a router can be reachable and still have something wrong.
	OpenAlerts int `json:"openAlerts"`

	CPU          *int    `json:"cpu"`
	Uptime       *string `json:"uptime"`
	MemPct       *int    `json:"memPct"`
	HddPct       *int    `json:"hddPct"`
	Version      *string `json:"version"`
	BoardName    *string `json:"boardName"`
	Arch         *string `json:"arch"`
	Serial       *string `json:"serial"`
	LicenseLevel *string `json:"licenseLevel"`

	RxMbps *float64 `json:"rxMbps"`
	TxMbps *float64 `json:"txMbps"`
	// Clients is the DHCP lease count, null when that payload has not arrived.
	Clients *int `json:"clients"`

	// SiteIDs and SiteNames are the multi-site answer (#117).
	//
	// THEY CAN BE DIFFERENT LENGTHS, and a caller that zips them will misalign.
	// The live builder resolves names with
	// `_rIds.map(sid => (sitesById.get(sid) || {}).name).filter(Boolean)`, so an
	// id naming a site this viewer cannot see — or one that no longer exists —
	// contributes an id and NO name. Reproduced deliberately: dropping the id
	// too would hide a membership the operator set, and padding the names with
	// an empty string would render a nameless chip.
	SiteIDs   []string `json:"siteIds"`
	SiteNames []string `json:"siteNames"`
	// SiteID and SiteName are BACKWARD-COMPATIBLE MIRRORS of the first entry,
	// which the live payload still sends beside the arrays.
	SiteID   *string `json:"siteId"`
	SiteName *string `json:"siteName"`
	// Geo is where to draw it and how confident to look. Null means UNLOCATED:
	// the map's tray, never a marker at 0,0.
	Geo *geoplace.Location `json:"geo"`
}

// Site is the subset of a sites row this needs, keyed by id by the caller.
type Site struct {
	Name string
	Row  *geoplace.SiteRow
}

// BuildRow assembles one row.
//
// `openAlerts` is looked up rather than passed, because a Go map read already
// gives 0 for a router the grouped query did not mention — which is exactly
// what the original's `|| 0` does with an absent key.
//
// `maySeeWanIp` is the disclosure gate. `/api/localcc` withholds the WAN address
// from callers without `system:settings`, and `geoplace.ResolveLocation` returns
// it rather than omitting it so the decision stays at the boundary that knows
// who is asking. This is that boundary.
func BuildRow(in Input, openAlerts map[string]int, sites map[string]Site, maySeeWanIp bool) Row {
	r := Row{
		ID: in.ID, Label: in.Label, Host: in.Host,
		IsActive: in.IsActive, Connected: in.Connected, Known: in.Known,
		OpenAlerts: openAlerts[in.ID],
	}
	if !in.Connected && in.LastError != "" {
		e := in.LastError
		r.LastError = &e
	}

	if p := in.System; p != nil {
		cpu, mem, hdd := p.CPULoad, p.MemPct, p.HddPct
		r.CPU, r.MemPct, r.HddPct = &cpu, &mem, &hdd
		up, ver, board := p.UptimeRaw, p.Version, p.BoardName
		r.Uptime, r.Version, r.BoardName = &up, &ver, &board
		// ── THREE OF THEM ARE ALREADY POINTERS, AND ARE CARRIED THROUGH ────
		//
		// `Arch`, `Serial` and `LicenseLevel` are `*string` in the payload
		// itself, and the collector's own comment says why: the page renders
		// them as an em dash when absent, and "not read yet" and "this router
		// has no routerboard" must both render as nothing. Taking the address
		// of them here would produce `**string` — which is what the compiler
		// caught — but the subtler error is flattening them to `""`, which
		// renders as nothing while claiming to be an answer.
		r.Arch, r.Serial, r.LicenseLevel = p.Arch, p.Serial, p.LicenseLevel
	}

	if wan := wanIface(in.IfStatus, in.DefaultIf); wan != nil {
		rx, tx := wan.RxMbps, wan.TxMbps
		r.RxMbps, r.TxMbps = &rx, &tx
	}

	if p := in.DHCPLeases; p != nil {
		n := len(p.Leases)
		r.Clients = &n
	}

	// ── SITE MEMBERSHIP (#117) ──────────────────────────────────────────
	//
	// The ids go out as given. The NAMES drop anything unresolvable, so the two
	// slices can differ in length — see the Row fields.
	r.SiteIDs = in.SiteIDs
	if r.SiteIDs == nil {
		// An empty ARRAY, not null: the live builder always sends a list, and a
		// browser doing `.map` over null would throw where the original renders
		// nothing.
		r.SiteIDs = []string{}
	}
	// ── THE TWO ARRAYS ARE ZIPPED BY INDEX ON THE CLIENT ────────────────────
	//
	// So they must stay the SAME LENGTH. An unresolvable id contributes an EMPTY
	// STRING rather than being dropped.
	//
	// This used to `.filter(Boolean)`, faithfully porting what the live builder
	// then did. Both were wrong, and the port was wrong twice over: it reproduced
	// the builder AND the consumer, `web/src/pages/routers.ts`, whose
	// `names[id] = nm[i] || id` is the line that misaligns. A site can be deleted
	// while a device still lists it; dropping the name removed an element from
	// the MIDDLE of the list while leaving the ids intact, so every name after
	// the first dangling membership attached to the wrong site — and picking that
	// entry in the dropdown then filtered to a site that no longer exists.
	//
	// Filed as ../MikroDash/ToDo.md §1, fixed upstream in e76962d, followed here.
	// DO NOT REINTRODUCE THE FILTER: take the blank out at the point of display,
	// which is what the Settings chips already do (`_sitesById` by id, filtered
	// where they render).
	r.SiteNames = []string{}
	for _, id := range r.SiteIDs {
		r.SiteNames = append(r.SiteNames, sites[id].Name)
	}

	// The MIRRORS, and the geo tier, both come from the FIRST id.
	var siteRow *geoplace.SiteRow
	if len(r.SiteIDs) > 0 {
		first := r.SiteIDs[0]
		id := first
		r.SiteID = &id
		// THE PRIMARY SITE SUPPLIES THE GEO TIER, read from the id list rather
		// than from the `SiteID` mirror — the live comment says why: it makes
		// explicit which site is meant when a device is in several.
		if s, ok := sites[first]; ok {
			if s.Name != "" {
				name := s.Name
				r.SiteName = &name
			}
			siteRow = s.Row
		}
	}

	// Resolved SERVER-SIDE so the browser holds one answer per router rather
	// than reimplementing the priority order — a second implementation is one
	// that can disagree.
	if loc := geoplace.ResolveLocation(in.Geo, siteRow); loc != nil {
		if !maySeeWanIp {
			loc.WanIP = ""
		}
		r.Geo = loc
	}
	return r
}

// wanIface finds the interface whose rates the card shows.
func wanIface(p *collect.IfStatusPayload, name string) *collect.Interface {
	if p == nil || name == "" {
		return nil
	}
	for i := range p.Interfaces {
		if p.Interfaces[i].Name == name {
			return &p.Interfaces[i]
		}
	}
	return nil
}

// WanIPFor is `_wanIpFor`: the address the automatic location is derived from.
//
// THE LIVE VALUE WINS, and the interface's first address is the fallback. Both
// are stripped of any prefix length — the original splits on "/" — because a
// geoip lookup wants an address and `198.51.100.7/24` is not one.
func WanIPFor(liveWanIP string, wan *collect.Interface) string {
	if liveWanIP != "" {
		return beforeSlash(liveWanIP)
	}
	if wan != nil && len(wan.IPs) > 0 {
		return beforeSlash(wan.IPs[0])
	}
	return ""
}

func beforeSlash(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i]
		}
	}
	return s
}
