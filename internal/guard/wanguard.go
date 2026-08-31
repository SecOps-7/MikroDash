package guard

// The self-cutoff warning for the WAN page — the port of
// `src/routeros/wanGuard.js`, and the last of the six live guard modules to
// land here.
//
// Renewing or releasing a DHCP lease drops that uplink for a few seconds. If
// MikroDash manages the router THROUGH that uplink, the same click drops the
// dashboard — and unlike a bad queue, it cannot be undone from the row that
// caused it, because the row is no longer reachable.
//
// ── IT WARNS, NEVER REFUSES, AND IT FAILS OPEN ──────────────────────────────
//
// Same posture as QueueGuard and for the same reasons. A remote admin with a
// stuck lease is exactly the person who most needs to renew it, so refusing
// would remove the feature at the moment it matters. And when the source
// address cannot be resolved — `/user/active` is denied to a read-only API
// user, which is the common case — no warning is produced and the action goes
// ahead. Failing closed would prompt on every routine renew on the majority of
// installs, and a prompt nobody can dismiss meaningfully is one they learn to
// click through.
//
// Do not read "guard" as "refuse". SelfGuard refuses; this and QueueGuard warn.
//
// ── HOW "AM I BEHIND THIS WAN" IS DECIDED ───────────────────────────────────
//
// `/user/active` gives the source address the ROUTER sees us from — past NAT,
// past the container bridge. If that address falls inside one of the router's
// own connected subnets, our packets never traverse a WAN and no lease action
// can strand us. If it does not, our traffic arrives over a route, and for a
// remotely-managed router that is the default route. So the WAN carrying the
// ACTIVE default route is the lifeline, and the only one worth warning about.
//
// Containment uses InCIDRs — this package's port of `src/util/ip.js`'s
// `isInCidrs`, which handles v4 and v6. QueueGuard's own three-valued IPv4
// matcher is deliberately NOT used: it is right for judging a queue target
// typed by a human and wrong for matching a real address against real subnets.
// The live module makes exactly the same distinction, in the same words.

import "encoding/json"

// WanPath is where this session sits relative to the router: on a directly
// attached network, or out beyond one of its uplinks.
//
// NAMED APART FROM selfpath.go's `ManagementPath`, which the live side also
// calls a management path. They are different questions — that one is "which
// interfaces are we behind", carrying Interfaces and every Address; this one is
// "local or remote", carrying one address and a Local flag — and JavaScript can
// hold both names because they live in separate modules. A Go package has one
// namespace, so the collision has to be resolved here. Same situation as
// `bwBpsToMbps` in internal/collect.
//
// Resolved false is "we could not tell", which is a different fact from Local
// false, and collapsing the two is what would make this guard fail closed.
type WanPath struct {
	Resolved bool   `json:"resolved"`
	Local    bool   `json:"local"`
	Address  string `json:"address"`
}

// ResolveManagementPath answers: is MikroDash on one of this router's directly
// attached networks?
//
// `connectedCidrs` is every address the router holds, from /ip/address.
//
// IF ANY session address is off-subnet we report remote, not local. Several
// sessions per router are normal and they need not share a path; the one that
// would be cut is the one that matters, so the answer errs toward warning.
func ResolveWanPath(selfAddresses []string, connectedCidrs []string) WanPath {
	cidrs := make([]string, 0, len(connectedCidrs))
	for _, c := range connectedCidrs {
		// The original filters on truthiness, so "" drops out — and a list of
		// nothing but empties is the same as no list at all, which is the
		// unresolved case below rather than "nothing contains you".
		if c != "" {
			cidrs = append(cidrs, c)
		}
	}
	if len(selfAddresses) == 0 || len(cidrs) == 0 {
		return WanPath{}
	}
	for _, a := range selfAddresses {
		if !InCIDRs(a, cidrs) {
			// The FIRST off-subnet address, not the first address. They differ
			// whenever sessions disagree about their path, which is precisely
			// the case this errs toward warning about.
			return WanPath{Resolved: true, Local: false, Address: a}
		}
	}
	// Local: every address matched, so `addrs[0]` and "the first match" are the
	// same element and the original's choice of the former is not observable.
	return WanPath{Resolved: true, Local: true, Address: selfAddresses[0]}
}

