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

// appsNames and appsOwner are the outside world the window tests classify
// against: Netflix and Teams by DNS name, one unnamed network.
var appsNames = map[string][]string{"52.113.194.132": {"teams.microsoft.com"}, "45.57.1.1": {"nflxvideo.net"}}

func appsOwner(ip string) (uint, string, bool) {
	if ip == "203.0.113.50" {
		return 64500, "Example Net", true
	}
	return 0, "", false
}

func appsSumOf(rates ...ConnRate) *appsSum {
	return SumApps(AppsInput{Rates: rates, Names: appsNames, Owner: appsOwner, Cat: apps.Default()})
}

// netflixAt is one LAN device streaming Netflix at rx Mbps.
func netflixAt(src string, rx float64) ConnRate {
	return ConnRate{Src: src, Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: rx}
}

// appsM is a minute boundary: 1.8e12 ms is a whole number of minutes.
const appsM = int64(1_800_000_000_000)

// TestSumAppsSumsOneReadingByApp drives SumApps with rates as connRates
// produces them.
func TestSumAppsSumsOneReadingByApp(t *testing.T) {
	s := appsSumOf(
		ConnRate{Src: "192.168.88.10", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 30, TxMbps: 1},
		ConnRate{Src: "192.168.88.11", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 10, TxMbps: 1},
		ConnRate{Src: "192.168.88.10", Dst: "52.113.194.132", Proto: "udp", DstPort: "3478", RxMbps: 2, TxMbps: 2},
		ConnRate{Src: "192.168.88.12", Dst: "203.0.113.50", Proto: "tcp", DstPort: "443", RxMbps: 4, TxMbps: 0},
		// LAN to LAN is not app traffic, and a non-LAN source is not ours.
		ConnRate{Src: "192.168.88.10", Dst: "192.168.88.1", Proto: "tcp", DstPort: "80", RxMbps: 99, TxMbps: 99},
		ConnRate{Src: "8.8.8.8", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 99, TxMbps: 99},
	)
	if s.all.rx != 46 || s.all.tx != 4 || s.unnamed != 4 {
		t.Fatalf("totals rx=%v tx=%v unnamed=%v, want 46, 4, 4", s.all.rx, s.all.tx, s.unnamed)
	}
	nf := s.apps["netflix"]
	if len(s.apps) != 2 || nf == nil || s.apps["teams"] == nil {
		t.Fatalf("apps = %v, want netflix and teams", s.apps)
	}
	if nf.rx != 40 || nf.tx != 2 || len(nf.devices) != 2 || nf.devices["192.168.88.10"].rx != 30 {
		t.Errorf("netflix = %+v", nf)
	}
}

// TestTheWindowRollsOverByTheMinute. A reading's rate is held for the time
// since the previous one and becomes bytes; a reading that spans a minute
// boundary is split between the two minutes by time; a minute closes, and the
// window is sent, only when a reading falls in a later minute. The first
// reading carrying time is sent once, as the hour beginning to fill.
func TestTheWindowRollsOverByTheMinute(t *testing.T) {
	w := newAppsWindow()
	r := appsSumOf(netflixAt("192.168.88.10", 8)) // 8 Mbps is 1,000,000 bytes a second

	if closed, end := w.add(r, appsM+50_000, 5000); len(closed) != 0 || end != 0 {
		t.Fatalf("the first reading, which holds no time, closed %v and sent a window ending %d", closed, end)
	}
	closed, end := w.add(r, appsM+55_000, 5000)
	if len(closed) != 0 || end != appsM+appsMinuteMs {
		t.Fatalf("the first reading with bytes closed %v and sent %d, want the open minute (%d) shown once", closed, end, appsM+appsMinuteMs)
	}
	p := BuildApps(w, end, appsM+55_000, nil)
	if p.RxBytes != 5_000_000 || len(p.Apps) != 1 || p.Apps[0].RxBytes != 5_000_000 {
		t.Fatalf("the filling minute shows %+v, want 5 s of 8 Mbps, 5,000,000 bytes", p)
	}
	if p.From != appsM+50_000 || p.To != appsM+55_000 {
		t.Errorf("the filling window covers %d..%d, want the first reading to the latest", p.From, p.To)
	}
	if _, end := w.add(r, appsM+58_000, 5000); end != 0 {
		t.Errorf("a second reading in the same minute sent the window again (end %d): it moves once a minute", end)
	}

	// 58 s to 62 s: 2 s in the closing minute, 2 s in the next.
	closed, end = w.add(r, appsM+62_000, 5000)
	if end != appsM+appsMinuteMs || len(closed) != 1 || closed[0].Start != appsM {
		t.Fatalf("the reading in the next minute closed %v and sent %d, want minute %d closed and sent", closed, end, appsM)
	}
	if got := closed[0].Apps["netflix"].Rx; got != 10_000_000 {
		t.Errorf("the closed minute holds %v bytes, want 10,000,000 (5 + 3 + the 2 s before the boundary)", got)
	}
	if got := w.minutes[appsM+appsMinuteMs].apps["netflix"].rx; got != 2_000_000 {
		t.Errorf("the open minute holds %v bytes of the reading that spanned the boundary, want 2,000,000", got)
	}
	p = BuildApps(w, end, appsM+62_000, nil)
	if p.RxBytes != 10_000_000 || p.To != appsM+appsMinuteMs || p.From != appsM+50_000 {
		t.Errorf("the window after the first minute closed: %d bytes, %d..%d", p.RxBytes, p.From, p.To)
	}
	if sp := p.Apps[0].Spark; len(sp) != appsWindowMinutes || sp[appsWindowMinutes-1] != 10_000_000 || sp[0] != 0 {
		t.Errorf("the sparkline is %v, want %d points ending in the closed minute's bytes", sp, appsWindowMinutes)
	}
}

