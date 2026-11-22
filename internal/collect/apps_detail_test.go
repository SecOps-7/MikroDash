package collect

import (
	"testing"

	"mikrodash/internal/apps"
)

// detailWindow is two minutes of two clients: one streaming Netflix, both
// sending traffic nothing names, one to an address the cache has a name for
// and one to an address it has none for.
func detailWindow(t *testing.T) *appsWindow {
	t.Helper()
	names := map[string][]string{"45.57.1.1": {"nflxvideo.net"}, "198.51.100.9": {"unknown.example"}}
	reading := func() *appsSum {
		return SumApps(AppsInput{Names: names, Owner: appsOwner, Cat: apps.Default(), Rates: []ConnRate{
			{Src: "192.168.88.10", Dst: "45.57.1.1", Proto: "tcp", DstPort: "443", RxMbps: 8},
			{Src: "192.168.88.10", Dst: "203.0.113.50", Proto: "udp", DstPort: "9000", RxMbps: 4},
			{Src: "192.168.88.20", Dst: "198.51.100.9", Proto: "tcp", DstPort: "8443", RxMbps: 2, TxMbps: 2},
		}})
	}
	w := newAppsWindow()
	for ts := appsM; ts <= appsM+120_000; ts += 5_000 {
		w.add(reading(), ts, 5000)
	}
	return w
}

// One client's view adds up to that client alone: its app, its unnamed bytes,
// and shares of its own traffic. Every client's views add up to the whole.
func TestOneClientsViewIsThatClientsAlone(t *testing.T) {
	w := detailWindow(t)
	ts := appsM + 120_000
	all := BuildApps(w, ts, nil)
	a := BuildAppsFor(w, ts, nil, "192.168.88.10")
	b := BuildAppsFor(w, ts, nil, "192.168.88.20")
	if a.RxBytes+b.RxBytes != all.RxBytes || a.TxBytes+b.TxBytes != all.TxBytes ||
		a.UnnamedBytes+b.UnnamedBytes != all.UnnamedBytes {
		t.Errorf("the clients do not add up to the whole: %d+%d vs %d, unnamed %d+%d vs %d",
			a.RxBytes, b.RxBytes, all.RxBytes, a.UnnamedBytes, b.UnnamedBytes, all.UnnamedBytes)
	}
	if len(a.Apps) != 1 || a.Apps[0].ID != "netflix" || a.Apps[0].Devices != 1 {
		t.Fatalf("client .10's apps: %+v", a.Apps)
	}
	if want := 100 * float64(a.Apps[0].TotalBytes) / float64(a.RxBytes+a.TxBytes); a.Apps[0].Share < want-0.01 || a.Apps[0].Share > want+0.01 {
		t.Errorf("share %.2f is not of the client's own traffic (%.2f)", a.Apps[0].Share, want)
	}
	if len(b.Apps) != 0 || b.UnnamedBytes == 0 {
		t.Errorf("client .20 used no app and sent unnamed traffic: %+v", b)
	}
	if c := BuildAppsFor(w, ts, nil, "192.168.88.99"); c.RxBytes != 0 || len(c.Apps) != 0 {
		t.Errorf("a client with no traffic: %+v", c)
	}
}

// The unnamed detail lists client, destination, owner and reason, never a
// name; it filters by client and its rows add up to the unnamed total.
func TestTheUnnamedDetail(t *testing.T) {
	w := detailWindow(t)
	ts := appsM + 120_000
	u := BuildUnnamed(w, ts, func(ip string) (string, string) { return "dev-" + ip, "02:00:00:00:00:01" }, "")
	if u.Total != 2 || len(u.Rows) != 2 || u.Partial {
		t.Fatalf("detail %+v", u)
	}
	var sum int64
	for _, r := range u.Rows {
		sum += r.RxBytes + r.TxBytes
		switch r.Dst {
		case "203.0.113.50":
			if r.Reason != UnnamedNoName || r.ASN != 64500 || r.Owner != "Example Net" || r.Client != "192.168.88.10" ||
				r.ClientName != "dev-192.168.88.10" || r.Proto != "udp" || r.Port != "9000" {
				t.Errorf("row %+v", r)
			}
		case "198.51.100.9":
			if r.Reason != UnnamedUnknownName || r.Owner != "" {
				t.Errorf("row %+v", r)
			}
		default:
			t.Errorf("a named destination is in the detail: %+v", r)
		}
	}
	if all := BuildApps(w, ts, nil); sum != all.UnnamedBytes || u.RxBytes+u.TxBytes != all.UnnamedBytes {
		t.Errorf("rows %d, totals %d, page %d", sum, u.RxBytes+u.TxBytes, all.UnnamedBytes)
	}
	one := BuildUnnamed(w, ts, nil, "192.168.88.20")
	if one.Total != 1 || one.Rows[0].Client != "192.168.88.20" {
		t.Errorf("filtered detail %+v", one)
	}

	clients := AppClients(w, nil)
	if len(clients) != 2 || clients[0].IP != "192.168.88.10" || clients[0].Bytes <= clients[1].Bytes {
		t.Errorf("clients %+v", clients)
	}
}

// A minute keeps at most appsUnnamedPerMinute rows; the rest still count in
// the totals, per client too, and the detail says it is partial.
func TestTheUnnamedDetailIsCappedNotLost(t *testing.T) {
	rates := make([]ConnRate, 0, appsUnnamedPerMinute+50)
	for i := 0; i < appsUnnamedPerMinute+50; i++ {
		rates = append(rates, ConnRate{Src: "192.168.88.10", Dst: "198.51.100.1", Proto: "udp",
			DstPort: string(rune('0'+i%10)) + string(rune('0'+i/10%10)) + string(rune('0'+i/100%10)) + string(rune('0'+i/1000%10)), RxMbps: 1})
	}
	w := newAppsWindow()
	in := AppsInput{Rates: rates, Cat: apps.Default()}
	for ts := appsM; ts <= appsM+10_000; ts += 5_000 {
		w.add(SumApps(in), ts, 5000)
	}
	ts := appsM + 10_000
	u := BuildUnnamed(w, ts, nil, "")
	all := BuildApps(w, ts, nil)
	one := BuildAppsFor(w, ts, nil, "192.168.88.10")
	if !u.Partial || u.Total != appsUnnamedPerMinute || len(u.Rows) != appsUnnamedRows {
		t.Errorf("partial %v total %d rows %d", u.Partial, u.Total, len(u.Rows))
	}
	if u.RxBytes != all.UnnamedBytes || one.UnnamedBytes != all.UnnamedBytes {
		t.Errorf("totals lost to the cap: detail %d client %d page %d", u.RxBytes, one.UnnamedBytes, all.UnnamedBytes)
	}
}