// wanFingerprint is a stable identity for the inputs a verdict came from.
//
// Echoed back by the browser to acknowledge the warning and recomputed from a
// fresh read on the retry, so an acknowledgement cannot be replayed against a
// different interface or survive our path changing underneath it. It must
// therefore match `JSON.stringify([...])` EXACTLY — a Go encoding that differed
// by one byte would reject every acknowledgement the browser sent back.
func wanFingerprint(targetWan, address, activeDefaultWan string) string {
	b, err := json.Marshal([]string{targetWan, address, activeDefaultWan})
	if err != nil {
		return ""
	}
	return string(b)
}

// CheckLeaseAction answers: would renewing or releasing this lease cut our own
// management path?
//
// `activeDefaultWan` is the interface carrying the active default route, or ""
// when that cannot be determined — in which case any WAN might be the lifeline
// and the warning applies to all of them.
func CheckLeaseAction(path *WanPath, targetWan, activeDefaultWan string) Verdict {
	if path == nil || !path.Resolved {
		return Verdict{Level: "none"} // fail open
	}
	if path.Local {
		return Verdict{Level: "none"} // cannot cut a directly attached session
	}
	// Remote, but touching an uplink that is not carrying our traffic. Return
	// packets follow the active default route; another WAN's lease is not on it.
	if activeDefaultWan != "" && targetWan != activeDefaultWan {
		return Verdict{Level: "none"}
	}
	return Verdict{
		Level: "warn",
		Code:  "self-cutoff",
		Detail: map[string]any{
			"address": path.Address,
			"wan":     targetWan,
			// False when we are warning because the active default route could
			// not be identified, rather than because this is demonstrably it.
			// The UI words the prompt differently for each.
			"certain": activeDefaultWan != "",
		},
		Fingerprint: wanFingerprint(targetWan, path.Address, activeDefaultWan),
	}
}

// ActiveDefaultWan names the uplink carrying our return traffic — the input
// CheckLeaseAction needs to tell "this WAN is the lifeline" from "some other
// WAN is". Ported from the block inside `_wanRead` (src/index.js), and pinned by
// tools/wan-default-cases.js, which LIFTS that block rather than retyping it.
//
// ── ONLY WHEN THERE IS EXACTLY ONE ──────────────────────────────────────────
//
// The live comment records this as verified against hardware: four default
// routes can be active at distance 1 simultaneously, and taking the first would
// name an uplink our packets may not use — warning about the wrong WAN while
// staying silent on the right one. Ambiguity is reported as unknown (""), which
// makes the guard warn for EVERY WAN rather than guess, with `certain: false`
// so the prompt can say why.
//
// `[0].gateway` is the wrong answer that looks right: it agrees on every
// single-route input and diverges only on the multi-homed router the rule
// exists for. The corpus asserts it disagrees somewhere, so a port that took
// the shortcut cannot pass.
//
// The gateway itself is resolved three ways, in order: a DHCP client on an
// interface of that name, a client whose own lease gateway matches it, and
// failing both the gateway string as given. A gateway is often an ADDRESS, so
// the second lookup is what makes the common case work at all.
func ActiveDefaultWan(routes []map[string]string, clients []map[string]string) string {
	var active []map[string]string
	for _, r := range routes {
		if r["dst-address"] == "0.0.0.0/0" && r["active"] == "true" {
			active = append(active, r)
		}
	}
	if len(active) != 1 {
		return ""
	}
	gw := active[0]["gateway"]
	if gw == "" {
		return ""
	}
	for _, c := range clients {
		if c["interface"] == gw {
			return c["interface"]
		}
	}
	for _, c := range clients {
		if c["gateway"] == gw {
			return c["interface"]
		}
	}
	return gw
}
