package guard

// Would this firewall rule cut MikroDash off from the router it manages?
//
// ── Where this sits among the other four ────────────────────────────────────
//
//	selfguard    REFUSES, fails CLOSED   protects usernames on /user
//	queueguard   warns,   fails OPEN     would this queue throttle us
//	wanguard     warns,   fails OPEN     is the management path local or remote
//	selfpath     warns,   fails OPEN     which interface are we reachable on
//	fwguard      warns,   fails OPEN     could this RULE block our session
//
// The others ask about topology. This one asks about MATCHING, which is the
// whole of what a firewall does: a bad input-chain rule locks this app out of
// the router, and the fix is WinBox.
//
// WARN, NEVER REFUSE. `chain=input action=drop` as the last line of a properly
// ordered chain is not a mistake — it is the correct end of every hardened
// firewall, and from here it is indistinguishable from the same rule placed
// first, which locks everyone out. Refusing it would make the page useless to
// exactly the people who most need a firewall.
//
// FAIL OPEN. Two reads have to succeed for this to answer at all, and
// `/user/active` is denied to the read-only API user the README recommends.
// That is the COMMON case. Failing closed would block every firewall edit on
// every correctly hardened router.
//
// ── What it deliberately does not model ─────────────────────────────────────
//
// ORDER. Whether a rule takes effect depends on every rule above it, and
// evaluating that means writing a firewall simulator whose bugs would be
// invisible. So the question is narrower and answerable: COULD THIS RULE MATCH
// our management traffic at all — and, symmetrically, does the accept rule being
// removed currently match it. Those two cover the ways a single edit locks you
// out. They do not cover a reorder that changes which of two existing rules
// wins.
//
// Also not modelled: address lists, `jump` targets, layer7, time windows,
// connection state, and negated matches. Each would be a source of confident
// wrong answers.

import (
	"encoding/json"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// blockingActions are the actions that stop a packet reaching the router.
var blockingActions = map[string]bool{"drop": true, "reject": true, "tarpit": true}

// toRouterChain is the chain in each table that sees traffic addressed TO the
// router.
//
// `forward` is traffic passing THROUGH and cannot touch our session, which is
// why the great majority of firewall edits raise nothing here. Tables absent
// from this map cannot block us at all: mangle has no dropping action, and NAT
// is not a filter.
var toRouterChain = map[string]string{
	"/ip/firewall/filter": "input",
	"/ip/firewall/raw":    "prerouting",
}

// inCIDRs is src/util/ip.js's isInCidrs, quirks included.
//
// TWO OF THEM ARE LOAD-BEARING, and both were found by running the original
// rather than reading it.
//
//  1. A MISSING OR UNREADABLE PREFIX MEANS THE WHOLE ADDRESS — /32 or /128.
//
//     This used to be the opposite, deliberately. `parseInt(parts[1], 10)` is
//     NaN when there is no slash, ipaddr.js's matchCIDR loops
//     `while (cidrBits > 0)`, NaN fails that immediately and it returns true —
//     so a spec with no prefix matched EVERY address of the same family, and
//     `isInCidrs('8.8.8.8', ['10.0.0.5'])` was true. This port REPRODUCED that,
//     on the argument that a guard disagreeing with the app it ports is worse
//     than one inheriting its bug, and reported it as ToDo item 8.
//
//     The live app fixed it in `d9da7b1`, prompted by that report, so this is
//     now a re-sync rather than a divergence. `/32` and `/128` are what a bare
//     address means everywhere in RouterOS.
//
//     AN EXPLICIT `/0` STILL MATCHES EVERYTHING, and so does a negative prefix:
//     the live fix is `Number.isNaN(bits) ? full : bits`, which leaves any
//     READABLE number alone, and matchCIDR's `while (cidrBits > 0)` then never
//     runs. Only the unreadable case changed. Conflating the two — treating
//     `bits <= 0` as the NaN signal — is why this function used a -1 sentinel,
//     and it is exactly what would break `0.0.0.0/0`.
//
//  2. ipaddr.js ACCEPTS THE COMPACT IPv4 FORMS — `10.0.0` is 10.0.0.0, `10.0` is
//     10.0.0.0, a bare integer is the whole address, and any part may be octal
//     or hex. netip parses none of them, so they are handled here.
//
// A family mismatch is false, which is what the original's thrown-and-caught
// length check comes to.
// InCIDRs is the exported form, for callers outside this package.
//
// The BANDWIDTH collector filters its rows on the same helper the live app uses
// — `src/util/ip.js`'s `isInCidrs` — so it shares this port rather than growing
// a second one that would have to inherit the same quirks independently.
func InCIDRs(ip string, cidrs []string) bool { return inCIDRs(ip, cidrs) }

func inCIDRs(ip string, cidrs []string) bool {
	addr, ok := parseAddrLenient(strings.TrimSpace(ip))
	if !ok {
		return false
	}
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		base, bits, ok := parseCIDRLenient(c)
		if !ok || base.BitLen() != addr.BitLen() {
			continue
		}
		switch {
		case bits <= 0:
			// An explicit /0 — or a negative prefix, which parseInt reads
			// happily. matchCIDR's `while (cidrBits > 0)` never runs and it
			// returns true. NOT the NaN case any more; see the header.
			return true
		case bits > base.BitLen():
			// matchCIDR THROWS on a mask longer than the address, and the throw
			// is caught as false. Verified against the original — the obvious
			// reading, that it runs off the end of the octet array and returns
			// true, is wrong.
			continue
		case bits == base.BitLen():
			if base == addr {
				return true
			}
		default:
			if p, err := base.Prefix(bits); err == nil && p.Contains(addr) {
				return true
			}
		}
	}
	return false
}

