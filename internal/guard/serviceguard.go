package guard

// Would this /ip/service change cut MikroDash off from the router?
//
// MikroDash speaks the RouterOS API: `api` (8728) in the clear, `api-ssl` (8729)
// over TLS. Disable the one it uses, move it to another port or VRF, or restrict
// the addresses it accepts from, and the next connection fails; the fix is
// WinBox. Like the router-user guard, and unlike the warning guards, this one
// REFUSES: nothing about losing the management channel is worth a dialog.
//
// ── WHICH SERVICE IS OURS ───────────────────────────────────────────────────
//
// Named by the session's TLS setting, not matched by port: MikroDash may reach
// the router through a port-forward (the CHR test router is reached on a host
// port that is not 8729), so the configured port need not be the service's.
//
// ── WHAT IS REFUSED ─────────────────────────────────────────────────────────
//
//	disable                      no service
//	port                         nothing listening where MikroDash connects
//	vrf                          the service moves off the table MikroDash reaches
//	address                      only when it would not admit MikroDash's source
//
// An address list that admits every address the router sees MikroDash arriving
// from is allowed, and clearing it (admit all) is always allowed. When that
// source cannot be read — /user/active is denied to a read-only API user —
// an address change is REFUSED, as the router-user guard refuses one: failing
// open here would be exactly the site visit this exists to prevent.
//
// The certificate is not refused: MikroDash accepts the router's self-signed
// certificate, so which one is served does not decide whether it connects.

import (
	"strings"
)

// ServiceRow is one side of an /ip/service write.
type ServiceRow struct {
	Name     string
	Port     string
	Address  string // comma-separated prefixes; empty admits every address
	VRF      string
	Disabled bool
}

// CheckServiceEdit judges one /ip/service write. `ours` is "api" or
// "api-ssl"; `self` is every address the router sees MikroDash arriving from,
// and `resolved` whether that could be read at all.
func CheckServiceEdit(ours string, self []string, resolved bool, before, after ServiceRow) Verdict {
	if !strings.EqualFold(strings.TrimSpace(before.Name), ours) {
		return Verdict{Level: "none"}
	}
	refuse := func(code string, value any) Verdict {
		return Verdict{Level: "refuse", Code: code, Detail: map[string]any{"service": ours, "value": value}}
	}
	if after.Disabled && !before.Disabled {
		return refuse("service-disable", ours)
	}
	if strings.TrimSpace(after.Port) != strings.TrimSpace(before.Port) {
		return refuse("service-port", strings.TrimSpace(after.Port))
	}
	if strings.TrimSpace(after.VRF) != strings.TrimSpace(before.VRF) {
		return refuse("service-vrf", strings.TrimSpace(after.VRF))
	}
	addr := strings.TrimSpace(after.Address)
	if addr != strings.TrimSpace(before.Address) && addr != "" {
		if !resolved || len(self) == 0 {
			return refuse("service-address-unknown", addr)
		}
		for _, a := range self {
			if !serviceAdmits(addr, a) {
				return refuse("service-address", addr)
			}
		}
	}
	return Verdict{Level: "none"}
}

// serviceAdmits reports whether a comma-separated /ip/service address list
// admits one address. An entry that does not parse admits nothing, which is
// the safe direction for a refusal.
func serviceAdmits(list, address string) bool {
	for _, entry := range strings.Split(list, ",") {
		if covers, decided := AddressCovers(strings.TrimSpace(entry), []string{address}); decided && covers {
			return true
		}
	}
	return false
}
