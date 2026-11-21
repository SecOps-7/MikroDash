package sharelink

// What a card link's socket may receive: for each shareable Dashboard card,
// the events that paint it and, for each event, the fields its rendering code
// reads (the operator's ruling of 2026-11-20: a card link receives only the
// data its card shows, never the whole shared feed). Every payload to a card
// link is projected onto these paths; an event not listed is not sent.
//
// The fields were read off each card's renderer in web/src/pages (the
// dashboard.ts handlers and the dashboard-card-*.ts files). A card that reads
// a field missing here shows less; a field listed here that the card does not
// read is a leak. TestEveryCardLinkReceivesOnlyItsCardsFields holds each
// card's projection against a payload with every field of the real Go type
// filled.
//
// ── PATHS ───────────────────────────────────────────────────────────────────
//
//	a            the key a, whole
//	a.b          b inside the object a
//	a[].b        b inside every element of the array a (elements keep their
//	             order and count, so two paths into one array merge)
//	a[]          the array a, each object element emptied: its length only
//	a[:1]        the first element of the array a
//	a[k=x|y].b   only the elements whose k is x or y (k=x;j=z for both)
//	a=[]         a as an empty array, whatever the payload holds: for a
//	             handler that reads a sibling card's list unguarded
//	[].b         b inside every element of a payload that is an array

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// CardData is, for one card, each event it takes and the paths it keeps.
type CardData map[string][]string

var connLists = []string{"topSources=[]", "topDestinations=[]", "topCountries=[]", "topPorts=[]"}

func with(base []string, more ...string) []string {
	return append(append([]string{}, base...), more...)
}

