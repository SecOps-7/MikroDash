package session

import (
	"strings"
	"testing"
)

// ── LEASES MUST START BEFORE NETWORKS, ON EVERY PATH ───────────────────────
//
// `dhcpNetworks.Tick` counts each subnet's used addresses by calling
// `dhcpLeases.UsedLeaseIPs()`. Run the other way round, that returns nil and
// every subnet gets a lease count of zero -- silently, because an empty lease
// set is not an error. Both collectors poll every 600s and a page focus replays
// `Last()`, so the zero survives for ten minutes: the DHCP page shows its subnet
// rows and a full lease table with every count reading 0%.
//
// WHY A SOURCE READ. The property is the ORDER of two statements, and nothing
// observable distinguishes the two orderings on a session whose leases happen to
// have loaded already -- which is why this survived a live check that showed
// correct percentages. It is the same shape as `release_test.go`'s ledgers:
// measure the source, and name the difference.
//
// BOTH PATHS, because they disagreed. The reconnect block already had leases
// first; the connect block did not, and that asymmetry WAS the bug -- a dropped
// connection corrected the page, which is what identified it. A test naming one
// path would not have seen it, which is the lesson `release_test.go` records
// about Release and Shutdown, hit again here.
func TestLeasesStartBeforeNetworksOnEveryPath(t *testing.T) {
	src := sessionSource(t)

	for _, c := range []struct{ what, start, end, lease, net string }{
		{
			what: "connect", start: "if first {", end: "\n\t\t}",
			lease: "s.dhcpLeases.Start()", net: "s.dhcpNetworks.Start()",
		},
		{
			what: "reconnect", start: "if s.dormancy != nil {", end: "\n\t\t}",
			lease: "s.dhcpLeases.Reconnected()", net: "s.dhcpNetworks.Reconnected()",
		},
	} {
		body := blockBetween(t, src, c.start, c.end)
		li, ni := strings.Index(body, c.lease), strings.Index(body, c.net)
		if li < 0 || ni < 0 {
			t.Errorf("%s path: could not find %s (%d) and %s (%d) — the anchor has moved "+
				"and this check is measuring nothing", c.what, c.lease, li, c.net, ni)
			continue
		}
		if li > ni {
			t.Errorf("%s path starts dhcpNetworks BEFORE dhcpLeases: every subnet's lease "+
				"count reads 0 for a full 600s poll interval, because UsedLeaseIPs() has "+
				"nothing to return yet", c.what)
		}
	}
}
