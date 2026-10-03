package resource

// The firewall forms: every property each of the eight rule menus accepts, laid
// out on WinBox's five tabs.
//
// ── WHY A CATALOGUE AND NOT EIGHT FIELD LISTS ───────────────────────────────
//
// A rule takes about sixty properties and the eight menus share most of them.
// Each property is described ONCE here (label, tab, negation, picker), and each
// menu names which properties it has. The menus' lists were read off a router,
// not written from memory: `/console/inspect request=child` on each menu's `add`,
// RouterOS 7.24.1, recorded in internal/resource/testdata/firewall-properties.json. The IPv4 and
// IPv6 sides differ by about a dozen properties, and a table offering one its
// menu refuses fails every save. TestFirewallFormsMatchTheRouter holds each
// menu's list to the recording in BOTH directions, so a property RouterOS adds is
// a failing test, not a field nobody can see.
//
// ── CLEARING ────────────────────────────────────────────────────────────────
//
// A match is REMOVED with `=!prop=`; an empty value is refused ("value of range
// expects range of ip addresses", measured on the CHR). So every optional match
// and action parameter is Clearable with ClearBang. The fields the form had
// before this were not Clearable at all, so emptying Source Address and saving
// left it on the router, silently. BuildArgs skips the clear for a property the
// stored row does not hold, so an edit sends only what changes.
//
// ── NEGATION ────────────────────────────────────────────────────────────────
//
// `Negatable` is the router's own answer: its completion offers a leading `!`
// for exactly these properties, in every menu that has them. Two of them negate
// PER ITEM rather than as a whole (`tcp-flags=syn,!ack`, `hotspot=http,!auth`),
// so a single toggle would mislabel them; they are text, with the `!` typed.

import "strings"

const (
	fwTabGeneral  = "General"
	fwTabAdvanced = "Advanced"
	fwTabExtra    = "Extra"
	fwTabAction   = "Action"
	fwTabStats    = "Statistics"
)

// fwProp is one property's description, shared by every menu that has it.
type fwProp struct {
	ros, label, tab string
	typ             Type
	neg             bool
	// menu, menuValue back a picker with one of the router's own lists.
	menu, menuValue string
	// actions limits an action parameter to the actions that use it.
	actions []string
	// required is required whenever its action is chosen: a mark rule with no
	// mark, or a jump with no target, is refused by the router with no clue
	// which box to fill.
	required    bool
	placeholder string
	help        string
}