// parseCIDRLenient is the live repo's own parseCIDR: split on "/", parse the
// left half, `parseInt` the right. Bits are -1 for the NaN cases.
func parseCIDRLenient(c string) (netip.Addr, int, bool) {
	left, right, hasSlash := strings.Cut(c, "/")
	addr, ok := parseAddrLenient(left)
	if !ok {
		return netip.Addr{}, 0, false
	}
	if !hasSlash {
		return addr, addr.BitLen(), true
	}
	n, ok := leadingInt(strings.TrimSpace(right))
	if !ok {
		// parseInt gave NaN, which now means the whole address.
		return addr, addr.BitLen(), true
	}
	return addr, n, true
}

// leadingInt is `parseInt(s, 10)`: a leading integer with whatever follows it
// ignored, and "not a number at all" reported separately.
//
// NOT strconv.Atoi, which this used before. `parseInt('8abc', 10)` is 8 while
// Atoi errors — and under the old NaN handling that difference turned a /8 into
// "matches everything", which is the same class of bug as the one above rather
// than a separate one.
func leadingInt(s string) (int, bool) {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return 0, false
	}
	n, err := strconv.Atoi(s[:i])
	if err != nil {
		// Longer than an int can hold. parseInt would give a float far outside
		// any prefix length, so saturate rather than wrap.
		if s[0] == '-' {
			return -1 << 30, true
		}
		return 1 << 30, true
	}
	return n, true
}

var v4PartRe = regexp.MustCompile(`^(?:0[xX][0-9a-fA-F]+|0[0-7]+|\d+)$`)

// parseIntAuto is ipaddr.js's: 0x is hex, a leading 0 is octal, else decimal.
func parseIntAuto(s string) (uint64, bool) {
	if !v4PartRe.MatchString(s) {
		return 0, false
	}
	switch {
	case strings.HasPrefix(s, "0x"), strings.HasPrefix(s, "0X"):
		n, err := strconv.ParseUint(s[2:], 16, 64)
		return n, err == nil
	case len(s) > 1 && s[0] == '0':
		n, err := strconv.ParseUint(s[1:], 8, 64)
		return n, err == nil
	default:
		n, err := strconv.ParseUint(s, 10, 64)
		return n, err == nil
	}
}

