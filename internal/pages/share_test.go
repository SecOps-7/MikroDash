package pages

import (
	"sort"
	"testing"
)

// The operator's rulings of 2026-11-20, as two literal lists. Writing them out
// here, rather than deriving them from the flags, is the point: the test holds
// the flags to the rulings, so a flag flipped by accident fails.
var (
	rulingShareable = []string{
		"address-lists", "apps", "arp", "bandwidth", "bridges", "capsman", "certificates",
		"clock", "connections", "dashboard", "devices", "dhcp", "dhcp-clients",
		"dhcp-servers", "dns", "firewall", "interface-lists", "interfaces", "ip-addresses",
		"ip-pools", "ip-services", "ipsec", "logging", "logs", "netwatch",
		"network-topology", "ntp-client", "openvpn", "ospf", "packages", "ppp",
		"pppoe-clients", "queues", "routing", "routing-rules", "routing-tables",
		"security-scan", "snmp", "vlans", "vpn", "vrrp", "wan", "wifi-clients",
		"wifi-map", "wifi-networks", "wireguard",
	}
	rulingNotShareable = []string{
		"settings", "backups", "audit-trail", "config-management", "users", "reports",
		"scripts", "scheduler", "containers", "files", "terminal", "tools-ping",
		"tools-traceroute", "tools-torch", "tools-btest", "tools-sniffer", "ai-agent",
	}
)

// TestTheShareListsAreTheRulings fails both ways: a page with no decision, a
// page in neither list or in both, a flag that disagrees with its list, and a
// listed key that no longer names a page.
func TestTheShareListsAreTheRulings(t *testing.T) {
	if len(rulingShareable) != 46 || len(rulingNotShareable) != 17 {
		t.Fatalf("the lists hold %d and %d keys; the rulings are 46 and 17",
			len(rulingShareable), len(rulingNotShareable))
	}
	want := map[string]ShareDecision{}
	for _, k := range rulingShareable {
		want[k] = Shareable
	}
	for _, k := range rulingNotShareable {
		if _, dup := want[k]; dup {
			t.Errorf("%s is in both lists", k)
		}
		want[k] = NotShareable
	}
	seen := map[string]bool{}
	for _, p := range All {
		seen[p.Key] = true
		if p.Share == ShareUndecided {
			t.Errorf("%s has no share decision; decide it, and add it to one list here", p.Key)
			continue
		}
		w, listed := want[p.Key]
		if !listed {
			t.Errorf("%s is in neither list", p.Key)
			continue
		}
		if p.Share != w {
			t.Errorf("%s: share decision %d, the ruling says %d", p.Key, p.Share, w)
		}
		if IsShareable(p.Key) != (w == Shareable) {
			t.Errorf("IsShareable(%q) disagrees with its decision", p.Key)
		}
	}
	var gone []string
	for k := range want {
		if !seen[k] {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	for _, k := range gone {
		t.Errorf("%s is listed but is no longer a page", k)
	}
	if len(seen) != len(want) {
		t.Errorf("%d pages, %d decisions in the lists", len(seen), len(want))
	}
	if IsShareable("no-such-page") || IsShareable("") {
		t.Error("an unknown key is shareable")
	}
}