// TestTheWindowIsTheLastHourAndMovesOncePerMinute. Readings every five
// seconds for 62 minutes: the window is sent once per closed minute (and once
// while the first filled), never more, and the busy first minute leaves it
// when it is more than an hour old.
func TestTheWindowIsTheLastHourAndMovesOncePerMinute(t *testing.T) {
	w := newAppsWindow()
	busy := appsSumOf(netflixAt("192.168.88.10", 16))
	steady := appsSumOf(netflixAt("192.168.88.10", 8))
	sent, closedN := 0, 0
	var end int64
	for ts := appsM; ts <= appsM+62*appsMinuteMs; ts += 5000 {
		r := steady
		if ts <= appsM+appsMinuteMs {
			r = busy // the first minute, and the reading that closes it
		}
		closed, e := w.add(r, ts, 5000)
		closedN += len(closed)
		if e != 0 {
			sent++
			end = e
		}
		if e == appsM+60*appsMinuteMs {
			p := BuildApps(w, e, ts, nil)
			if p.RxBytes != 120_000_000+59*60_000_000 {
				t.Errorf("the first full hour holds %d bytes, want the busy minute and 59 steady ones", p.RxBytes)
			}
			if p.From != appsM {
				t.Errorf("a full hour reads as starting at %d, want %d", p.From, appsM)
			}
		}
	}
	if closedN != 62 || sent != 63 {
		t.Errorf("62 minutes closed %d minutes and sent the window %d times, want 62 and 63 (each close, and the first filling)", closedN, sent)
	}
	p := BuildApps(w, end, end, nil)
	if p.RxBytes != 60*60_000_000 {
		t.Errorf("the last hour holds %d bytes, want sixty steady minutes: the busy first minute did not expire", p.RxBytes)
	}
	if _, ok := w.minutes[appsM]; ok {
		t.Error("a minute more than an hour old is still held")
	}
	if len(w.minutes) > appsWindowMinutes+1 {
		t.Errorf("%d minutes held, want at most the hour and the open one", len(w.minutes))
	}
}

// TestTheWindowCapsAGap. A reading after a gap (a suspended collector, a
// stalled router) is held for at most two polls: ten minutes at 8 Mbps is
// not 600 MB, it is 10 s of it. And the hour starts filling again from there.
func TestTheWindowCapsAGap(t *testing.T) {
	w := newAppsWindow()
	r := appsSumOf(netflixAt("192.168.88.10", 8))
	w.add(r, appsM+1_000, 5000)
	w.add(r, appsM+6_000, 5000)
	closed, end := w.add(r, appsM+10*appsMinuteMs+30_000, 5000)
	var total float64
	for _, m := range closed {
		total += m.Apps["netflix"].Rx
	}
	if total != 5_000_000 {
		t.Errorf("the minutes closed by the gap hold %v bytes, want only the 5 s before it", total)
	}
	if got := w.minutes[appsM+10*appsMinuteMs].apps["netflix"].rx; got != 10_000_000 {
		t.Errorf("the reading after a 10-minute gap added %v bytes, want two polls' worth, 10,000,000", got)
	}
	if p := BuildApps(w, end, end, nil); p.From > p.To {
		t.Errorf("the window the gap closed reads as %d..%d, From after To", p.From, p.To)
	}
	for ts := appsM + 10*appsMinuteMs + 35_000; ts <= appsM+11*appsMinuteMs; ts += 5000 {
		_, end = w.add(r, ts, 5000)
	}
	if p := BuildApps(w, end, end, nil); p.From != appsM+10*appsMinuteMs+20_000 || p.To != appsM+11*appsMinuteMs {
		t.Errorf("after a gap the hour reads as %d..%d, want filling from the start of what the reading was held for", p.From, p.To)
	}
	for _, c := range []struct {
		name     string
		prev, ts int64
		pollMs   int
		want     int64
	}{
		{"the first reading", 0, 15_000, 5000, 0},
		{"a clock that went backwards", 20_000, 15_000, 5000, 0},
		{"exactly two polls is not cut", 10_000, 20_000, 5000, 10_000},
		{"no poll period reads as the 5 s default", 10_000, 110_000, 0, 10_000},
	} {
		if got := appsHeldMs(c.prev, c.ts, c.pollMs); got != c.want {
			t.Errorf("%s: held for %d ms, want %d", c.name, got, c.want)
		}
	}
	if got := mbpsToBytes(8, 5); got != 5_000_000 {
		t.Errorf("8 Mbps for 5 s is %v bytes, want 5,000,000", got)
	}
}

