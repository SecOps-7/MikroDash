package guard

// Would this IPsec change take away the path the router answers MikroDash on?
//
// Two ways, both measured against RouterOS 7.24 on the CHR:
//
//   - A POLICY applies to the router's own traffic. One whose destination covers
//     MikroDash's address and whose action is `encrypt` or `discard` sends the
//     router's replies into a tunnel MikroDash is not at the far end of, or
//     nowhere. The ordinary site-to-site policy (192.168.88.0/24 to the far
//     LAN) is fine; a careless 0.0.0.0/0 is not.
//   - A router managed THROUGH a tunnel: MikroDash's address is inside a policy
//     that already encrypts, so removing or disabling that policy, its peer or
//     the peer's identity drops the tunnel MikroDash arrives over.
//
// Both are the same question — does an enabled, non-template policy that
// encrypts or discards cover MikroDash's address, before or after the change —
// so one guard answers it for policies, peers and identities.
//
// WARN, NEVER REFUSE, like routePath and rulePath, and it warns that it cannot
// tell when /user/active cannot be read. It does not model src-address or
// ports: the router answers from its own address, and a policy it cannot rule
// out is warned about.
import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"mikrodash/internal/routeros"
)

// IPsecPolicy is one side of a policy write, or a policy read from the router.
type IPsecPolicy struct {
	Present  bool
	Disabled bool
	Template bool
	Dst      string
	Protocol string
	Action   string
	Peer     string
}

// IPsecPolicyOf reads a policy row in RouterOS spellings.
func IPsecPolicyOf(r routeros.Reply) IPsecPolicy {
	return IPsecPolicy{Present: true, Disabled: r["disabled"] == "true", Template: r["template"] == "true",
		Dst: r["dst-address"], Protocol: r["protocol"], Action: r["action"], Peer: r["peer"]}
}

// steers reports whether the policy could take the router's replies away: in
// force, and an action other than `none` (which bypasses IPsec). `encrypt` is
// RouterOS's default action.
func (p IPsecPolicy) steers() bool {
	if !p.Present || p.Disabled || p.Template {
		return false
	}
	switch strings.TrimSpace(p.Protocol) {
	case "", "all", "tcp":
	default:
		return false // the API is TCP; a policy for another protocol cannot carry it
	}
	return strings.TrimSpace(p.Action) != "none"
}

func (p IPsecPolicy) covers(ip netip.Addr) bool {
	d := strings.TrimSpace(p.Dst)
	if d == "" {
		return true
	}
	pr, err := netip.ParsePrefix(d)
	if err != nil {
		if a, aerr := netip.ParseAddr(d); aerr == nil {
			return a.Unmap() == ip
		}
		return true
	}
	return pr.Masked().Contains(ip)
}

func (p IPsecPolicy) same(o IPsecPolicy) bool {
	t := strings.TrimSpace
	return p.Present == o.Present && p.Disabled == o.Disabled && p.Template == o.Template &&
		t(p.Dst) == t(o.Dst) && t(p.Protocol) == t(o.Protocol) && t(p.Action) == t(o.Action) && t(p.Peer) == t(o.Peer)
}

// CheckIPsecPolicyEdit judges one policy write. `activeRows` is /user/active and
// `usernames` the logins MikroDash uses; `action` is create, update or delete.
func CheckIPsecPolicyEdit(activeRows []routeros.Reply, usernames []string, action string, before, after IPsecPolicy) Verdict {
	if before.same(after) {
		return Verdict{Level: "none"}
	}
	var sides []IPsecPolicy
	for _, s := range []IPsecPolicy{before, after} {
		if s.steers() {
			sides = append(sides, s)
		}
	}
	if len(sides) == 0 {
		return Verdict{Level: "none"}
	}
	act := actionWord(action)
	addrs, resolved := SelfAddresses(activeRows, usernames)
	if !resolved {
		s := sides[len(sides)-1]
		return Verdict{Level: "warn", Code: "ipsec-cutoff-unknown",
			Detail:      map[string]any{"destination": dstWord(s.Dst), "ipsecAction": actWord(s.Action), "action": act},
			Fingerprint: ipsecFingerprint("ipsec-cutoff-unknown", act, sides, nil)}
	}
	for _, s := range sides {
		if ip, ok := coveredAddr(s, addrs); ok {
			return Verdict{Level: "warn", Code: "ipsec-cutoff",
				Detail: map[string]any{"address": ip, "destination": dstWord(s.Dst),
					"ipsecAction": actWord(s.Action), "action": act},
				Fingerprint: ipsecFingerprint("ipsec-cutoff", act, sides, addrs)}
		}
	}
	return Verdict{Level: "none"}
}

// CheckIPsecPeerEdit judges a write to a peer, or to an identity, that could
// drop the tunnel MikroDash arrives over: the peer named `peer` (before the
// change) carries MikroDash when an enabled policy that encrypts through it
// covers MikroDash's address. `policies` is /ip/ipsec/policy, read fresh.
// `changed` is false for an edit that moves nothing but the comment.
func CheckIPsecPeerEdit(activeRows []routeros.Reply, usernames []string, policies []routeros.Reply,
	action, peer string, changed bool) Verdict {
	if !changed || strings.TrimSpace(peer) == "" {
		return Verdict{Level: "none"}
	}
	var through []IPsecPolicy
	for _, r := range policies {
		p := IPsecPolicyOf(r)
		if p.steers() && strings.TrimSpace(p.Action) != "discard" && strings.TrimSpace(p.Peer) == strings.TrimSpace(peer) {
			through = append(through, p)
		}
	}
	if len(through) == 0 {
		return Verdict{Level: "none"}
	}
	act := actionWord(action)
	addrs, resolved := SelfAddresses(activeRows, usernames)
	if !resolved {
		return Verdict{Level: "warn", Code: "ipsec-peer-cutoff-unknown",
			Detail:      map[string]any{"peer": peer, "action": act},
			Fingerprint: ipsecFingerprint("ipsec-peer-cutoff-unknown", act, through, nil)}
	}
	for _, p := range through {
		if ip, ok := coveredAddr(p, addrs); ok {
			return Verdict{Level: "warn", Code: "ipsec-peer-cutoff",
				Detail:      map[string]any{"address": ip, "peer": peer, "destination": dstWord(p.Dst), "action": act},
				Fingerprint: ipsecFingerprint("ipsec-peer-cutoff", act, through, addrs)}
		}
	}
	return Verdict{Level: "none"}
}

func coveredAddr(p IPsecPolicy, addrs []string) (string, bool) {
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if p.covers(ip) {
			return ip.String(), true
		}
	}
	return "", false
}

func actionWord(action string) string {
	if action == "create" || action == "delete" {
		return action
	}
	return "update"
}

func dstWord(d string) string {
	if strings.TrimSpace(d) == "" {
		return "any destination"
	}
	return strings.TrimSpace(d)
}

func actWord(a string) string {
	if strings.TrimSpace(a) == "" {
		return "encrypt"
	}
	return strings.TrimSpace(a)
}

func ipsecFingerprint(code, action string, ps []IPsecPolicy, addrs []string) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = strings.Join([]string{p.Dst, p.Protocol, p.Action, p.Peer}, "|")
	}
	a := append([]string(nil), addrs...)
	sort.Strings(a)
	b, _ := json.Marshal([]any{code, action, s, a})
	return string(b)
}
