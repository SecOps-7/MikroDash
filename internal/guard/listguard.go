package guard

// Would this list-membership change cut MikroDash off from the router?
//
// A firewall rule can match on a LIST rather than on an interface or an address:
// `in-interface-list=!LAN action=drop` on the input chain is the last line of
// every default MikroTik configuration. Its effect changes without the rule
// being touched, whenever a list's MEMBERS change. Remove the port MikroDash
// arrives on from `LAN`, and that rule starts dropping it; the fix is WinBox.
// `fwGuard` never sees this, because it is not a firewall write, and says in its
// own header that it does not model lists.
//
// ── THE QUESTION IT ANSWERS ─────────────────────────────────────────────────
//
// Does this change take MikroDash OUT of a list, or put it IN one, that an
// enabled input-chain rule matches on — such that the rule stops ACCEPTING our
// traffic or starts DROPPING it? "Us" is the interface the management address
// sits behind (interface lists) or the address the router sees us from (address
// lists), from the same `ManagementPath` the other guards use. The rule's other
// clauses go through `MatchesUs`, so a list rule on another port stays quiet.
//
// Like fwGuard it does not model ORDER: it asks whether the change COULD flip a
// rule that matches us, not whether that rule wins. And it does not model a DNS
// name in an address list, which resolves to addresses this process cannot see.
//
// WARN, NEVER REFUSE, and FAIL OPEN when the management path cannot be
// resolved, exactly as fwGuard does: the lists are firewall matching, and the
// two guards should agree about when they can speak.

import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"
)

// ListMember is one side of a membership write: the row before it, or after it.
type ListMember struct {
	// Present is whether the row exists on this side (false before a create and
	// after a delete).
	Present  bool
	List     string
	Value    string // the interface name, or the address spec
	Disabled bool
}

// ListRule is one input-chain filter rule's list match.
type ListRule struct {
	ID string
	// Match is the list clause as RouterOS holds it: `LAN`, or `!LAN` negated.
	Match string
	// Rule carries the chain, the action, the disabled flag and the other
	// clauses, which must also match our traffic for the list to matter.
	Rule FWRule
}

// CheckListMember judges one membership write.
//
// `kind` is "interface" or "address". `action` is create, update or delete.
func CheckListMember(ctx FWContext, kind string, rules []ListRule, action string, before, after ListMember) Verdict {
	if !ctx.Resolved {
		return Verdict{Level: "none"} // fail open, as fwGuard does
	}
	isUs := func(m ListMember) bool {
		if !m.Present || m.Disabled {
			return false
		}
		if kind == "interface" {
			v := strings.ToLower(strings.TrimSpace(m.Value))
			for _, i := range ctx.Interfaces {
				if v != "" && strings.ToLower(strings.TrimSpace(i)) == v {
					return true
				}
			}
			return false
		}
		return listAddressCovers(m.Value, ctx.Addresses)
	}
	beforeUs, afterUs := isUs(before), isUs(after)
	// Membership is per list: moving us from one list to another leaves the
	// first and joins the second.
	sameList := strings.TrimSpace(after.List) == strings.TrimSpace(before.List)
	var leaves, joins string
	if beforeUs && !(afterUs && sameList) {
		leaves = strings.TrimSpace(before.List)
	}
	if afterUs && !(beforeUs && sameList) {
		joins = strings.TrimSpace(after.List)
	}
	if leaves == "" && joins == "" {
		return Verdict{Level: "none"}
	}

	for _, r := range rules {
		if r.Rule.Disabled || !strings.EqualFold(strings.TrimSpace(r.Rule.Chain), "input") {
			continue
		}
		// The other clauses must match us too. The list clause is what this
		// guard reasons about, so the clause it stands for is left out of that
		// question.
		probe := r.Rule
		if kind == "interface" {
			probe.InInterface = ""
		} else {
			probe.SrcAddress = ""
		}
		if !MatchesUs(probe, ctx) {
			continue
		}
		name, negated := strings.CutPrefix(strings.TrimSpace(r.Match), "!")
		name = strings.TrimSpace(name)
		act := strings.ToLower(strings.TrimSpace(r.Rule.Action))
		accepts := act == "accept"
		blocks := act == "drop" || act == "reject" || act == "tarpit"
		if name == "" || (!accepts && !blocks) {
			continue
		}
		var list, value, move string
		switch {
		// Leaving L: a rule on `L` stops matching us, one on `!L` starts.
		case name == leaves && ((!negated && accepts) || (negated && blocks)):
			list, value, move = leaves, before.Value, "leaves"
		// Joining L: a rule on `L` starts matching us, one on `!L` stops.
		case name == joins && ((!negated && blocks) || (negated && accepts)):
			list, value, move = joins, after.Value, "joins"
		default:
			continue
		}
		effect := "starts-drop"
		if accepts {
			effect = "loses-accept"
		}
		return Verdict{
			Level: "warn", Code: "list-cutoff",
			Detail: map[string]any{"kind": kind, "list": list, "value": value, "action": action,
				"move": move, "effect": effect, "rule": r.ID, "ruleMatch": r.Match, "ruleAction": act},
			Fingerprint: listFingerprint(kind, action, list, value, r.ID, ctx.Addresses, ctx.Interfaces),
		}
	}
	return Verdict{Level: "none"}
}

// listAddressCovers is AddressCovers plus the RANGE form an address list takes
// (`a-b`), which the firewall's own address clause does not. A DNS name, or
// anything else that does not parse, is not us: this process cannot see what it
// resolves to, and claiming otherwise would be a guess.
func listAddressCovers(spec string, addresses []string) bool {
	s := strings.TrimSpace(spec)
	if s == "" {
		return false
	}
	if lo, hi, ok := strings.Cut(s, "-"); ok {
		a, err1 := netip.ParseAddr(strings.TrimSpace(lo))
		b, err2 := netip.ParseAddr(strings.TrimSpace(hi))
		if err1 != nil || err2 != nil {
			return false
		}
		for _, x := range addresses {
			ip, err := netip.ParseAddr(strings.TrimSpace(x))
			if err != nil {
				continue
			}
			ip = ip.Unmap()
			if ip.BitLen() == a.BitLen() && a.Compare(ip) <= 0 && ip.Compare(b) <= 0 {
				return true
			}
		}
		return false
	}
	covers, decided := AddressCovers(s, addresses)
	return decided && covers
}

// listFingerprint binds an acknowledgement to these inputs, recomputed from a
// fresh read on the retry.
func listFingerprint(kind, action, list, value, rule string, addrs, ifaces []string) string {
	a := append([]string(nil), addrs...)
	sort.Strings(a)
	i := append([]string(nil), ifaces...)
	sort.Strings(i)
	b, _ := json.Marshal([]any{"list-cutoff", kind, action, list, strings.TrimSpace(value), rule, a, i})
	return string(b)
}