var cardData = map[string]CardData{
	"card-traffic": {
		"traffic:history": {"ifName", "points[].ts", "points[].rx_mbps", "points[].tx_mbps"},
		"traffic:update":  {"ifName", "ts", "rx_mbps", "tx_mbps"},
		"ifstatus:names":  {"interfaces[].name", "interfaces[].running", "interfaces[].disabled"},
		"wan:status":      {"ifName", "disabled", "running"},
		"stream:health":   {"collector", "degraded", "restarts"},
	},
	"card-system": {
		"system:update": {"uptimeRaw", "cpuLoad", "memPct", "totalHdd", "hddPct", "boardName", "version",
			"cpuCount", "cpuFreq", "totalMem", "tempC", "updateAvailable", "latestVersion", "updateChannel", "updateStatus"},
	},
	"dc-card-bw": {
		"traffic:update": {"rx_mbps", "tx_mbps"},
	},
	"card-connections": {
		"conn:update": with(connLists, "total", "ts", "protoCounts.tcp", "protoCounts.udp", "protoCounts.icmp",
			"protoCounts.other", "topSources[].ip", "topSources[].name", "topSources[].count",
			"topDestinations[].key", "topDestinations[].count", "topDestinations[].country",
			"topDestinations[].city", "topDestinations[].org", "topDestinations[].cat"),
		"stream:health": {"collector", "degraded", "restarts"},
	},
	"dc-card-destcc": {
		"conn:update": with(connLists, "topCountries[].cc", "topCountries[].count"),
	},
	"dc-card-topcc": {
		"conn:update": with(connLists, "topCountries[].cc", "topCountries[].count", "topCountries[].proto.tcp",
			"topCountries[].proto.udp", "topCountries[].proto.other"),
	},
	"dc-card-flow": {
		"conn:update": with(connLists, "topSources[].ip", "topSources[].name", "topSources[].count",
			"topDestinations[].org", "topDestinations[].country", "topDestinations[].key",
			"topDestinations[].cat", "topDestinations[].count"),
	},
	"dc-card-topports": {
		"conn:update": with(connLists, "topPorts[].port", "topPorts[].count"),
	},
	"dc-card-netflow": {
		"traffic:update": {"ifName", "rx_mbps", "tx_mbps"},
		"ifstatus:update": {"interfaces[].running", "interfaces[].disabled", "interfaces[].rxMbps",
			"interfaces[].txMbps", "interfaces[].type", "interfaces[].name"},
		"ifstatus:names":  {"interfaces[].running", "interfaces[].disabled", "interfaces[].type"},
		"wireless:update": {"clients[]"},
		"lan:wan":         {"wanIp"},
	},
	"dc-card-signal": {"wireless:update": {"clients[].signal"}},
	"dc-card-band":   {"wireless:update": {"clients[].band"}},
	"card-network": {
		"lan:overview": {"internetIfaces[].name", "internetIfaces[].ip", "networks[].cidr", "networks[].gateway",
			"networks[].dns", "networks[].leaseCount"},
	},
	"dc-card-iputil": {"lan:overview": {"totalPoolSize", "totalLeases"}},
	"card-wireguard": {
		"vpn:update": {"tunnels[type=WireGuard;state=active].type", "tunnels[type=WireGuard;state=active].state",
			"tunnels[type=WireGuard;state=active].lastHandshake", "tunnels[type=WireGuard;state=active].endpoint",
			"tunnels[type=WireGuard;state=active].name", "tunnels[type=WireGuard;state=active].interface"},
	},
	"dc-card-physports": {
		"ifstatus:update": {"interfaces[type=ether|sfp|sfp-sfpplus].type", "interfaces[type=ether|sfp|sfp-sfpplus].name",
			"interfaces[type=ether|sfp|sfp-sfpplus].running", "interfaces[type=ether|sfp|sfp-sfpplus].disabled",
			"interfaces[type=ether|sfp|sfp-sfpplus].ips[:1]"},
	},
	"dc-card-fwaction": {
		"firewall:update": {"filter[].disabled", "filter[].action", "nat[].disabled", "nat[].action",
			"mangle[].disabled", "mangle[].action", "raw[].disabled", "raw[].action"},
	},
	"dc-card-logs": {
		"logs:history": {"[].severity", "[].topics", "[].time", "[].message"},
		"logs:new":     {"severity", "topics", "time", "message"},
	},
	"dc-card-wanflow": {
		"wan:update": {"wans[].name", "wans[].rxMbps", "wans[].txMbps", "wans[].routeActive", "ratesAvailable"},
	},
	"dc-card-ping": {
		"ping:update":  {"permissionDenied", "target", "ts", "rtt", "loss", "minRtt", "maxRtt"},
		"ping:history": {"history[].rtt", "history[].loss", "history[].ts", "target", "minRtt", "maxRtt"},
	},
	"card-toptalkers": {
		"talkers:update": {"devices[].name", "devices[].mac", "devices[].rx_mbps", "devices[].tx_mbps", "available"},
	},
	"dc-card-netwatch": {"netwatch:update": {"hosts[].status", "hosts[].name", "hosts[].host"}},
	"dc-card-routes": {
		"routing:update": {"routeCounts.connect", "routeCounts.static", "routeCounts.dynamic", "routeCounts.bgp",
			"routeCounts.ospf", "routeCounts.total"},
	},
	"dc-card-bgp":   {"routing:update": {"summary.total", "summary.established", "summary.down"}},
	"dc-card-agent": {"ai:overview": {"text", "error", "at", "color"}},
}

// CardDataOf is what a shareable card's link may receive.
func CardDataOf(id string) (CardData, bool) {
	if !CardShareable(id) {
		return nil, false
	}
	d, ok := cardData[id]
	return d, ok
}

// ── The projector ───────────────────────────────────────────────────────────

type seg struct {
	key    string // "" for a payload that is itself an array
	arr    bool
	empty  bool                // a=[]
	limit  int                 // a[:N]; 0 is no limit
	filter map[string][]string // a[k=x|y]
}

