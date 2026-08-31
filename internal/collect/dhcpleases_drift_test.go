package collect

import (
	"strings"
	"testing"

	"mikrodash/internal/routeros"
)

// The two halves of the live repo's port-drift note §18, both of which this port
// had wrong: a merging re-read, and an allow-list that under-counted.

type fakeLeaseReader struct {
	rows []routeros.Reply
	err  error
	conn bool
	// serverRows answers the /ip/dhcp-server/print the collector makes first.
	serverRows []routeros.Reply
}

func (f *fakeLeaseReader) Connected() bool { return f.conn }
func (f *fakeLeaseReader) Do(cmd routeros.Cmd) ([]routeros.Reply, error) {
	if strings.Contains(cmd.Path, "dhcp-server/lease") {
		return f.rows, f.err
	}
	return f.serverRows, nil
}

func lease(ip, mac, status string) routeros.Reply {
	return routeros.Reply{".id": "*" + ip, "address": ip, "mac-address": mac, "status": status}
}

func newTestLeases(f *fakeLeaseReader) *DHCPLeases {
	return NewDHCPLeases(f, func(string, string, any) {}, 0)
}

// TestAFullReadREPLACESTheTable is the one with a confirmed field failure behind
// it: a CCR2004 whose two /23 pools read 509/509 because every address the pool
// had ever handed out was still in the table.
//
// `/ip/dhcp-server/lease/print` never carries `.dead`, so a merging re-read
// prunes nothing and the table only ever grows.
func TestAFullReadREPLACESTheTable(t *testing.T) {
	f := &fakeLeaseReader{conn: true, rows: []routeros.Reply{
		lease("198.51.100.10", "02:00:00:00:00:01", "bound"),
		lease("198.51.100.11", "02:00:00:00:00:02", "bound"),
		lease("198.51.100.12", "02:00:00:00:00:03", "bound"),
	}}
	d := newTestLeases(f)
	d.RefreshNow()
	if got := len(d.LeaseIPs()); got != 3 {
		t.Fatalf("first read produced %d leases, want 3", got)
	}

	// The router now reports only one. The other two are gone — and gone with no
	// `.dead` marker, which is exactly how a poll-mode removal arrives.
	f.rows = []routeros.Reply{lease("198.51.100.10", "02:00:00:00:00:01", "bound")}
	d.RefreshNow()
	ips := d.LeaseIPs()
	if len(ips) != 1 {
		t.Fatalf("after a shorter read the table holds %d leases (%v) — it MERGED, so a lease "+
			"that vanished during a disconnect stays for good and the table only ever grows", len(ips), ips)
	}
	if ips[0] != "198.51.100.10" {
		t.Errorf("the surviving lease is %q", ips[0])
	}
	// The payload the page renders must agree.
	if last := d.Last(); last == nil || len(last.Leases) != 1 {
		t.Errorf("the payload still lists the phantoms: %+v", last)
	}

	// EVERY map, not just the one the counts read.
	//
	// `byMAC` is mac→ip "for the name lookups other pages make" and has no
	// consumer yet, so a stale entry is invisible today and misattributing the
	// moment one arrives — a departed device's MAC still resolving to the address
	// it used to hold. Asserted directly because this test is in-package; a
	// mutation clearing only `byIP` survived everything observable from outside.
	d.mu.Lock()
	nMAC, nIP := len(d.byMAC), len(d.byIP)
	d.mu.Unlock()
	if nMAC != 1 || nIP != 1 {
		t.Errorf("after the shorter read: byIP has %d, byMAC has %d — both must be replaced", nIP, nMAC)
	}
}

// TestAFailedReadLeavesTheTableStanding — the maps are cleared AFTER the read
// returns, never before, or a transient failure blanks the table and every name
// lookup hanging off it.
func TestAFailedReadLeavesTheTableStanding(t *testing.T) {
	f := &fakeLeaseReader{conn: true, rows: []routeros.Reply{
		lease("198.51.100.10", "02:00:00:00:00:01", "bound"),
		lease("198.51.100.11", "02:00:00:00:00:02", "bound"),
	}}
	d := newTestLeases(f)
	d.RefreshNow()
	if len(d.LeaseIPs()) != 2 {
		t.Fatal("precondition: two leases should have loaded")
	}
	f.err = errString("connection reset")
	d.RefreshNow()
	if got := len(d.LeaseIPs()); got != 2 {
		t.Errorf("a failed read left %d leases; it must leave the previous table standing", got)
	}
}

// TestUsedLeaseIPsIsADenyList. An address is in use unless its status is
// `waiting`. The inverse matters as much as the rule: a filter that dropped
// transient states would under-count, which is the shape the live repo's notes
// explicitly warn against.
func TestUsedLeaseIPsIsADenyList(t *testing.T) {
	// Every status RouterOS documents, plus the empty one.
	rows := []routeros.Reply{
		lease("198.51.100.1", "02:00:00:00:00:01", "bound"),
		lease("198.51.100.2", "02:00:00:00:00:02", "offered"),
		lease("198.51.100.3", "02:00:00:00:00:03", "testing"),
		lease("198.51.100.4", "02:00:00:00:00:04", "authorizing"),
		lease("198.51.100.5", "02:00:00:00:00:05", "declined"),
		lease("198.51.100.6", "02:00:00:00:00:06", "conflict"),
		lease("198.51.100.7", "02:00:00:00:00:07", ""),
		// A status RouterOS does not document today. A deny-list over-counts by
		// one; an allow-list would drop it silently.
		lease("198.51.100.8", "02:00:00:00:00:08", "some-future-state"),
		lease("198.51.100.9", "02:00:00:00:00:09", "waiting"),
		lease("198.51.100.20", "02:00:00:00:00:0a", "WAITING"),
	}
	d := newTestLeases(&fakeLeaseReader{conn: true, rows: rows})
	d.RefreshNow()

	used := d.UsedLeaseIPs()
	if len(used) != 8 {
		t.Errorf("UsedLeaseIPs returned %d of 10 (%v); only the two `waiting` rows are free", len(used), used)
	}
	for _, ip := range used {
		if ip == "198.51.100.9" || ip == "198.51.100.20" {
			t.Errorf("%s is `waiting` — a reservation nobody holds — and was counted as used", ip)
		}
	}
	// THE TABLE STILL LISTS EVERYTHING. A page that hides reservations is worse
	// than one that miscounts them.
	if got := len(d.LeaseIPs()); got != len(rows) {
		t.Errorf("the lease table lists %d of %d rows — only the arithmetic may filter", got, len(rows))
	}
	if last := d.Last(); last == nil || len(last.Leases) != len(rows) {
		t.Errorf("the rendered payload dropped rows: %d of %d", len(last.Leases), len(rows))
	}
}

// TestCaseIsIgnoredOnTheStatus — RouterOS spells statuses lower case, but the
// comparison must not depend on that: a build that shouted would turn every
// reservation into a used address overnight.
func TestCaseIsIgnoredOnTheStatus(t *testing.T) {
	for _, s := range []string{"waiting", "Waiting", "WAITING", "WaItInG"} {
		d := newTestLeases(&fakeLeaseReader{conn: true, rows: []routeros.Reply{
			lease("198.51.100.1", "02:00:00:00:00:01", s),
		}})
		d.RefreshNow()
		if got := len(d.UsedLeaseIPs()); got != 0 {
			t.Errorf("status %q counted as used", s)
		}
	}
}
