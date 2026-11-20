package pages

import (
	"sort"
	"testing"
)

// The operator's rulings of 2026-11-20, as three literal lists. Writing them
// out here, rather than deriving them from the flags, is the point: the test
// holds the flags to the rulings, so a flag flipped by accident fails.
//
// He first ruled every page shareable but administration and tools, then
// narrowed it to the six pages first proposed, then added every page a
// Dashboard card draws from, then took DNS out and added Wi-Fi Networks and
// Apps Usage. The developer decides this, in code; nothing in
// the app lets a user change it. Sharing another page is moving its key from rulingNotYet to
// rulingShareable and flipping its flag. A page in rulingNever never moves.
var (
	rulingShareable = []string{
		"apps", "bandwidth", "connections", "dashboard", "devices", "dhcp", "firewall",
		"interfaces", "logs", "netwatch", "network-topology", "routing", "security-scan",
		"vpn", "wan", "wifi-clients", "wifi-networks",
	}
	// Not chosen yet: shareable when the operator says so.
	rulingNotYet = []string{
		"address-lists", "arp", "bridges", "capsman", "certificates",
		"clock", "dhcp-clients", "dhcp-servers", "dns",
		"interface-lists", "ip-addresses", "ip-pools", "ip-services", "ipsec", "logging",
		"ntp-client", "openvpn", "ospf", "packages", "ppp",
		"pppoe-clients", "queues", "routing-rules", "routing-tables",
		"snmp", "vlans", "vrrp", "wifi-map", "wireguard",
	}
	// Never: administration, credentials, tools that act on the router.
	rulingNever = []string{
		"settings", "backups", "audit-trail", "config-management", "users", "reports",
		"scripts", "scheduler", "containers", "files", "terminal", "tools-ping",
		"tools-traceroute", "tools-torch", "tools-btest", "tools-sniffer", "ai-agent",
	}
	rulingNotShareable = append(append([]string{}, rulingNotYet...), rulingNever...)
)

// TestTheShareListsAreTheRulings fails both ways: a page with no decision, a
// page in neither list or in both, a flag that disagrees with its list, and a
// listed key that no longer names a page.
func TestTheShareListsAreTheRulings(t *testing.T) {
	if len(rulingShareable) != 17 || len(rulingNotYet) != 29 || len(rulingNever) != 17 {
		t.Fatalf("the lists hold %d, %d and %d keys; the rulings are 17, 29 and 17",
			len(rulingShareable), len(rulingNotYet), len(rulingNever))
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