func parsePath(p string) ([]seg, error) {
	var out []seg
	if key, ok := strings.CutSuffix(p, "=[]"); ok {
		if key == "" || strings.ContainsAny(key, ".[]=") {
			return nil, fmt.Errorf("path %q: only a top-level key can be emptied", p)
		}
		return []seg{{key: key, empty: true}}, nil
	}
	for _, part := range strings.Split(p, ".") {
		s := seg{key: part}
		if i := strings.IndexByte(part, '['); i >= 0 {
			if !strings.HasSuffix(part, "]") {
				return nil, fmt.Errorf("path %q: unclosed [", p)
			}
			s.key, s.arr = part[:i], true
			inner := part[i+1 : len(part)-1]
			switch {
			case inner == "":
			case strings.HasPrefix(inner, ":"):
				n, err := strconv.Atoi(inner[1:])
				if err != nil || n < 1 {
					return nil, fmt.Errorf("path %q: bad limit", p)
				}
				s.limit = n
			default:
				s.filter = map[string][]string{}
				for _, cond := range strings.Split(inner, ";") {
					k, vs, ok := strings.Cut(cond, "=")
					if !ok || k == "" || vs == "" {
						return nil, fmt.Errorf("path %q: bad filter", p)
					}
					s.filter[k] = strings.Split(vs, "|")
				}
			}
		} else if part == "" {
			return nil, fmt.Errorf("path %q: empty segment", p)
		}
		out = append(out, s)
	}
	return out, nil
}

func keep(el any, f map[string][]string) bool {
	m, ok := el.(map[string]any)
	if !ok {
		return false
	}
	for k, allowed := range f {
		v, _ := m[k].(string)
		found := false
		for _, a := range allowed {
			found = found || a == v
		}
		if !found {
			return false
		}
	}
	return true
}

// apply copies what segs name from src into dst, returning the new dst.
func apply(src, dst any, segs []seg) any {
	if len(segs) == 0 {
		return src
	}
	s := segs[0]
	if s.key == "" {
		return applyArray(src, dst, s, segs[1:])
	}
	m, ok := src.(map[string]any)
	if !ok {
		return dst
	}
	out, _ := dst.(map[string]any)
	if out == nil {
		out = map[string]any{}
	}
	if s.empty {
		// Never over a list another path filled, whichever came first.
		if _, has := out[s.key]; !has {
			out[s.key] = []any{}
		}
		return out
	}
	v, present := m[s.key]
	if !present {
		return out
	}
	if s.arr {
		out[s.key] = applyArray(v, out[s.key], s, segs[1:])
	} else {
		out[s.key] = apply(v, out[s.key], segs[1:])
	}
	return out
}

func applyArray(src, dst any, s seg, rest []seg) any {
	arr, ok := src.([]any)
	if !ok {
		return dst
	}
	var els []any
	for _, el := range arr {
		if s.filter == nil || keep(el, s.filter) {
			els = append(els, el)
		}
	}
	if s.limit > 0 && len(els) > s.limit {
		els = els[:s.limit]
	}
	prev, _ := dst.([]any)
	out := make([]any, len(els))
	for i, el := range els {
		var d any
		if i < len(prev) {
			d = prev[i]
		}
		if len(rest) == 0 {
			if _, obj := el.(map[string]any); obj {
				if d == nil {
					d = map[string]any{} // its length only
				}
				out[i] = d
			} else {
				out[i] = el
			}
			continue
		}
		out[i] = apply(el, d, rest)
	}
	return out
}

// Project keeps only paths of a payload. It works on the payload's JSON, so
// a field is kept by its wire name.
func Project(paths []string, payload any) (json.RawMessage, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	var src any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&src); err != nil {
		return nil, err
	}
	var out any
	for _, p := range paths {
		segs, err := parsePath(p)
		if err != nil {
			return nil, err
		}
		out = apply(src, out, segs)
	}
	if out == nil {
		if _, isArr := src.([]any); isArr {
			out = []any{}
		} else {
			out = map[string]any{}
		}
	}
	return json.Marshal(out)
}