// Kept in WinBox's order: a tab draws its fields in the order they are named
// here.
var fwCatalogue = []fwProp{
	// ── General ──
	{ros: "chain", label: "Chain", tab: fwTabGeneral},
	{ros: "src-address", label: "Src. Address", tab: fwTabGeneral, neg: true, placeholder: "10.0.0.0/24"},
	{ros: "dst-address", label: "Dst. Address", tab: fwTabGeneral, neg: true},
	{ros: "src-prefix", label: "Src. Prefix", tab: fwTabGeneral},
	{ros: "dst-prefix", label: "Dst. Prefix", tab: fwTabGeneral},
	{ros: "protocol", label: "Protocol", tab: fwTabGeneral, neg: true},
	{ros: "src-port", label: "Src. Port", tab: fwTabGeneral, neg: true},
	// A port match is a list or a range as often as it is a number, so this is
	// text: `443`, `80,443` and `1000-2000` are all valid to RouterOS.
	{ros: "dst-port", label: "Dst. Port", tab: fwTabGeneral, neg: true, placeholder: "443, or 1000-2000"},
	{ros: "port", label: "Any. Port", tab: fwTabGeneral, neg: true,
		help: "Matches the source or the destination port."},
	{ros: "in-interface", label: "In. Interface", tab: fwTabGeneral, neg: true, menu: "/interface", menuValue: "name"},
	{ros: "out-interface", label: "Out. Interface", tab: fwTabGeneral, neg: true, menu: "/interface", menuValue: "name"},
	{ros: "in-interface-list", label: "In. Interface List", tab: fwTabGeneral, neg: true, menu: "/interface/list", menuValue: "name"},
	{ros: "out-interface-list", label: "Out. Interface List", tab: fwTabGeneral, neg: true, menu: "/interface/list", menuValue: "name"},
	{ros: "packet-mark", label: "Packet Mark", tab: fwTabGeneral, neg: true,
		help: "A mark name, or no-mark for none."},
	{ros: "connection-mark", label: "Connection Mark", tab: fwTabGeneral, neg: true,
		help: "A mark name, or no-mark for none."},
	{ros: "routing-mark", label: "Routing Mark", tab: fwTabGeneral, neg: true, menu: "/routing/table", menuValue: "name"},
	{ros: "connection-type", label: "Connection Type", tab: fwTabGeneral, neg: true},
	// A comma list, not one value: `established,related` is the single most
	// common thing written here. The `!` negates the whole list.
	{ros: "connection-state", label: "Connection State", tab: fwTabGeneral, neg: true,
		placeholder: "established,related",
		help:        "A comma list of: invalid, established, related, new, untracked."},
	{ros: "connection-nat-state", label: "Connection NAT State", tab: fwTabGeneral, neg: true,
		help: "A comma list of: srcnat, dstnat, ein-snat, ein-dnat."},

	// ── Advanced ──
	{ros: "src-address-list", label: "Src. Address List", tab: fwTabAdvanced, neg: true},
	{ros: "dst-address-list", label: "Dst. Address List", tab: fwTabAdvanced, neg: true},
	{ros: "layer7-protocol", label: "Layer7 Protocol", tab: fwTabAdvanced, neg: true,
		menu: "/ip/firewall/layer7-protocol", menuValue: "name"},
	{ros: "content", label: "Content", tab: fwTabAdvanced, neg: true},
	{ros: "tls-host", label: "TLS Host", tab: fwTabAdvanced, neg: true, placeholder: "*.example.com"},
	{ros: "connection-bytes", label: "Connection Bytes", tab: fwTabAdvanced, placeholder: "0-1000000"},
	{ros: "connection-rate", label: "Connection Rate", tab: fwTabAdvanced, neg: true, placeholder: "0-100k"},
	{ros: "per-connection-classifier", label: "Per Connection Classifier", tab: fwTabAdvanced, neg: true,
		placeholder: "both-addresses:2/0"},
	{ros: "src-mac-address", label: "Src. MAC Address", tab: fwTabAdvanced, neg: true},
	{ros: "in-bridge-port", label: "In. Bridge Port", tab: fwTabAdvanced, neg: true, menu: "/interface", menuValue: "name"},
	{ros: "out-bridge-port", label: "Out. Bridge Port", tab: fwTabAdvanced, neg: true, menu: "/interface", menuValue: "name"},
	{ros: "in-bridge-port-list", label: "In. Bridge Port List", tab: fwTabAdvanced, neg: true, menu: "/interface/list", menuValue: "name"},
	{ros: "out-bridge-port-list", label: "Out. Bridge Port List", tab: fwTabAdvanced, neg: true, menu: "/interface/list", menuValue: "name"},
	{ros: "ipsec-policy", label: "IPsec Policy", tab: fwTabAdvanced, placeholder: "in,ipsec",
		help: "A direction and a policy: in or out, then ipsec or none."},
	{ros: "ingress-priority", label: "Ingress Priority", tab: fwTabAdvanced, neg: true},
	{ros: "priority", label: "Priority", tab: fwTabAdvanced, neg: true},
	{ros: "dscp", label: "DSCP (TOS)", tab: fwTabAdvanced, neg: true},
	{ros: "tos", label: "TOS", tab: fwTabAdvanced, neg: true},
	{ros: "tcp-mss", label: "TCP MSS", tab: fwTabAdvanced, neg: true},
	{ros: "packet-size", label: "Packet Size", tab: fwTabAdvanced, neg: true, placeholder: "0-1500"},
	{ros: "random", label: "Random", tab: fwTabAdvanced, help: "A percentage, 1 to 99."},
	// Negated PER FLAG, so no toggle: see the header.
	{ros: "tcp-flags", label: "TCP Flags", tab: fwTabAdvanced, placeholder: "syn,!ack",
		help: "A comma list of: ack, cwr, ece, fin, psh, rst, syn, urg. Prefix a flag with ! to match it unset."},
	{ros: "icmp-options", label: "ICMP Options", tab: fwTabAdvanced, neg: true, placeholder: "8:0",
		help: "Type:code. Needs the protocol set to ICMP."},
	{ros: "ipv4-options", label: "IPv4 Options", tab: fwTabAdvanced},
	{ros: "headers", label: "IPv6 Headers", tab: fwTabAdvanced, neg: true,
		help: "A comma list of: hop, dst, route, frag, ah, esp, none, proto."},
	{ros: "ttl", label: "TTL", tab: fwTabAdvanced, placeholder: "equal:64",
		help: "equal, not-equal, less-than or greater-than, then a number."},
	{ros: "hop-limit", label: "Hop Limit", tab: fwTabAdvanced, placeholder: "equal:64",
		help: "equal, not-equal, less-than or greater-than, then a number."},
	{ros: "realm", label: "Realm", tab: fwTabAdvanced, neg: true},
	{ros: "p2p", label: "P2P", tab: fwTabAdvanced, neg: true},

	// ── Extra ──
	{ros: "connection-limit", label: "Connection Limit", tab: fwTabExtra, neg: true, placeholder: "100,32"},
	{ros: "limit", label: "Limit", tab: fwTabExtra, neg: true, placeholder: "10,5:packet",
		help: "Rate, burst and unit: 10/1s,5:packet."},
	{ros: "dst-limit", label: "Dst. Limit", tab: fwTabExtra, placeholder: "10,5,dst-address",
		help: "Rate, burst, mode, and optionally the expiry."},
	{ros: "nth", label: "Nth", tab: fwTabExtra, neg: true, placeholder: "10,1"},
	{ros: "time", label: "Time", tab: fwTabExtra, neg: true, placeholder: "8h-17h,mon,tue,wed,thu,fri"},
	{ros: "src-address-type", label: "Src. Address Type", tab: fwTabExtra, neg: true},
	{ros: "dst-address-type", label: "Dst. Address Type", tab: fwTabExtra, neg: true},
	{ros: "psd", label: "PSD", tab: fwTabExtra, placeholder: "21,3s,3,1",
		help: "Port scan detection: weight threshold, delay, low and high port weights."},
	// Negated PER ITEM, so no toggle: see the header.
	{ros: "hotspot", label: "Hotspot", tab: fwTabExtra, placeholder: "from-client,!auth",
		help: "A comma list of: auth, from-client, http, local-dst, to-client. Prefix one with ! to negate it."},
	// A yes/no MATCH: unset matches both, so it cannot be a checkbox, which has
	// no third state and would write `no` on every save.
	{ros: "fragment", label: "Fragment", tab: fwTabExtra},

	// ── Action ──
	{ros: "action", label: "Action", tab: fwTabAction},
	{ros: "log", label: "Log", tab: fwTabAction, typ: TypeBool},
	{ros: "log-prefix", label: "Log Prefix", tab: fwTabAction},
	{ros: "jump-target", required: true, label: "Jump Target", tab: fwTabAction, actions: []string{"jump"}},
	{ros: "address-list", label: "Address List", tab: fwTabAction,
		actions: []string{"add-src-to-address-list", "add-dst-to-address-list"}},
	{ros: "address-list-timeout", label: "Timeout", tab: fwTabAction, placeholder: "1d",
		actions: []string{"add-src-to-address-list", "add-dst-to-address-list"},
		help:    "A duration, or none-dynamic / none-static to keep the entry."},
	{ros: "reject-with", label: "Reject With", tab: fwTabAction, actions: []string{"reject"}},
	{ros: "to-addresses", label: "To Addresses", tab: fwTabAction,
		actions: []string{"dst-nat", "src-nat", "netmap", "same", "endpoint-independent-nat"}},
	// `to-address`, SINGULAR, is IPv6 NAT's spelling; the plural is an "unknown
	// parameter" trap on every IPv6 save.
	{ros: "to-address", label: "To Address", tab: fwTabAction,
		actions: []string{"dst-nat", "src-nat", "netmap"}},
	{ros: "to-ports", label: "To Ports", tab: fwTabAction,
		actions: []string{"dst-nat", "src-nat", "masquerade", "redirect", "netmap", "same", "endpoint-independent-nat"}},
	{ros: "randomise-ports", label: "Randomise Ports", tab: fwTabAction,
		actions: []string{"endpoint-independent-nat"}},
	{ros: "same-not-by-dst", label: "Same Not By Dst.", tab: fwTabAction, actions: []string{"same"}},
	{ros: "socks5-server", label: "SOCKS5 Server", tab: fwTabAction, actions: []string{"socksify"}},
	{ros: "socks5-port", label: "SOCKS5 Port", tab: fwTabAction, actions: []string{"socksify"}},
	{ros: "socksify-service", label: "Socksify Service", tab: fwTabAction, actions: []string{"socksify"}},
	{ros: "new-connection-mark", required: true, label: "New Connection Mark", tab: fwTabAction, actions: []string{"mark-connection"}},
	{ros: "new-packet-mark", required: true, label: "New Packet Mark", tab: fwTabAction, actions: []string{"mark-packet"}},
	{ros: "new-routing-mark", required: true, label: "New Routing Mark", tab: fwTabAction, actions: []string{"mark-routing"},
		menu: "/routing/table", menuValue: "name"},
	{ros: "new-dscp", label: "New DSCP", tab: fwTabAction, actions: []string{"change-dscp"},
		help: "0 to 63, or from-priority / from-priority-to-high-3-bits."},
	{ros: "new-mss", label: "New MSS", tab: fwTabAction, actions: []string{"change-mss"}, placeholder: "clamp-to-pmtu",
		help: "A size in bytes, or clamp-to-pmtu."},
	{ros: "new-priority", label: "New Priority", tab: fwTabAction, actions: []string{"set-priority"},
		help: "0 to 7, or from-dscp / from-dscp-high-3-bits / from-ingress."},
	{ros: "new-ttl", label: "New TTL", tab: fwTabAction, actions: []string{"change-ttl"}, placeholder: "set:64",
		help: "set, increment or decrement, then a number."},
	{ros: "new-hop-limit", label: "New Hop Limit", tab: fwTabAction, actions: []string{"change-hop-limit"},
		placeholder: "set:64", help: "set, increment or decrement, then a number."},
	{ros: "route-dst", label: "Route Dst.", tab: fwTabAction, actions: []string{"route"}},
	{ros: "sniff-id", label: "Sniff ID", tab: fwTabAction, actions: []string{"sniff-tzsp"}},
	{ros: "sniff-target", label: "Sniff Target", tab: fwTabAction, actions: []string{"sniff-tzsp"}},
	{ros: "sniff-target-port", label: "Sniff Target Port", tab: fwTabAction, actions: []string{"sniff-tzsp"}},
	// Marking and changing actions default to passthrough=yes, and turning it
	// off is how a mangle chain stops after the first match.
	{ros: "passthrough", label: "Passthrough", tab: fwTabAction, typ: TypeBool},
	{ros: "comment", label: "Comment", tab: fwTabAction},
	{ros: "disabled", label: "Disabled", tab: fwTabAction, typ: TypeBool},
}