// parseAddrLenient is ipaddr.js's parse: netip for the ordinary forms, plus the
// compact IPv4 spellings netip rejects.
func parseAddrLenient(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return netip.Addr{}, false
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return a, true
	}
	if strings.Contains(s, ":") {
		return netip.Addr{}, false // v6 is netip's job alone
	}
	parts := strings.Split(s, ".")
	vals := make([]uint64, len(parts))
	for i, p := range parts {
		n, ok := parseIntAuto(p)
		if !ok {
			return netip.Addr{}, false
		}
		vals[i] = n
	}
	// The last part absorbs the remaining octets; every earlier part is one.
	var n uint64
	switch len(parts) {
	case 1:
		n = vals[0]
		if n > 0xffffffff {
			return netip.Addr{}, false
		}
	case 2, 3, 4:
		last := len(parts) - 1
		limit := uint64(1) << (8 * (4 - last))
		if vals[last] >= limit {
			return netip.Addr{}, false
		}
		n = vals[last]
		for i := last - 1; i >= 0; i-- {
			if vals[i] > 255 {
				return netip.Addr{}, false
			}
			n |= vals[i] << (8 * (3 - i))
		}
	default:
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), true
}

// addrLike is the original's test for "a spec that looks parseable". Hex digits,
// colons, dots and slashes only.
var addrLike = regexp.MustCompile(`^[0-9a-fA-F:./]+$`)

// AddressCovers answers whether an address spec covers any of our addresses.
//
// THREE-VALUED, which is why it returns two bools. The second is `decided`;
// false means UNDECIDABLE — a range (`10.0.0.1-10.0.0.5`), a negation
// (`!10.0.0.0/8`), an address-list name, anything unparseable. The caller treats
// undecidable as a MATCH, because for a blocking rule the safe direction is to
// ask rather than to stay quiet.
func AddressCovers(spec string, addresses []string) (covers, decided bool) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return true, true // no source match: everything, including us
	}
	if strings.HasPrefix(s, "!") || strings.Contains(s, "-") {
		return false, false
	}
	addrs := make([]string, 0, len(addresses))
	for _, a := range addresses {
		if a != "" {
			addrs = append(addrs, a)
		}
	}
	if len(addrs) == 0 {
		return false, false
	}
	for _, a := range addrs {
		if inCIDRs(a, []string{s}) {
			return true, true
		}
	}
	// The original writes this as `isInCidrs(addrs[0], [s]) === false && looksLikeAddress`,
	// and the first half is ALWAYS true here: the loop above just established
	// that no address matches, and addrs[0] is one of them. So the spelling that
	// survives is the second half alone — a spec that parses but does not
	// contain us is a decided "no"; one that does not parse is undecidable.
	if addrLike.MatchString(s) {
		return false, true
	}
	return false, false
}

// PortCovers answers whether a RouterOS port spec — `443`, `80,443`,
// `1000-2000` — includes `port`.
func PortCovers(spec string, port int) bool {
	s := strings.TrimSpace(spec)
	if s == "" {
		return true // no port match: every port, including ours
	}
	for _, part := range strings.Split(s, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if lo, hi, ok := strings.Cut(p, "-"); ok {
			// JavaScript's Number(), so " 80 " and "0443" both work and
			// anything else is NaN and skipped.
			l, lerr := jsNumber(lo)
			h, herr := jsNumber(hi)
			if lerr == nil && herr == nil && float64(port) >= l && float64(port) <= h {
				return true
			}
			continue
		}
		if n, err := jsNumber(p); err == nil && n == float64(port) {
			return true
		}
	}
	return false
}

// jsNumber is Number(): whitespace-tolerant, leading zeros fine, empty is 0 —
// though an empty part never reaches here.
func jsNumber(s string) (float64, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, nil
	}
	return strconv.ParseFloat(t, 64)
}

// FWContext is what the guard needs to know about our own connection.
type FWContext struct {
	Resolved   bool
	Addresses  []string
	Interfaces []string
	APIPort    int
}

// FWRule is a rule in the resource registry's field names.
type FWRule struct {
	Chain       string
	Action      string
	SrcAddress  string
	DstAddress  string
	Protocol    string
	DstPort     string
	InInterface string
	Disabled    bool
}

