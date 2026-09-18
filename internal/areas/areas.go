// Package areas declares a RouterOS area once and lets the rest of the app be
// generated from it.
//
// ── WHY A DECLARATION RATHER THAN ANOTHER PAGE ──────────────────────────────
//
// MikroMCP reaches perhaps sixty RouterOS menus MikroDash does not. Each one, as
// a hand-built page, is a collector (the 21-row checklist in
// Collector-Architecture.md), a page key in six places, a nav entry, a visibility
// setting, a module, markup, and a tool — for a table of rows the resource engine
// could already render. Sixty of those is not a roadmap, it is a rewrite.
//
// An area is the same thing said once: which page key, which nav group, which
// resources, which columns, how often to read. Everything else — the page, the
// collector's subscription, the nav entry, the visibility toggle, the `list_`
// tool and `change_row`'s coverage — follows from the registry and the
// declaration.
//
// ── WHAT IT IS NOT ──────────────────────────────────────────────────────────
//
// It is not a second resource registry: an area POINTS at resources, and the
// registry keeps saying what a row is, which fields it has and which guards it
// declares. It is not a second permission model either: `Key` is a page key, so
// the existing per-user, per-router matrix gates an area exactly as it gates a
// hand-built page.
//
// ── THE LEDGERS ARE IN THE TEST, AND THEY FAIL BOTH WAYS ────────────────────
//
// An area naming a page key, nav group or resource that does not exist is a page
// nobody can open, a nav entry in no group, or a table of nothing. Those are
// checked against `internal/pages`, the shell markup and `internal/resource`
// rather than against a list typed beside them.
package areas

import "time"

// Table is one tab of an area: a resource, and the columns the list shows.
//
// The columns are FIELD NAMES from the resource, not a second schema. A column
// naming a field the resource does not declare is a header over an empty cell,
// which the ledger refuses.
type Table struct {
	// Resource is the registry key, e.g. "addressList".
	Resource string
	// Columns are the resource's field names, in the order the table shows them.
	// Empty means every non-secret field, in declaration order.
	Columns []string
	// Title names the tab when an area has more than one table. Empty takes the
	// resource's own label.
	Title string
}

// Area is one generated page.
type Area struct {
	// Key is a page key: the URL, the room, the permission and the visibility
	// guard, exactly as a hand-built page's key is. See CLAUDE.md's table of the
	// six things a page key means.
	Key string
	// Title is what the nav entry and the page header say.
	Title string
	// NavGroup is one of the shell's existing groups — "network", "wireless",
	// "ipsvc", "security", "traffic", "tunnels", "system". A new group is a
	// change to the shell, which is why this is checked against it.
	NavGroup string
	// Icon is the nav entry's picture: the INNER markup of a 24×24 SVG (paths,
	// circles, rects…), stroke-drawn like the shell's hand-built entries, whose
	// `.nav-icon svg` rule supplies the stroke. Every area has its own, so a
	// generated page is recognisable in the collapsed nav.
	//
	// IT REACHES innerHTML (`mountAreaNav` in web/src/pages/area.ts), so it is a
	// constant declared here and nowhere else: never router data, never a
	// setting. `TestEveryAreaHasItsOwnIcon` holds it to plain SVG shapes.
	Icon string
	// Tables are the area's tabs, in order. One table renders without tabs.
	Tables []Table
	// Poll is how often the areas collector re-reads this area's menus while
	// somebody is looking at it. Configuration, so it is always a poll and never
	// a stream: a stream holds an API channel, and these menus change when
	// somebody edits them.
	Poll time.Duration
}