// fwStats are shown and never sent: Display fields, read with the row.
var fwStats = []Field{
	{Name: "bytes", ROS: "bytes", Label: "Bytes", Type: TypeText, Display: true, Tab: fwTabStats},
	{Name: "packets", ROS: "packets", Label: "Packets", Type: TypeText, Display: true, Tab: fwTabStats},
}

// fwMenuSpec is what one menu holds: its properties, and the vocabularies that
// differ between menus. Every list here was read off the router and is held to
// internal/resource/testdata/firewall-properties.json.
type fwMenuSpec struct {
	props []string
	// values are the menu's own vocabularies, by property: chain, action and
	// reject-with differ between the menus.
	values map[string][]string
}

// fwProtocols is the protocol list RouterOS completes, the same on both
// families, and every value in it was accepted by all eight menus on the CHR.
//
// `ipv6-icmp` IS NOT HERE. The IPv6 menus refuse that spelling ("input does not
// match any value of protocol", 7.24.1), and the router itself offers `icmpv6`
// on both sides, so `icmpv6` is the one name that works everywhere.
var fwProtocols = []string{"tcp", "udp", "icmp", "icmpv6", "gre", "ipsec-esp", "ipsec-ah",
	"igmp", "ospf", "pim", "vrrp", "sctp", "udp-lite", "dccp", "l2tp", "ipip", "ipencap",
	"etherip", "encap", "egp", "ggp", "hmp", "idpr-cmtp", "ipv6-encap", "ipv6-frag",
	"ipv6-nonxt", "ipv6-opts", "ipv6-route", "iso-tp4", "ddp", "pup", "rdp", "rspf", "rsvp",
	"st", "vmtp", "xns-idp", "xtp"}

