package collect

import (
	"strconv"
	"testing"

	"mikrodash/internal/apps"
	"mikrodash/internal/routeros"
)

// TestDNSNamesFollowCNAMEsBack: the cache rows as RouterOS 7.25.1 returns them
// (measured on CHR Test), where the address belongs to the CNAME's target and
// the name a device asked for is one hop back.
func TestDNSNamesFollowCNAMEsBack(t *testing.T) {
	rows := []routeros.Reply{
		{"name": "apps.mikrotik.com", "type": "CNAME", "data": "global-balancer-e.mikrotik.com."},
		{"name": "global-balancer-e.mikrotik.com", "type": "A", "data": "159.148.147.251"},
		{"name": "global-balancer-e.mikrotik.com", "type": "AAAA", "data": "2a02:610:7501:3000::251"},
		{"name": "Teams.Microsoft.com", "type": "CNAME", "data": "teams-ring.msedge.net."},
		{"name": "teams-ring.msedge.net", "type": "CNAME", "data": "s-0005.s-msedge.net."},
		{"name": "s-0005.s-msedge.net", "type": "A", "data": "52.113.194.132"},
		{"name": "junk", "type": "TXT", "data": "v=spf1"},
	}
	got := DNSNames(rows)
	has := func(ip, name string) bool {
		for _, n := range got[ip] {
			if n == name {
				return true
			}
		}
		return false
	}
	for _, c := range []struct{ ip, name string }{
		{"159.148.147.251", "apps.mikrotik.com"},
		{"159.148.147.251", "global-balancer-e.mikrotik.com"},
		{"2a02:610:7501:3000::251", "apps.mikrotik.com"},
		{"52.113.194.132", "teams.microsoft.com"},
	} {
		if !has(c.ip, c.name) {
			t.Errorf("%s does not carry %s: %v", c.ip, c.name, got[c.ip])
		}
	}
	if m := apps.Default().Classify(got["52.113.194.132"], 8075, "tcp", 443); m.App == nil || m.App.ID != "teams" {
		t.Errorf("an address reached through two CNAMEs was not Teams: %+v", m)
	}
}

// TestAppsSumByAppAndKeepQuietOnesForAWhile drives BuildApps with rates as
// connRates produces them.
func TestAppsSumByAppAndKeepQuietOnesForAWhile(t *testing.T) {
	names := map[string][]string{"52.113.194.132": {"teams.microsoft.com"}, "45.57.1.1": {"nflxvideo.net"}}
	owner := func(ip string) (uint, string, bool) {
		if ip == "203.0.113.50" {
			return 64500, "Example Net", true
		}
		return 0, "", false
	}
	rates := []ConnRate{
		{Src: "192.168.88.10", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 30, TxMbps: 1},
		{Src: "192.168.88.11", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 10, TxMbps: 1},
		{Src: "192.168.88.10", Dst: "52.113.194.132", Proto: "udp", DstPort: "3478", RxMbps: 2, TxMbps: 2},
		{Src: "192.168.88.12", Dst: "203.0.113.50", Proto: "tcp", DstPort: "443", RxMbps: 4, TxMbps: 0},
		// LAN to LAN is not app traffic, and a non-LAN source is not ours.
		{Src: "192.168.88.10", Dst: "192.168.88.1", Proto: "tcp", DstPort: "80", RxMbps: 99, TxMbps: 99},
		{Src: "8.8.8.8", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 99, TxMbps: 99},
	}
	spark := map[string][]float64{}
	in := AppsInput{Rates: rates, Now: 1000, Names: names, Owner: owner, Cat: apps.Default(), PollMs: 5000,
		NameOf: func(ip string) (string, string) {
			if ip == "192.168.88.10" {
				return "living-room-tv", "02:00:00:00:00:10"
			}
			return "", ""
		}}
	p := BuildApps(spark, in)
	if p.RxMbps != 46 || p.TxMbps != 4 || p.UnnamedMbps != 4 {
		t.Fatalf("totals rx=%v tx=%v unnamed=%v, want 46, 4, 4", p.RxMbps, p.TxMbps, p.UnnamedMbps)
	}
	if len(p.Apps) != 2 || p.Apps[0].ID != "netflix" || p.Apps[1].ID != "teams" {
		t.Fatalf("apps = %+v, want netflix then teams", p.Apps)
	}
	nf := p.Apps[0]
	if nf.RxMbps != 40 || nf.TxMbps != 2 || nf.Devices != 2 || nf.Via != apps.ViaName || nf.Top != "living-room-tv" {
		t.Errorf("netflix = %+v", nf)
	}
	if nf.Share != 84 {
		t.Errorf("netflix share %v, want 84 (42 of 50)", nf.Share)
	}
	if d := p.Devices["netflix"]; len(d) != 2 || d[0].IP != "192.168.88.10" || d[0].MAC != "02:00:00:00:00:10" {
		t.Errorf("netflix devices = %+v", d)
	}
	if len(p.Unnamed) != 1 || p.Unnamed[0].Org != "Example Net" || p.Unnamed[0].RxMbps != 4 {
		t.Errorf("unnamed = %+v", p.Unnamed)
	}

	// Quiet readings: Netflix stays listed with a falling sparkline, at zero.
	in.Rates = nil
	for i := 0; i < appsSparkLen-1; i++ {
		p = BuildApps(spark, in)
	}
	if len(p.Apps) != 2 || p.Apps[0].TotalMbps != 0 || len(p.Apps[0].Spark) != appsSparkLen {
		t.Fatalf("after %d quiet readings: %+v", appsSparkLen-1, p.Apps)
	}
	// One more and the busy reading has scrolled out: both leave.
	p = BuildApps(spark, in)
	if len(p.Apps) != 0 || len(spark) != 0 {
		t.Errorf("after %d quiet readings the apps are still listed: %+v", appsSparkLen, p.Apps)
	}
}

// TestDNSNamesIsBoundedAgainstAFloodedCache: a device controls what its own
// domain puts in the router's cache. Ten thousand CNAMEs for one target, which
// has three address records, must not cost ten thousand names per address.
func TestDNSNamesIsBoundedAgainstAFloodedCache(t *testing.T) {
	var rows []routeros.Reply
	for i := 0; i < 10000; i++ {
		rows = append(rows, routeros.Reply{"name": "n" + strconv.Itoa(i) + ".example", "type": "CNAME", "data": "target.example."})
	}
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3"} {
		rows = append(rows, routeros.Reply{"name": "target.example", "type": "A", "data": ip},
			routeros.Reply{"name": "target.example", "type": "A", "data": ip})
	}
	// Below the cap, the same address recorded twice still carries each name once.
	small := DNSNames([]routeros.Reply{
		{"name": "a.example", "type": "CNAME", "data": "b.example."},
		{"name": "b.example", "type": "A", "data": "203.0.113.7"},
		{"name": "b.example", "type": "A", "data": "203.0.113.7"},
	})
	if n := small["203.0.113.7"]; len(n) != 2 {
		t.Errorf("one address recorded twice carries %v, want b.example and a.example once each", n)
	}

	got := DNSNames(rows)
	for ip, names := range got {
		if len(names) > dnsNamesPerAddr {
			t.Errorf("%s carries %d names, cap %d", ip, len(names), dnsNamesPerAddr)
		}
		seen := map[string]bool{}
		for _, n := range names {
			if seen[n] {
				t.Errorf("%s carries %s twice", ip, n)
			}
			seen[n] = true
		}
		if !seen["target.example"] {
			t.Errorf("%s lost the name of its own record", ip)
		}
	}
}
