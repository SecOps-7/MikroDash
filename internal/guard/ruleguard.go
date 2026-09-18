package guard

// Would this routing rule change how the router routes its replies to MikroDash?
//
// A rule in /routing/rule runs before the main table is consulted (the default
// policy-rules order is mangle, vrf, local, USER, main), so a rule can send the
// router's replies to MikroDash somewhere else entirely: `unreachable` or `drop`
// on its address, or a lookup in a table that has no route back. The classic
// case is the ordinary one — "send the LAN out through ISP2", src-address=LAN
// action=lookup-only-in-table table=isp2 — which also matches the router's own
// replies from its LAN address, and the table holding only a default route
// out of ISP2 sends them there.
//
// ── IT ANSWERS ONLY THE QUESTION IT CAN PROVE ───────────────────────────────
//
// It asks whether a rule, before or after the change, COULD match a reply to an
// address the router sees MikroDash at: enabled, no routing mark (the router's
// replies carry none unless mangle adds one) and no interface (a locally
// generated reply has no incoming one), a destination that covers the address
// or none at all, and an action that steers — drop, unreachable, or a lookup in
// a table other than main. It does not model src-address, because the router
// answers from its own address and which one depends on the path; a rule it
// cannot rule out is warned about. WARN, NEVER REFUSE, like routePath, and like
// routePath it warns when where MikroDash connects from cannot be read.
import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"mikrodash/internal/routeros"
)

// RuleChange is one side of a routing rule write.
type RuleChange struct {
	Present   bool
	Disabled  bool
	Dst       string
	Src       string
	Mark      string
	Interface string
	Action    string
	Table     string
}

func (r RuleChange) same(o RuleChange) bool {
	t := strings.TrimSpace
	return r.Present == o.Present && r.Disabled == o.Disabled && t(r.Dst) == t(o.Dst) &&
		t(r.Src) == t(o.Src) && t(r.Mark) == t(o.Mark) && t(r.Interface) == t(o.Interface) &&
		t(r.Action) == t(o.Action) && t(r.Table) == t(o.Table)
}

// steers reports whether the rule could redirect an unmarked, locally generated
// reply: enabled, no mark, no interface, and an action that is not "look it up
// in main as usual".
func (r RuleChange) steers() bool {
	if !r.Present || r.Disabled || strings.TrimSpace(r.Mark) != "" || strings.TrimSpace(r.Interface) != "" {
		return false
	}
	switch strings.TrimSpace(r.Action) {
	case "drop", "unreachable":
		return true
	case "lookup", "lookup-only-in-table", "":
		// `lookup` is RouterOS's default action. A lookup in main is what
		// happens without the rule.
		return strings.TrimSpace(r.Table) != "main"
	}
	// An action this guard does not know (mangle, in 7.24) is warned about
	// rather than assumed harmless.
	return true
}

// covers reports whether the rule's destination could include `ip`. An empty
// or unparseable destination matches everything as far as this guard can tell.
func (r RuleChange) covers(ip netip.Addr) bool {
	d := strings.TrimSpace(r.Dst)
	if d == "" {
		return true
	}
	p, err := netip.ParsePrefix(d)
	if err != nil {
		if a, aerr := netip.ParseAddr(d); aerr == nil {
			return a.Unmap() == ip
		}
		return true
	}
	return p.Masked().Contains(ip)
}

// CheckRuleEdit judges one routing rule write. `activeRows` is /user/active and
// `usernames` the logins MikroDash uses; `action` is create, update or delete.
func CheckRuleEdit(activeRows []routeros.Reply, usernames []string, action string, before, after RuleChange) Verdict {
	if before.same(after) {
		return Verdict{Level: "none"}
	}
	var sides []RuleChange
	for _, s := range []RuleChange{before, after} {
		if s.steers() {
			sides = append(sides, s)
		}
	}
	if len(sides) == 0 {
		return Verdict{Level: "none"}
	}
	act := "update"
	if action == "create" || action == "delete" {
		act = action
	}
	rule := sides[len(sides)-1]
	addrs, resolved := SelfAddresses(activeRows, usernames)
	if !resolved {
		return Verdict{Level: "warn", Code: "rule-cutoff-unknown",
			Detail:      ruleDetail(rule, act, ""),
			Fingerprint: ruleFingerprint("rule-cutoff-unknown", act, sides, nil)}
	}
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		for _, s := range sides {
			if s.covers(ip) {
				return Verdict{Level: "warn", Code: "rule-cutoff",
					Detail:      ruleDetail(s, act, ip.String()),
					Fingerprint: ruleFingerprint("rule-cutoff", act, sides, addrs)}
			}
		}
	}
	return Verdict{Level: "none"}
}

func ruleDetail(r RuleChange, act, addr string) map[string]any {
	dst := strings.TrimSpace(r.Dst)
	if dst == "" {
		dst = "any destination"
	}
	a := strings.TrimSpace(r.Action)
	if a == "" {
		a = "lookup"
	}
	d := map[string]any{"destination": dst, "ruleAction": a, "table": strings.TrimSpace(r.Table), "action": act}
	if addr != "" {
		d["address"] = addr
	}
	return d
}

// ruleFingerprint binds an acknowledgement to these inputs, recomputed from a
// fresh read on the retry, as routePath's is.
func ruleFingerprint(code, action string, sides []RuleChange, addrs []string) string {
	s := make([]string, len(sides))
	for i, r := range sides {
		s[i] = strings.Join([]string{r.Dst, r.Src, r.Action, r.Table}, "|")
	}
	a := append([]string(nil), addrs...)
	sort.Strings(a)
	b, _ := json.Marshal([]any{code, action, s, a})
	return string(b)
}