// TestBuildAppsSumsTheHourByApp. Per app: bytes each way, share of all
// traffic, the devices that used it over the hour (not just the last minute),
// the busiest one, how it was named; and the unnamed total.
func TestBuildAppsSumsTheHourByApp(t *testing.T) {
	w := newAppsWindow()
	first := appsSumOf(
		ConnRate{Src: "192.168.88.10", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 30, TxMbps: 1},
		ConnRate{Src: "192.168.88.10", Dst: "52.113.194.132", Proto: "udp", DstPort: "3478", RxMbps: 2, TxMbps: 2},
		ConnRate{Src: "192.168.88.12", Dst: "203.0.113.50", Proto: "tcp", DstPort: "443", RxMbps: 4, TxMbps: 0},
	)
	second := appsSumOf(ConnRate{Src: "192.168.88.11", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 10, TxMbps: 1})
	// Each reading's rate covers the time before it.
	w.add(first, appsM+40_000, 10_000)                    // holds nothing
	w.add(first, appsM+50_000, 10_000)                    // 10 s of the first, minute 0
	w.add(first, appsM+60_000, 10_000)                    // 10 more; closes minute 0
	w.add(second, appsM+70_000, 10_000)                   // 10 s of the second, minute 1
	_, end := w.add(second, appsM+2*appsMinuteMs, 10_000) // 50 s later: held two polls, 20 s; closes minute 1
	p := BuildApps(w, end, end, func(ip string) (string, string) {
		if ip == "192.168.88.10" {
			return "living-room-tv", "02:00:00:00:00:10"
		}
		return "", ""
	})
	// minute 0: netflix 30/1 Mbps x 20 s, teams 2/2 x 20 s, unnamed 4 x 20 s.
	// minute 1: netflix 10/1 x 30 s.
	mb := func(mbps, s float64) int64 { return int64(mbpsToBytes(mbps, s)) }
	if len(p.Apps) != 2 || p.Apps[0].ID != "netflix" || p.Apps[1].ID != "teams" {
		t.Fatalf("apps = %+v, want netflix then teams", p.Apps)
	}
	nf := p.Apps[0]
	if nf.RxBytes != mb(30, 20)+mb(10, 30) || nf.TxBytes != mb(1, 20)+mb(1, 30) {
		t.Errorf("netflix moved %d/%d bytes, want %d/%d", nf.RxBytes, nf.TxBytes, mb(30, 20)+mb(10, 30), mb(1, 20)+mb(1, 30))
	}
	if nf.Devices != 2 || nf.Top != "living-room-tv" || nf.Via != apps.ViaName {
		t.Errorf("netflix = %+v, want two devices over the hour, the TV the busiest, named by DNS", nf)
	}
	if d := p.Devices["netflix"]; len(d) != 2 || d[0].IP != "192.168.88.10" || d[0].MAC != "02:00:00:00:00:10" || d[0].RxBytes != mb(30, 20) {
		t.Errorf("netflix devices = %+v", d)
	}
	if p.UnnamedBytes != mb(4, 20) {
		t.Errorf("unnamed %d bytes, want %d", p.UnnamedBytes, mb(4, 20))
	}
	all := float64(p.RxBytes + p.TxBytes)
	if want := round4(100 * float64(nf.TotalBytes) / all); nf.Share != want || nf.TotalBytes != nf.RxBytes+nf.TxBytes {
		t.Errorf("netflix share %v of %v bytes, want %v", nf.Share, all, want)
	}
	if nf.Spark[appsWindowMinutes-2] != mb(31, 20) || nf.Spark[appsWindowMinutes-1] != mb(11, 30) {
		t.Errorf("netflix's last two minutes are %v, want %d and %d", nf.Spark[appsWindowMinutes-2:], mb(31, 20), mb(11, 30))
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