// MatchesUs answers: could this rule match the traffic that keeps MikroDash
// connected?
//
// EVERY CLAUSE HAS TO HOLD, and an empty field matches everything — which is why
// a bare `chain=input action=drop` is the loudest case here: it matches on all
// four counts.
func MatchesUs(rule FWRule, ctx FWContext) bool {
	if covers, decided := AddressCovers(rule.SrcAddress, ctx.Addresses); decided && !covers {
		return false
	}

	proto := strings.ToLower(strings.TrimSpace(rule.Protocol))
	if proto != "" && proto != "tcp" {
		return false // the API is TCP
	}

	if !PortCovers(rule.DstPort, ctx.APIPort) {
		return false
	}

	inIf := strings.ToLower(strings.TrimSpace(rule.InInterface))
	if inIf != "" {
		found := false
		for _, i := range ctx.Interfaces {
			if strings.ToLower(i) == inIf {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// CheckRule is the verdict.
//
// `what` is "create" | "update" | "delete" | "enable" | "disable" | "move".
// `before` is the freshly-read row in resource field names, or nil on create.
func CheckRule(ctx FWContext, menu string, values FWRule, before *FWRule, what string) Verdict {
	if !ctx.Resolved {
		return Verdict{Level: "none"} // fail open
	}
	chain, ok := toRouterChain[menu]
	if !ok {
		return Verdict{Level: "none"} // mangle and NAT cannot block us
	}

	removing := what == "delete" || what == "disable"
	// On removal the rule of interest is the one already there; otherwise it is
	// the one about to exist.
	rule := values
	if removing {
		if before != nil {
			rule = *before
		} else {
			rule = FWRule{}
		}
	}
	if strings.ToLower(rule.Chain) != chain {
		return Verdict{Level: "none"}
	}
	if !MatchesUs(rule, ctx) {
		return Verdict{Level: "none"}
	}

	act := strings.ToLower(rule.Action)
	where := func(kind string) map[string]any {
		d := map[string]any{"kind": kind, "chain": chain, "action": act, "what": what,
			"port": ctx.APIPort}
		d["address"] = firstOrNil(ctx.Addresses)
		d["interface"] = firstOrNil(ctx.Interfaces)
		return d
	}

	// 1. A rule that would stop our packets — created, edited into existence,
	//    enabled, or moved somewhere it may now win. A rule left disabled blocks
	//    nothing, so it only counts when it is on, or being switched on.
	if !removing && blockingActions[act] && !(rule.Disabled && what != "enable") {
		return Verdict{Level: "warn", Code: "self-lockout",
			Detail:      where("block"),
			Fingerprint: fwFingerprint(what, menu, rule, ctx)}
	}

	// 2. The other half: the rule letting us in is the one being taken away or
	//    moved.
	beforeDisabled := before != nil && before.Disabled
	if (removing || what == "move") && act == "accept" && !beforeDisabled {
		return Verdict{Level: "warn", Code: "self-lockout",
			Detail:      where("accept-removed"),
			Fingerprint: fwFingerprint(what, menu, rule, ctx)}
	}

	return Verdict{Level: "none"}
}

// firstOrNil returns the first element, or nil so it marshals as JSON null the
// way the original's `|| null` does.
func firstOrNil(s []string) any {
	if len(s) == 0 {
		return nil
	}
	return s[0]
}

func fwFingerprint(what, menu string, rule FWRule, ctx FWContext) string {
	addrs := append([]string(nil), ctx.Addresses...)
	sort.Strings(addrs)
	// An empty slice must marshal as [] rather than null, which is what
	// JSON.stringify does with the original's `.slice().sort()`.
	if addrs == nil {
		addrs = []string{}
	}
	b, _ := json.Marshal([]any{
		what, menu, rule.Chain, rule.Action,
		rule.SrcAddress, rule.DstAddress, rule.Protocol, rule.DstPort, rule.InInterface,
		addrs, ctx.APIPort,
	})
	return string(b)
}
