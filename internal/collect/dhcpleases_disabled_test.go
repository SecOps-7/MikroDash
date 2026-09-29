package collect

import (
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
)

// leaseRows replays a lease table through the collector and returns what it
// would emit, so these tests exercise the real parse rather than a hand-built
// payload that agrees with itself.
//
// `fakeReader` is the one in wireless_test.go, reused rather than reinvented.
func leaseRows(t *testing.T, rows ...routeros.Reply) *LeasesPayload {
	t.Helper()
	r := fakeReader{rows: map[string][]routeros.Reply{
		"/ip/dhcp-server/print":       {{"name": "dhcp1", "interface": "bridge"}},
		"/interface/vlan/print":       {},
		"/ip/dhcp-server/lease/print": rows,
	}}
	c := NewDHCPLeases(r, hub.Relay{}, 600000)
	c.RefreshNow()
	p := c.Last()
	if p == nil {
		t.Fatal("the collector emitted nothing")
	}
	return p
}

// TestADisabledLeaseIsCarriedAndNeverResolvesAName — issue #139.
//
// ── THE BUG, AND WHY IT WAS WORSE THAN A STALE LABEL ───────────────────────
//
// Every name lookup returned the FIRST row matching an address, and the payload
// is in the order the router first mentioned each IP. So a disabled reservation
// for 10.0.0.50, seen first, beat the active lease for the same address that
// replaced it — and live traffic was labelled with the name of the device that
// had been swapped out.
//
// ── AND WHY MIKRODASH COULD NOT TELL ───────────────────────────────────────
//
// Measured on a CHR running RouterOS 7.24.4: disabling a lease does NOT change
// its `status` — it stays `waiting` — and `disabled` was not in the collector's
// proplist. There was no information to filter on, which is why the row had to
// gain a field rather than the filter being written against what was there.
//
// This test is named by `addedSinceNode`, because the AX3 capture contains no
// disabled lease: every replayed row carries `false`, so the golden's own rule
// that at least one row must hold a non-empty value cannot be met from the
// corpus. This is the proof that the field is still produced.
func TestADisabledLeaseIsCarriedAndNeverResolvesAName(t *testing.T) {
	p := leaseRows(t,
		routeros.Reply{".id": "*1", "address": "10.0.0.50", "mac-address": "02:00:00:00:00:01",
			"host-name": "old-printer", "status": "waiting", "server": "dhcp1", "disabled": "true"},
		routeros.Reply{".id": "*2", "address": "10.0.0.51", "mac-address": "02:00:00:00:00:02",
			"host-name": "new-printer", "status": "bound", "server": "dhcp1", "disabled": "false"},
	)

	// ── THE ROW IS STILL LISTED ──────────────────────────────────────────────
	//
	// The lease page is where an operator goes to turn a reservation back on, so
	// hiding it there would trade one wrong answer for another. Filtering in the
	// COLLECTOR would have done exactly that.
	var found *Lease
	for i := range p.Leases {
		if p.Leases[i].IP == "10.0.0.50" {
			found = &p.Leases[i]
		}
	}
	if found == nil {
		t.Fatal("the disabled lease is missing from the payload; the lease page could no " +
			"longer show it, or offer to enable it")
	}
	if !found.Disabled {
		t.Error("the lease is not marked disabled, so nothing downstream can filter it")
	}

	// ── AND IT RESOLVES NOTHING ──────────────────────────────────────────────
	if l := LeaseForIP(p, "10.0.0.50"); l != nil {
		t.Errorf("a disabled lease resolved a name for its address: %q", l.Name)
	}
	if l := LeaseForMAC(p, "02:00:00:00:00:01"); l != nil {
		t.Errorf("a disabled lease resolved a name for its MAC: %q", l.Name)
	}

	// ── THE CONTROL ──────────────────────────────────────────────────────────
	//
	// Without it this test passes for a build where the lookups return nil for
	// everything, which would take every hostname off the Connections card.
	l := LeaseForIP(p, "10.0.0.51")
	if l == nil || l.HostName != "new-printer" {
		t.Fatalf("an enabled lease did not resolve: %+v", l)
	}
	if LeaseForMAC(p, "02:00:00:00:00:02") == nil {
		t.Error("an enabled lease did not resolve by MAC")
	}
	// Case-insensitively: RouterOS reports a MAC upper-case here and the ARP
	// table is not guaranteed to agree with it.
	if LeaseForMAC(p, "02:00:00:00:00:02") == nil {
		t.Error("a MAC lookup is case-sensitive")
	}
}

// TestTheActiveLeaseWinsAnAddressWhateverTheOrder.
//
// The reporter's exact scenario, and the one the old code got wrong: an address
// carrying BOTH a disabled reservation and an active lease. Whichever the router
// mentions first, the answer must be the active one.
//
// BOTH ORDERS ARE REPLAYED, because the defect was an ordering one - a test
// using a single order would pass on the broken build half the time, which is
// indistinguishable from passing because the code is right.
func TestTheActiveLeaseWinsAnAddressWhateverTheOrder(t *testing.T) {
	dead := routeros.Reply{".id": "*1", "address": "10.0.0.50",
		"mac-address": "02:00:00:00:00:01", "host-name": "old-printer",
		"status": "waiting", "server": "dhcp1", "disabled": "true"}
	live := routeros.Reply{".id": "*2", "address": "10.0.0.50",
		"mac-address": "02:00:00:00:00:02", "host-name": "new-printer",
		"status": "bound", "server": "dhcp1", "disabled": "false"}

	for _, tc := range []struct {
		what string
		rows []routeros.Reply
	}{
		{"disabled first", []routeros.Reply{dead, live}},
		{"active first", []routeros.Reply{live, dead}},
	} {
		p := leaseRows(t, tc.rows...)
		l := LeaseForIP(p, "10.0.0.50")
		if l == nil {
			t.Errorf("%s: the address resolved to nothing, though an active lease holds it",
				tc.what)
			continue
		}
		if l.HostName != "new-printer" {
			t.Errorf("%s: 10.0.0.50 resolved to %q - the device that was replaced",
				tc.what, l.HostName)
		}
	}
}

// TestAnAddressWithOnlyADisabledLeaseShowsNoName.
//
// The operator's decision on the issue, in one case: "if it does not resolve we
// simply omit a hostname". The connection is still shown by its address - the
// Connections card renders a nameless row on purpose - and what is not shown is
// a name that has stopped being true.
func TestAnAddressWithOnlyADisabledLeaseShowsNoName(t *testing.T) {
	p := leaseRows(t,
		routeros.Reply{".id": "*1", "address": "10.0.0.60", "mac-address": "02:00:00:00:00:09",
			"host-name": "retired-laptop", "status": "waiting", "server": "dhcp1",
			"disabled": "true"},
	)
	if l := LeaseForIP(p, "10.0.0.60"); l != nil {
		t.Errorf("an address with only a disabled lease resolved to %q", l.Name)
	}
	// THE CONTROL: the row IS in the payload, so this is a filter on the lookup
	// rather than the lease having been dropped from the collector.
	if len(p.Leases) != 1 {
		t.Errorf("the payload carries %d lease(s); the lease page needs the row", len(p.Leases))
	}
}
