package guard

// Which interface carries MikroDash's own management traffic — the L2 guard.
//
// The port of src/routeros/selfPath.js and selfAddress.js.
//
// WARN, NEVER REFUSE. Pulling a port, disabling a VLAN or renaming a bridge are
// all ordinary things to do, and the one time it is catastrophic is
// indistinguishable, from here, from the many times it is routine. Refusing
// would make the Bridges page useless in order to prevent a mistake that a
// sentence prevents instead.
//
// FAIL OPEN. Two menus have to be readable for this to answer at all —
// /user/active and /ip/address — and /user/active is denied to the read-only
// API user the README tells people to create. That is the COMMON case. Failing
// closed would block every VLAN edit on every correctly hardened router in
// order to guard against a mistake on the others. A caller must read
// `Resolved: false` as "no warning", never as "no risk".
//
// WHAT IT DELIBERATELY DOES NOT MODEL: only the address the router sees us
// arriving on. Not the operator's browser path, not the route the reply takes,
// not bonding or failover. If MikroDash reaches the router over a second link
// that survives the edit, this still warns — over-warning on the L2 question is
// the safe direction, and the warning names the address and the interface so
// the reader can judge it.

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"mikrodash/internal/routeros"
)

// ManagementPath is where the router sees us from, and behind which interfaces.
type ManagementPath struct {
	Resolved bool
	// Interfaces the management address sits behind. Both `interface` and
	// `actual-interface` are collected: they differ when an address is
	// configured on a bridge, where RouterOS reports the physical port as the
	// actual one, and a guard that knew only one name would miss an edit aimed
	// at the other.
	Interfaces []string
	// Address is the one that matched a configured prefix.
	Address string
	// Addresses is EVERY address the router sees us from, not just the matched
	// one — MikroDash holds several logins per router (the dashboard session,
	// the alerter, the routers overview) and they need not share a source.
	Addresses []string
}

// SelfAddresses is the router's own view of where we connect from.
//
// /user/active carries a source address per logged-in session, which is past
// any NAT, past the container bridge, past whatever the host thinks its address
// is. Nothing else available to this process can answer that question.
//
// Names are compared trimmed and lowercased — over-matching is the safe
// direction for a guard.
func SelfAddresses(activeRows []routeros.Reply, usernames []string) ([]string, bool) {
	names := map[string]bool{}
	for _, n := range usernames {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names[n] = true
		}
	}
	var out []string
	for _, r := range activeRows {
		if r["name"] == "" || r["address"] == "" {
			continue
		}
		if !names[strings.ToLower(strings.TrimSpace(r["name"]))] {
			continue
		}
		a := strings.TrimSpace(r["address"])
		if a != "" && !contains(out, a) {
			out = append(out, a)
		}
	}
	return out, len(out) > 0
}

// ResolveManagementInterfaces finds the interfaces our management address sits
// behind.
//
// An address is matched by SUBNET CONTAINMENT, not equality: the router holds
// 10.0.0.1/24 and sees us at 10.0.0.5, so the question is which configured
// prefix contains us.
func ResolveManagementInterfaces(activeRows, addressRows []routeros.Reply, usernames []string) ManagementPath {
	addrs, resolved := SelfAddresses(activeRows, usernames)
	if !resolved {
		return ManagementPath{}
	}

	var ifaces []string
	matched := ""
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		for _, row := range addressRows {
			if row["address"] == "" {
				continue
			}
			// Masked(): /ip/address holds a host address with a prefix length
			// ("10.0.0.1/24"), which is not a canonical network, and Contains
			// on an unmasked prefix would not answer the question asked.
			p, err := netip.ParsePrefix(strings.TrimSpace(row["address"]))
			if err != nil || !p.Masked().Contains(ip) {
				continue
			}
			if matched == "" {
				matched = a
			}
			for _, name := range []string{row["interface"], row["actual-interface"]} {
				if n := strings.TrimSpace(name); n != "" && !contains(ifaces, n) {
					ifaces = append(ifaces, n)
				}
			}
		}
	}

	// We know where the router sees us from, but no configured prefix contains
	// it — so we arrive over a route rather than off a connected subnet, and no
	// single interface here is "the" management interface. That is wanGuard's
	// question, not this one.
	if len(ifaces) == 0 {
		return ManagementPath{Address: addrs[0], Addresses: addrs}
	}
	return ManagementPath{Resolved: true, Interfaces: ifaces, Address: matched, Addresses: addrs}
}

// Verdict is a guard's answer. Level is "none" or "warn"; this guard never
// refuses.
type Verdict struct {
	Level       string         `json:"level"`
	Code        string         `json:"code,omitempty"`
	Detail      map[string]any `json:"detail,omitempty"`
	Fingerprint string         `json:"fingerprint,omitempty"`
}

// Warned reports whether this verdict needs acknowledging.
func (v Verdict) Warned() bool { return v.Level == "warn" }

// CheckInterfaceEdit answers: would this edit touch the interface we are
// reachable on?
//
// `targets` are the interface names the row is about — a resource declares
// which of its fields those are. `action` is "update" or "delete"; both warn,
// because disabling a port and removing it cut the same link.
//
// Names are compared trimmed and lowercased. RouterOS is case sensitive here,
// so this over-matches slightly, which is the safe direction for a warning.
func CheckInterfaceEdit(path ManagementPath, targets []string, action string) Verdict {
	if !path.Resolved {
		return Verdict{Level: "none"} // fail open
	}
	var want []string
	for _, t := range targets {
		if t = strings.TrimSpace(t); t != "" {
			want = append(want, t)
		}
	}
	if len(want) == 0 {
		return Verdict{Level: "none"}
	}

	mine := map[string]bool{}
	for _, n := range path.Interfaces {
		mine[strings.ToLower(n)] = true
	}
	hit := ""
	for _, t := range want {
		if mine[strings.ToLower(t)] {
			hit = t
			break
		}
	}
	if hit == "" {
		return Verdict{Level: "none"}
	}

	act := "update"
	if action == "delete" {
		act = "delete"
	}
	return Verdict{
		Level: "warn", Code: "self-cutoff",
		Detail:      map[string]any{"interface": hit, "address": path.Address, "action": act},
		Fingerprint: fingerprint(action, want, path),
	}
}

// fingerprint is a stable identity for the exact inputs a verdict came from.
//
// Recomputed from a fresh read on the retry, so an acknowledgement cannot be
// carried from one row to another or replayed against a different write.
func fingerprint(action string, targets []string, path ManagementPath) string {
	t := append([]string(nil), targets...)
	sort.Strings(t)
	i := append([]string(nil), path.Interfaces...)
	sort.Strings(i)
	b, _ := json.Marshal([]any{action, t, i, path.Address})
	return string(b)
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