// fwShared is every property all eight menus have.
var fwShared = []string{"action", "address-list", "address-list-timeout", "chain", "comment",
	"content", "disabled", "dscp", "dst-address", "dst-address-list", "dst-address-type",
	"dst-limit", "dst-port", "icmp-options", "in-bridge-port", "in-bridge-port-list",
	"in-interface", "in-interface-list", "ingress-priority", "ipsec-policy", "jump-target",
	"limit", "log", "log-prefix", "nth", "out-bridge-port", "out-bridge-port-list",
	"out-interface", "out-interface-list", "packet-mark", "packet-size",
	"per-connection-classifier", "port", "priority", "protocol", "random", "src-address",
	"src-address-list", "src-address-type", "src-mac-address", "src-port", "tcp-mss", "time",
	"tos"}

// fwConntrack is every menu but raw: raw runs before connection tracking, so
// there is no connection to match on yet.
var fwConntrack = []string{"connection-bytes", "connection-limit", "connection-mark",
	"connection-rate", "connection-type", "routing-mark"}

func fwProps(lists ...[]string) []string {
	out := append([]string{}, fwShared...)
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

var fwV4Only = []string{"fragment", "hotspot", "ipv4-options", "psd", "ttl"}

// THE ADDRESS TYPES DIFFER BY FAMILY: IPv6 has anycast and unreachable, and no
// broadcast or blackhole.
var fwAddrTypes4 = []string{"unicast", "local", "broadcast", "multicast", "blackhole"}
var fwAddrTypes6 = []string{"unicast", "local", "multicast", "anycast", "unreachable"}

// fwVals is a menu's own vocabularies, with the address types of its family.
func fwVals(v6 bool, own map[string][]string) map[string][]string {
	t := fwAddrTypes4
	if v6 {
		t = fwAddrTypes6
	}
	own["src-address-type"], own["dst-address-type"] = t, t
	return own
}

var fwV6Only = []string{"headers", "hop-limit"}

var fwMenus = map[string]fwMenuSpec{
	"/ip/firewall/filter": {
		props: fwProps(fwV4Only, fwConntrack, []string{"connection-nat-state", "connection-state",
			"layer7-protocol", "p2p", "realm", "reject-with", "tcp-flags", "tls-host"}),
		values: fwVals(false, map[string][]string{
			"chain": {"input", "forward", "output"},
			"action": {"accept", "drop", "reject", "tarpit", "log", "passthrough",
				"fasttrack-connection", "jump", "return", "add-src-to-address-list", "add-dst-to-address-list"},
			"reject-with": {"icmp-network-unreachable", "icmp-host-unreachable", "icmp-port-unreachable",
				"icmp-protocol-unreachable", "icmp-net-prohibited", "icmp-host-prohibited",
				"icmp-admin-prohibited", "tcp-reset"},
		}),
	},
	"/ip/firewall/nat": {
		props: fwProps(fwV4Only, fwConntrack, []string{"layer7-protocol", "randomise-ports", "realm",
			"same-not-by-dst", "socks5-port", "socks5-server", "socksify-service",
			"to-addresses", "to-ports"}),
		values: fwVals(false, map[string][]string{
			"chain": {"srcnat", "dstnat", "input", "output"},
			"action": {"accept", "masquerade", "dst-nat", "src-nat", "redirect", "netmap", "same",
				"endpoint-independent-nat", "socksify", "log", "passthrough", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"},
		}),
	},
	"/ip/firewall/mangle": {
		props: fwProps(fwV4Only, fwConntrack, []string{"connection-nat-state", "connection-state",
			"layer7-protocol", "new-connection-mark", "new-dscp", "new-mss", "new-packet-mark",
			"new-priority", "new-routing-mark", "new-ttl", "p2p", "passthrough", "realm",
			"route-dst", "sniff-id", "sniff-target", "sniff-target-port", "tcp-flags", "tls-host"}),
		values: fwVals(false, map[string][]string{
			"chain": {"prerouting", "input", "forward", "output", "postrouting"},
			"action": {"accept", "mark-connection", "mark-packet", "mark-routing", "change-mss",
				"change-ttl", "change-dscp", "set-priority", "clear-df", "strip-ipv4-options", "route",
				"fasttrack-connection", "drop", "sniff-tzsp", "sniff-pc", "log", "passthrough",
				"jump", "return", "add-src-to-address-list", "add-dst-to-address-list"},
		}),
	},
	"/ip/firewall/raw": {
		props: fwProps(fwV4Only, []string{"tcp-flags", "tls-host"}),
		values: fwVals(false, map[string][]string{
			"chain": {"prerouting", "output"},
			"action": {"accept", "drop", "notrack", "log", "passthrough", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"},
		}),
	},
	"/ipv6/firewall/filter": {
		props: fwProps(fwV6Only, fwConntrack, []string{"connection-nat-state", "connection-state",
			"reject-with", "tcp-flags", "tls-host"}),
		values: fwVals(true, map[string][]string{
			"chain": {"input", "forward", "output"},
			// No `tarpit`: the IPv4 filter takes it, this one does not.
			"action": {"accept", "drop", "reject", "log", "passthrough", "fasttrack-connection",
				"jump", "return", "add-src-to-address-list", "add-dst-to-address-list"},
			// SHARES ONLY THREE VALUES WITH IPv4.
			"reject-with": {"icmp-no-route", "icmp-address-unreachable", "icmp-admin-prohibited",
				"icmp-port-unreachable", "icmp-not-neighbour", "icmp-err-src-routing-header",
				"icmp-headers-too-long", "tcp-reset"},
		}),
	},
	"/ipv6/firewall/nat": {
		props: fwProps(fwV6Only, fwConntrack, []string{"connection-state", "tcp-flags", "to-address", "to-ports"}),
		values: fwVals(true, map[string][]string{
			"chain": {"srcnat", "dstnat", "input", "output"},
			// No `same`: IPv4 NAT takes it, this one does not.
			"action": {"accept", "masquerade", "dst-nat", "src-nat", "redirect", "netmap",
				"log", "passthrough", "jump", "return", "add-src-to-address-list", "add-dst-to-address-list"},
		}),
	},
	"/ipv6/firewall/mangle": {
		props: fwProps(fwV6Only, fwConntrack, []string{"connection-nat-state", "connection-state",
			"dst-prefix", "new-connection-mark", "new-dscp", "new-hop-limit", "new-mss",
			"new-packet-mark", "new-priority", "new-routing-mark", "passthrough", "sniff-id",
			"sniff-target", "sniff-target-port", "src-prefix", "tcp-flags", "tls-host"}),
		values: fwVals(true, map[string][]string{
			"chain": {"prerouting", "input", "forward", "output", "postrouting"},
			// `change-hop-limit` where IPv4 has `change-ttl`; no `route`, no
			// `fasttrack-connection`; `snpt` and `dnpt` are IPv6's own.
			"action": {"accept", "mark-connection", "mark-packet", "mark-routing", "change-mss",
				"change-hop-limit", "change-dscp", "set-priority", "snpt", "dnpt", "drop",
				"sniff-tzsp", "sniff-pc", "log", "passthrough", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"},
		}),
	},
	"/ipv6/firewall/raw": {
		props: fwProps(fwV6Only, []string{"tcp-flags", "tls-host"}),
		values: fwVals(true, map[string][]string{
			"chain": {"prerouting", "output"},
			"action": {"accept", "drop", "notrack", "log", "passthrough", "jump", "return",
				"add-src-to-address-list", "add-dst-to-address-list"},
		}),
	},
}

// fwSharedValues are the vocabularies that do not differ between menus.
var fwSharedValues = map[string][]string{
	"protocol":        fwProtocols,
	"connection-type": {"ftp", "h323", "irc", "pptp", "quake3", "sip", "tftp"},
	"ipv4-options": {"any", "loose-source-routing", "no-record-route", "no-router-alert",
		"no-source-routing", "no-timestamp", "none", "record-route", "router-alert",
		"strict-source-routing", "timestamp"},
	"p2p": {"all-p2p", "bit-torrent", "blubster", "direct-connect", "edonkey",
		"fasttrack", "gnutella", "soulseek", "warez", "winmx"},
	"fragment":        {"yes", "no"},
	"randomise-ports": {"yes", "no"},
	"same-not-by-dst": {"yes", "no"},
}

// fwCamel is a property's wire name: `src-address` is `srcAddress`. The names
// the form had before the catalogue all follow it, so history recorded under
// them still reads.
func fwCamel(ros string) string {
	parts := strings.Split(ros, "-")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// fwForm builds one menu's fields, fresh: two resources sharing a slice would
// let a later per-menu tweak leak sideways.
func fwForm(menu string) []Field {
	m, ok := fwMenus[menu]
	if !ok {
		panic("firewall: no property list for " + menu)
	}
	has := map[string]bool{}
	for _, p := range m.props {
		has[p] = true
	}
	out := []Field{}
	for _, p := range fwCatalogue {
		if !has[p.ros] {
			continue
		}
		f := Field{Name: fwCamel(p.ros), ROS: p.ros, Label: p.label, Tab: p.tab, Type: TypeText,
			Negatable: p.neg, Placeholder: p.placeholder, Help: p.help}
		switch {
		case p.ros == "chain" || p.ros == "action" || p.required:
			f.Required = true
		case p.typ == TypeBool:
			f.Type, f.Clearable = TypeBool, true
		case p.ros == "comment":
			// Cleared with an empty value, as it always has been.
			f.Clearable = true
		default:
			f.Clearable, f.ClearBang = true, true
		}
		vals := m.values[p.ros]
		if vals == nil {
			vals = fwSharedValues[p.ros]
		}
		if p.ros == "jump-target" {
			vals = m.values["chain"]
		}
		switch {
		case vals != nil:
			f.OptionsFrom = &OptionsFrom{Values: append([]string{}, vals...)}
		case p.menu != "":
			f.OptionsFrom = &OptionsFrom{Menu: p.menu, Value: p.menuValue}
		}
		if p.actions != nil {
			f.ShowIf = &ShowIf{Field: "action", In: append([]string{}, p.actions...)}
		}
		out = append(out, f)
	}
	return append(out, fwStats...)
}
