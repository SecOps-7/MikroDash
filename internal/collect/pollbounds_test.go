package collect

import (
	"sort"
	"testing"
)

// ── A STORED POLL INTERVAL MEANS WHAT IT SAYS (2026-09-18) ──────────────────
//
// Three places bound an interval, and they were written separately: the store's
// `pollBounds` (what a save accepts), the Settings slider (what it offers), and
// each collector's own clamp (what it runs at). The collectors' clamps were
// carried from the Node collectors; the store's and the slider's were written in
// the Go cutover. So an operator could set WAN to 1 s, see it saved, and get 2 s,
// reported as "the WAN page updates every 2 seconds". Seven more collectors had
// a floor above what the store accepts, and four a ceiling below it.
//
// The operator chose to honour the setting. The invariant, for every poll key:
//
//	slider range  ⊆  store range  ⊆  what the collector runs at
//
// measured, not read from source: each collector is BUILT at the store's
// minimum and maximum and must adopt exactly that interval. A ledger, both
// ways: every collector a poll key feeds has an entry here, and every entry here
// is fed by a poll key.

// effectiveInterval builds a collector at a requested interval and returns the
// interval it adopted.
var effectiveInterval = map[string]func(int) int{
	"arp":          func(ms int) int { return NewARP(emptyReader{}, ms).PollMs() },
	"bandwidth":    func(ms int) int { return NewBandwidth(emptyReader{}, Emit{}, nil, nil, nil, ms).pollMs.ms() },
	"bridges":      func(ms int) int { return NewBridges(emptyReader{}, Emit{}, nil, ms).PollMs() },
	"capsman":      func(ms int) int { return NewCapsman(emptyReader{}, Emit{}, ms).PollMs() },
	"conns":        func(ms int) int { return NewConnections(emptyReader{}, Emit{}, nil, nil, ms).pollMs.ms() },
	"dhcpNetworks": func(ms int) int { return NewDHCPNetworks(emptyReader{}, Emit{}, nil, "", ms).PollMs() },
	"dns":          func(ms int) int { return NewDNS(emptyReader{}, Emit{}, ms).PollMs() },
	"firewall":     func(ms int) int { return NewFirewall(emptyReader{}, Emit{}, ms).pollMs.ms() },
	"ifStatus":     func(ms int) int { return NewIfStatus(emptyReader{}, Emit{}, "r", ms).pollMs.ms() },
	"packages":     func(ms int) int { return NewPackages(emptyReader{}, Emit{}, ms).PollMs() },
	"ping":         func(ms int) int { return NewPing(nil, Emit{}, ms, "192.0.2.1").pollMs.ms() },
	"ppp":          func(ms int) int { return NewPPP(emptyReader{}, Emit{}, ms).PollMs() },
	"queues":       func(ms int) int { return NewQueues(emptyReader{}, Emit{}, nil, ms).PollMs() },
	"rosusers":     func(ms int) int { return NewRosUsers(emptyReader{}, Emit{}, nil, ms).PollMs() },
	"routing":      func(ms int) int { return NewRouting(emptyReader{}, Emit{}, ms).PollMs() },
	"system":       func(ms int) int { return NewSystem(emptyReader{}, Emit{}, ms).PollMs() },
	"talkers":      func(ms int) int { return NewTalkers(emptyReader{}, Emit{}, ms, 0).PollMs() },
	"topology":     func(ms int) int { return NewTopology(emptyReader{}, Emit{}, nil, "r", "l", ms).pollMs.ms() },
	"vlans":        func(ms int) int { return NewVlans(emptyReader{}, Emit{}, nil, nil, ms).pollMs.ms() },
	"vpn":          func(ms int) int { return NewVPN(emptyReader{}, Emit{}, ms).pollMs.ms() },
	"wan":          func(ms int) int { return NewWan(emptyReader{}, Emit{}, nil, ms).PollMs() },
	"wifi":         func(ms int) int { return NewWifi(emptyReader{}, Emit{}, ms).pollMs.ms() },
	"wireless":     func(ms int) int { return NewWireless(emptyReader{}, Emit{}, nil, ms).pollMs.ms() },
}

func TestEveryStoredPollIntervalIsHonoured(t *testing.T) {
	var pollMap map[string]string
	readJSON(t, "../collection/pollmap.json", &pollMap)
	var st struct {
		PollBounds map[string][2]int `json:"pollBounds"`
	}
	readJSON(t, "../store/settings_tables.json", &st)
	var sliders struct {
		Sliders []struct {
			Key      string `json:"key"`
			Min, Max int
		} `json:"sliders"`
	}
	readJSON(t, "../../testdata/poll-tables.json", &sliders)
	slider := map[string][2]int{}
	for _, f := range sliders.Sliders {
		slider[f.Key] = [2]int{f.Min, f.Max}
	}
	if len(pollMap) < 20 || len(st.PollBounds) < 20 || len(slider) < 10 {
		t.Fatalf("pollmap %d, store bounds %d, sliders %d: a table went empty and this check asks nothing",
			len(pollMap), len(st.PollBounds), len(slider))
	}

	keys := make([]string, 0, len(pollMap))
	for k := range pollMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fed := map[string]bool{}
	for _, key := range keys {
		coll := pollMap[key]
		fed[coll] = true
		b, ok := st.PollBounds[key]
		if !ok {
			t.Errorf("%s has no store bounds", key)
			continue
		}
		if s, ok := slider[key]; ok && (s[0] < b[0] || s[1] > b[1]) {
			t.Errorf("%s: the slider offers %v, outside the %v the store accepts", key, s, b)
		}
		eff, ok := effectiveInterval[coll]
		if !ok {
			t.Errorf("%s feeds %q, which has no entry here", key, coll)
			continue
		}
		for _, ms := range b {
			if got := eff(ms); got != ms {
				t.Errorf("%s = %d ms is accepted by the store, and %s runs at %d ms", key, ms, coll, got)
			}
		}
	}
	for coll := range effectiveInterval {
		if !fed[coll] {
			t.Errorf("%q is listed here and no poll key feeds it", coll)
		}
	}
}