// declared is the catalogue.
//
// Each entry is one generated page. The ledgers in internal/verify hold every
// field of it to something real: the page key to `internal/pages`, the nav group
// to the shell, the resources and columns to the registry, and each resource to a
// fixture and a row in the frozen API surface.
var declared = []Area{
	// ── THE FIRST AREA ──────────────────────────────────────────────────────
	//
	// IP pools: one table, four fields, and a menu nothing else in this app
	// reads. Chosen as the mechanism's first instance BECAUSE it is new — a
	// migration of an existing page would have had two collectors reading one
	// menu until the old one was deleted, and "does the generated page work"
	// would have been asked of a page that was already working.
	{
		Key: "ip-pools", Title: "IP Pools", NavGroup: "ipsvc",
		Icon: `<path d="M8 4H5v16h3"/><path d="M16 4h3v16h-3"/><circle cx="9" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="15" cy="12" r="1"/>`,
		Tables: []Table{{Resource: "ipPool",
			Columns: []string{"name", "ranges", "used", "total", "nextPool", "comment"}}},
		// A pool changes when somebody edits it. Sixty seconds is the
		// configuration cadence the bridges and VLAN collectors use.
		Poll: 60 * time.Second,
	},
	// ── SLICE 6: FIREWALL AND SERVICES ──────────────────────────────────────
	//
	// Address lists: the entries firewall rules match against, static ones and
	// the dynamic ones rules add with a timeout. Nothing else reads the menu.
	{
		Key: "address-lists", Title: "Address Lists", NavGroup: "security",
		Icon: `<path d="M9 6h11"/><path d="M9 12h11"/><path d="M9 18h11"/><circle cx="4.5" cy="6" r="1.2"/><circle cx="4.5" cy="12" r="1.2"/><circle cx="4.5" cy="18" r="1.2"/>`,
		Tables: []Table{{Resource: "addressList",
			Columns: []string{"list", "address", "timeout", "dynamic", "comment"}}},
		Poll: 60 * time.Second,
	},
	// Interface lists: who is in LAN, WAN and the rest, which the firewall
	// matches on. Members first, because changing who is in a list is the
	// common task. Both resources carry the list-membership lockout guard.
	{
		Key: "interface-lists", Title: "Interface Lists", NavGroup: "network",
		Icon: `<rect x="2" y="6" width="20" height="12" rx="2"/><rect x="5" y="9.5" width="3.5" height="5"/><rect x="10.25" y="9.5" width="3.5" height="5"/><rect x="15.5" y="9.5" width="3.5" height="5"/>`,
		Tables: []Table{
			{Resource: "ifListMember", Title: "Members",
				Columns: []string{"list", "interface", "disabled", "dynamic", "comment"}},
			{Resource: "ifList", Title: "Lists",
				Columns: []string{"name", "include", "exclude", "builtin", "comment"}},
		},
		Poll: 60 * time.Second,
	},
	// IP services: the router's own services, and the live connections RouterOS
	// 7.24 lists beside them. The one MikroDash connects through is guarded.
	{
		Key: "ip-services", Title: "Services", NavGroup: "system",
		Icon: `<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M7 9l3 3-3 3"/><path d="M13 15h4"/>`,
		Tables: []Table{{Resource: "ipService",
			Columns: []string{"name", "port", "proto", "availableFrom", "disabled", "dynamic", "remote"}}},
		Poll: 60 * time.Second,
	},
	// Certificates: what the router holds, and until when. Removing the one
	// api-ssl presents is refused while MikroDash speaks TLS.
	{
		Key: "certificates", Title: "Certificates", NavGroup: "security",
		Icon: `<path d="M14 20H5a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v5"/><path d="M7 9h8"/><path d="M7 13h4"/><circle cx="18" cy="15" r="3"/><path d="M16.5 17.6L16 22l2-1 2 1-.5-4.4"/>`,
		Tables: []Table{{Resource: "certificate",
			Columns: []string{"name", "commonName", "privateKey", "trusted", "invalidAfter", "expiresAfter"}}},
		Poll: 60 * time.Second,
	},
	// ── SLICE 7: SYSTEM AND AUTOMATION ──────────────────────────────────────
	//
	// Scripts. Their code, their policy and running them are behind codeGate.
	{
		Key: "scripts", Title: "Scripts", NavGroup: "system",
		Icon: `<polyline points="8 7 3 12 8 17"/><polyline points="16 7 21 12 16 17"/><path d="M14 4l-4 16"/>`,
		Tables: []Table{{Resource: "script",
			Columns: []string{"name", "owner", "policy", "runCount", "lastStarted", "comment"}}},
		Poll: 60 * time.Second,
	},
	// Scheduler. Its on-event and policy are behind codeGate; its timing and
	// enabling are not.
	{
		Key: "scheduler", Title: "Scheduler", NavGroup: "system",
		Icon: `<rect x="3" y="5" width="18" height="16" rx="2"/><path d="M3 10h18"/><path d="M8 3v4"/><path d="M16 3v4"/><path d="M8 14h.01M12 14h.01M16 14h.01M8 18h.01M12 18h.01"/>`,
		Tables: []Table{{Resource: "scheduler",
			Columns: []string{"name", "startTime", "interval", "nextRun", "runCount", "disabled", "comment"}}},
		Poll: 60 * time.Second,
	},
	// NTP client: its settings (the first singleton) and its server list.
	{
		Key: "ntp-client", Title: "NTP Client", NavGroup: "system",
		Icon: `<path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/><path d="M12 7v5l3 2"/>`,
		Tables: []Table{
			{Resource: "ntpClient", Title: "Settings",
				Columns: []string{"enabled", "mode", "servers", "vrf", "status", "syncedServer", "systemOffset"}},
			{Resource: "ntpServer", Title: "Servers",
				Columns: []string{"address", "iburst", "minPoll", "maxPoll", "disabled", "comment"}},
		},
		Poll: 60 * time.Second,
	},
	// Clock: the time zone. The time itself is shown and set by NTP.
	{
		Key: "clock", Title: "Clock", NavGroup: "system",
		Icon: `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
		Tables: []Table{{Resource: "clock",
			Columns: []string{"time", "date", "timeZoneName", "timeZoneAutodetect", "gmtOffset", "dstActive"}}},
		Poll: 60 * time.Second,
	},
	// Logging: rules (which topics go where) and actions (where "where" is).
	{
		Key: "logging", Title: "Logging", NavGroup: "system",
		Icon: `<path d="M6 3h11a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6z"/><path d="M4 7h4M4 12h4M4 17h4"/><path d="M11 8h5"/><path d="M11 12h5"/>`,
		Tables: []Table{
			{Resource: "logRule", Title: "Rules",
				Columns: []string{"topics", "action", "prefix", "disabled", "isDefault", "comment"}},
			{Resource: "logAction", Title: "Actions",
				Columns: []string{"name", "target", "memoryLines", "remote", "isDefault"}},
		},
		Poll: 60 * time.Second,
	},
	// SNMP: its settings (a singleton) and its communities. No password is read.
	{
		Key: "snmp", Title: "SNMP", NavGroup: "system",
		Icon: `<path d="M4 18a8 8 0 1 1 16 0"/><path d="M12 18l4-5"/><circle cx="12" cy="18" r="1"/><path d="M12 10v1M7.5 12.5l.7.7M16.5 12.5l-.7.7"/>`,
		Tables: []Table{
			{Resource: "snmp", Title: "Settings",
				Columns: []string{"enabled", "contact", "location", "trapTarget", "trapVersion", "engineId"}},
			{Resource: "snmpCommunity", Title: "Communities",
				Columns: []string{"name", "addresses", "security", "readAccess", "writeAccess", "disabled"}},
		},
		Poll: 60 * time.Second,
	},
	// Files: see and remove, nothing else. Contents are never read.
	{
		Key: "files", Title: "Files", NavGroup: "system",
		Icon: `<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`,
		Tables: []Table{{Resource: "file",
			Columns: []string{"name", "type", "size", "lastModified"}}},
		Poll: 60 * time.Second,
	},
	// ── SLICE 9: ROUTING AND VPN ────────────────────────────────────────────
	//
	// Routing tables: the tables policy routing looks routes up in. `main` is
	// dynamic and read-only. The table's routes stay on the Routing page.
	{
		Key: "routing-tables", Title: "Routing Tables", NavGroup: "ipsvc",
		Icon: `<rect x="3" y="4" width="18" height="16" rx="2"/><path d="M3 9h18"/><path d="M3 14.5h18"/><path d="M9 4v16"/>`,
		Tables: []Table{{Resource: "routingTable",
			Columns: []string{"name", "fib", "disabled", "dynamic", "invalid", "comment"}}},
		Poll: 60 * time.Second,
	},
	// Routing rules: policy routing, first match wins, so the page draws the
	// reorder arrows. Guarded by rulePath.
	{
		Key: "routing-rules", Title: "Routing Rules", NavGroup: "ipsvc",
		Icon: `<path d="M16 3h5v5"/><path d="M8 3H3v5"/><path d="M12 22v-8.3a4 4 0 0 0-1.2-2.9L3 3"/><path d="M15 9l6-6"/>`,
		Tables: []Table{{Resource: "routingRule",
			Columns: []string{"srcAddress", "dstAddress", "routingMark", "interface", "action", "table", "inactive", "disabled", "comment"}}},
		Poll: 60 * time.Second,
	},
	// OSPF: neighbours first, because "is it up" is the common question; then
	// the instances, areas and interface templates that make it so.
	{
		Key: "ospf", Title: "OSPF", NavGroup: "ipsvc",
		Icon: `<circle cx="12" cy="5" r="2.5"/><circle cx="5" cy="18" r="2.5"/><circle cx="19" cy="18" r="2.5"/><path d="M10.8 7.2L6.2 15.8"/><path d="M13.2 7.2l4.6 8.6"/><path d="M7.5 18h9"/>`,
		Tables: []Table{
			{Resource: "ospfNeighbor", Title: "Neighbors",
				Columns: []string{"routerId", "address", "interface", "area", "state", "adjacency", "stateChanges"}},
			{Resource: "ospfInstance", Title: "Instances",
				Columns: []string{"name", "version", "routerId", "originateDefault", "redistribute", "inactive", "disabled", "comment"}},
			{Resource: "ospfArea", Title: "Areas",
				Columns: []string{"name", "instance", "areaId", "type", "inactive", "disabled", "comment"}},
			{Resource: "ospfTemplate", Title: "Interface Templates",
				Columns: []string{"area", "interfaces", "networks", "type", "cost", "passive", "inactive", "disabled", "comment"}},
		},
		Poll: 60 * time.Second,
	},
	// IPsec: peers, their identities, and the policies that decide what is
	// encrypted, the policies showing their phase-2 state. Guarded by ipsecPath.
	{
		Key: "ipsec", Title: "IPsec", NavGroup: "tunnels",
		Icon: `<rect x="5" y="11" width="14" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/><path d="M12 15v2"/>`,
		Tables: []Table{
			{Resource: "ipsecPolicy", Title: "Policies",
				Columns: []string{"srcAddress", "dstAddress", "protocol", "action", "peer", "tunnel", "ph2State", "disabled", "comment"}},
			{Resource: "ipsecPeer", Title: "Peers",
				Columns: []string{"name", "address", "exchangeMode", "profile", "passive", "responder", "disabled", "comment"}},
			{Resource: "ipsecIdentity", Title: "Identities",
				Columns: []string{"peer", "authMethod", "myId", "remoteId", "generatePolicy", "disabled", "comment"}},
		},
		Poll: 60 * time.Second,
	},
	// OpenVPN: the servers this router runs and the clients it dials out with.
	// The accounts a server admits are the PPP page's secrets.
	{
		Key: "openvpn", Title: "OpenVPN", NavGroup: "tunnels",
		Icon: `<path d="M12 3l7 3v5c0 4.5-3 8.2-7 10-4-1.8-7-5.5-7-10V6z"/><path d="M9 12l2 2 4-4"/>`,
		Tables: []Table{
			{Resource: "ovpnServer", Title: "Servers",
				Columns: []string{"name", "port", "protocol", "certificate", "auth", "cipher", "inactive", "disabled", "comment"}},
			{Resource: "ovpnClient", Title: "Clients",
				Columns: []string{"name", "connectTo", "port", "protocol", "user", "addDefaultRoute", "running", "disabled", "comment"}},
		},
		Poll: 60 * time.Second,
	},
	// VRRP: virtual router addresses shared between routers, and which one
	// holds each. Its scripts are behind codeGate.
	{
		Key: "vrrp", Title: "VRRP", NavGroup: "network",
		Icon: `<rect x="3" y="14" width="7" height="6" rx="1"/><rect x="14" y="14" width="7" height="6" rx="1"/><circle cx="12" cy="5" r="2.5"/><path d="M6.5 14v-3h11v3"/><path d="M12 7.5V11"/>`,
		Tables: []Table{{Resource: "vrrp",
			Columns: []string{"name", "interface", "vrid", "priority", "version", "running", "invalid", "disabled", "comment"}}},
		Poll: 60 * time.Second,
	},
	// PPPoE clients: the uplinks this router dials. Their sessions show on the
	// PPP page; the WAN page shows the uplink they make.
	{
		Key: "pppoe-clients", Title: "PPPoE Clients", NavGroup: "tunnels",
		Icon: `<path d="M4 12h5"/><path d="M15 12h5"/><rect x="9" y="8" width="6" height="8" rx="1.5"/><path d="M17 9l3 3-3 3"/>`,
		Tables: []Table{{Resource: "pppoeClient",
			Columns: []string{"name", "interface", "user", "serviceName", "addDefaultRoute", "running", "invalid", "disabled", "comment"}}},
		Poll: 60 * time.Second,
	},
	// ── THE PROOF: A HAND-BUILT PAGE, MIGRATED ──────────────────────────────
	//
	// IP Addresses was a collector, a page module, markup, a nav entry, a room,
	// a dormancy target, a poll key and a visibility key (#97). It is now this.
	// What changed on the page, deliberately: the two families are two tabs
	// rather than one table with a Family column, visibility is the shared
	// `hiddenAreas` list, and the interval is this one rather than a setting.
	{
		Key: "ip-addresses", Title: "IP Addresses", NavGroup: "network",
		Icon: `<rect x="3" y="3" width="18" height="18" rx="3"/><path d="M8 7v10"/><path d="M12 17V7h3.5a3 3 0 0 1 0 6H12"/>`,
		Tables: []Table{
			{Resource: "ipAddress", Title: "IPv4",
				Columns: []string{"address", "network", "interface", "disabled", "dynamic", "invalid", "comment"}},
			{Resource: "ipv6Address", Title: "IPv6",
				Columns: []string{"address", "interface", "advertise", "disabled", "dynamic", "invalid", "comment"}},
		},
		Poll: 60 * time.Second,
	},
}

// All returns the declared areas.
func All() []Area { return append([]Area(nil), declared...) }

// ByKey resolves one area, or false.
func ByKey(key string) (Area, bool) {
	for _, a := range declared {
		if a.Key == key {
			return a, true
		}
	}
	return Area{}, false
}

// Keys is every area's page key, in declaration order.
func Keys() []string {
	out := make([]string, 0, len(declared))
	for _, a := range declared {
		out = append(out, a.Key)
	}
	return out
}

// Resources is every resource key an area names, deduplicated, in declaration
// order. The areas collector reads exactly these menus.
func Resources() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, a := range declared {
		for _, t := range a.Tables {
			if t.Resource != "" && !seen[t.Resource] {
				seen[t.Resource] = true
				out = append(out, t.Resource)
			}
		}
	}
	return out
}
