package guard

// Would this static route change cut the path MikroDash reaches the router over?
//
// selfPath answers an INTERFACE question, so it said nothing about routes, and
// the route resources declared no guard: editing or removing the route the
// management session returns over could sever it without a word. Raised by
// HeisLuka on #97.
//
// ── IT ANSWERS ONLY THE QUESTION IT CAN PROVE ───────────────────────────────
//
// This is not a routing simulator. Policy routing, recursive next hops and
// several tables make a confident simulator worse than no guard. It asks one
// thing: does the route, before or after the change, cover an address the router
// sees MikroDash arriving from? An address on a directly connected subnet does
// not depend on a static route, so it is left out.
//
// WARN, NEVER REFUSE, like selfPath: the operator acknowledges and saves. And
// UNLIKE selfPath it does not fail open. When the router's own view of where we
// connect from cannot be read, a change that affects forwarding warns that the
// guard cannot tell, rather than implying it is safe. The operator chose that
// trade for routes on #97.
import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"mikrodash/internal/routeros"
)

// RouteChange is one side of a route write: the row before it, or after it.
type RouteChange struct {
	// Present is whether the route exists on this side (false before a create
	// and after a delete).
	Present  bool
	Dst      string
	Gateway  string
	Distance string
	Table    string
	Disabled bool
}

func (r RouteChange) forwarding() bool { return r.Present && !r.Disabled }

func (r RouteChange) sameForwarding(o RouteChange) bool {
	return r.Present == o.Present && r.Disabled == o.Disabled &&
		strings.TrimSpace(r.Dst) == strings.TrimSpace(o.Dst) &&
		strings.TrimSpace(r.Gateway) == strings.TrimSpace(o.Gateway) &&
		strings.TrimSpace(r.Distance) == strings.TrimSpace(o.Distance) &&
		strings.TrimSpace(r.Table) == strings.TrimSpace(o.Table)
}

// CheckRouteEdit judges one route write.
//
// `activeRows` is /user/active, `addressRows` every configured address (IPv4 and
// IPv6), `usernames` the logins MikroDash uses. `action` is create, update or
// delete.
func CheckRouteEdit(activeRows, addressRows []routeros.Reply, usernames []string,
	action string, before, after RouteChange) Verdict {

	// A comment, or a route that is disabled on both sides, moves no packet.
	if before.sameForwarding(after) || (!before.forwarding() && !after.forwarding()) {
		return Verdict{Level: "none"}
	}
	var dsts []netip.Prefix
	for _, side := range []RouteChange{before, after} {
		if !side.forwarding() {
			continue
		}
		if p, err := netip.ParsePrefix(strings.TrimSpace(side.Dst)); err == nil {
			dsts = append(dsts, p.Masked())
		}
	}
	if len(dsts) == 0 {
		return Verdict{Level: "none"}
	}
	act := "update"
	switch action {
	case "create", "delete":
		act = action
	}

	addrs, resolved := SelfAddresses(activeRows, usernames)
	if !resolved {
		return Verdict{
			Level: "warn", Code: "route-cutoff-unknown",
			Detail:      map[string]any{"destination": prefixList(dsts), "action": act},
			Fingerprint: routeFingerprint("route-cutoff-unknown", act, dsts, nil),
		}
	}

	var connected []netip.Prefix
	for _, row := range addressRows {
		if p, err := netip.ParsePrefix(strings.TrimSpace(row["address"])); err == nil {
			connected = append(connected, p.Masked())
		}
	}
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if within(connected, ip) {
			continue
		}
		for _, d := range dsts {
			if d.Contains(ip) {
				return Verdict{
					Level: "warn", Code: "route-cutoff",
					Detail:      map[string]any{"address": ip.String(), "destination": d.String(), "action": act},
					Fingerprint: routeFingerprint("route-cutoff", act, dsts, addrs),
				}
			}
		}
	}
	return Verdict{Level: "none"}
}

func within(prefixes []netip.Prefix, ip netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

func prefixList(ps []netip.Prefix) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.String()
	}
	return strings.Join(parts, ", ")
}

// routeFingerprint binds an acknowledgement to these inputs, recomputed from a
// fresh read on the retry, as selfPath's is.
func routeFingerprint(code, action string, dsts []netip.Prefix, addrs []string) string {
	d := make([]string, len(dsts))
	for i, p := range dsts {
		d[i] = p.String()
	}
	sort.Strings(d)
	a := append([]string(nil), addrs...)
	sort.Strings(a)
	b, _ := json.Marshal([]any{code, action, d, a})
	return string(b)
}
