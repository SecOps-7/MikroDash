package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mikrodash/internal/routeros"
)

var connListLAN = []string{"192.0.2.0/24"}

func connRow(id, src, dst, proto, state string, orig, repl string) routeros.Reply {
	r := routeros.Reply{".id": id, "src-address": src, "dst-address": dst, "protocol": proto,
		"orig-bytes": orig, "repl-bytes": repl}
	if state != "" {
		r["tcp-state"] = state
	}
	return r
}

// One row per connection, each field where it belongs: the client named only
// for a LAN source, the country and org only for an outside destination, the
// TCP state only for TCP, the destination port from its own property.
func TestAConnectionListRowIsTheConnection(t *testing.T) {
	rows := []routeros.Reply{
		connRow("*1", "192.0.2.10", "198.51.100.7", "TCP", "established", "1000", "9000"),
		connRow("*2", "198.51.100.9", "192.0.2.10", "tcp", "syn-received", "10", "0"),
		connRow("*3", "2001:db8::5", "2001:db8::fb", "udp", "", "5", "5"),
	}
	rows[0]["dst-port"] = "443"
	rows[1]["dst-port"] = "22"
	named := 0
	list, _ := BuildConnList(ConnListInput{Rows: rows, LanCidrs: connListLAN,
		NameOf: func(ip string) (string, string) { named++; return "laptop", "" },
		Geo:    func(ip string) (string, string) { return "DE", "" },
		Org:    func(ip string) (string, string, bool) { return "Example Net", "cdn", true }})
	if list.Total != 3 || len(list.Rows) != 3 || list.Capped {
		t.Fatalf("%+v", list)
	}
	a, b, c := list.Rows[0], list.Rows[1], list.Rows[2]
	if a.Src != "192.0.2.10" || a.Dst != "198.51.100.7" || a.DstPort != "443" ||
		a.Proto != "tcp" || a.State != "established" || !a.Local || a.Client != "laptop" ||
		a.Country != "DE" || a.Org != "Example Net" || a.Tx != 1000 || a.Rx != 9000 {
		t.Errorf("outbound row: %+v", a)
	}
	if a.DstLocal || !b.DstLocal {
		t.Errorf("destination on the LAN: outbound %v, inbound %v", a.DstLocal, b.DstLocal)
	}
	if b.Local || b.Client != "" || b.Country != "" || b.DstPort != "22" || b.State != "syn-received" {
		t.Errorf("an inbound connection to the LAN was named or placed: %+v", b)
	}
	if c.Src != "2001:db8::5" || c.Dst != "2001:db8::fb" || c.DstPort != "" || c.State != "" {
		t.Errorf("an IPv6 row, or a state on UDP: %+v", c)
	}
	if named != 1 {
		t.Errorf("the client was named %d times; once per address is enough", named)
	}
	if a.TxRate != nil || a.RxRate != nil {
		t.Error("a first reading has rates, with nothing to difference against")
	}
}

// Rates are the change per second since the connection's last reading; a
// counter that went backwards (a reused id) is not a rate.
func TestConnectionRatesAreThePerSecondChange(t *testing.T) {
	first := []routeros.Reply{connRow("*1", "192.0.2.10:1", "198.51.100.7:443", "tcp", "established", "1000", "5000")}
	_, prev := BuildConnList(ConnListInput{Rows: first, LanCidrs: connListLAN})
	second := []routeros.Reply{
		connRow("*1", "192.0.2.10:1", "198.51.100.7:443", "tcp", "established", "3000", "25000"),
		connRow("*2", "192.0.2.11:2", "198.51.100.8:443", "tcp", "established", "10", "10"),
	}
	list, next := BuildConnList(ConnListInput{Rows: second, LanCidrs: connListLAN, Prev: prev, Elapsed: 2 * time.Second})
	r := list.Rows[0]
	if r.TxRate == nil || r.RxRate == nil || *r.TxRate != 1000 || *r.RxRate != 10000 {
		t.Fatalf("rates %v %v, want 1000 and 10000 bytes a second", r.TxRate, r.RxRate)
	}
	if list.Rows[1].TxRate != nil {
		t.Error("a connection new in this reading has a rate")
	}
	if next["*2"] != [2]int64{10, 10} {
		t.Error("the next reading will not difference the new connection")
	}
	reused := []routeros.Reply{connRow("*1", "192.0.2.10:1", "198.51.100.7:443", "tcp", "established", "5", "5")}
	again, _ := BuildConnList(ConnListInput{Rows: reused, LanCidrs: connListLAN, Prev: next, Elapsed: time.Second})
	if again.Rows[0].TxRate != nil {
		t.Error("a counter that went backwards produced a rate")
	}
}

// Past the processing cap the list says it is not the whole table.
func TestTheConnectionListSaysWhenItIsCapped(t *testing.T) {
	rows := []routeros.Reply{connRow("*1", "192.0.2.1:1", "198.51.100.1:1", "udp", "", "1", "1"),
		connRow("*2", "192.0.2.1:2", "198.51.100.1:2", "udp", "", "1", "1")}
	list, _ := BuildConnList(ConnListInput{Rows: rows, MaxConns: 1})
	if !list.Capped || list.Total != 2 || len(list.Rows) != 1 {
		t.Errorf("%+v", list)
	}
}

// A real capture: a row for every connection the router listed.
func TestTheConnectionListOfARealTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(testdata, "fixtures", "Mikrotik identity-0cc5 AX3", "conns.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	rows, _ := newReplayReader(f).Do(connsCmd)
	if len(rows) < 100 {
		t.Fatalf("the fixture replayed %d rows; this reads nothing", len(rows))
	}
	list, _ := BuildConnList(ConnListInput{Rows: rows, MaxConns: connsMaxRows})
	if len(list.Rows) != len(rows) || list.Total != len(rows) {
		t.Errorf("%d rows for %d connections", len(list.Rows), len(rows))
	}
	for _, r := range list.Rows {
		if r.ID == "" || r.Src == "" || r.Proto == "" {
			t.Fatalf("a row lost its identity: %+v", r)
		}
	}
}
