package guard

// Would this DHCP client change take away the address MikroDash dials?
//
// A router whose uplink address comes from a DHCP client is reached AT that
// address: the hAP AC2 here is dialled at 10.0.0.53, which its client on ether1
// holds. Removing the client, disabling it or moving it to another interface
// drops the address, and the dashboard with it, whether MikroDash is on the same
// subnet or not — which is the gap the WAN page's lease guard leaves (it passes a
// directly attached session as safe, measured on the hAP AC2).
//
// The client row reports the address it holds, so the question needs no other
// read: does the client, as it stands before the change, hold one of the
// addresses MikroDash dials? The dialled host is resolved by the caller; a name
// that does not resolve gives no addresses, and the guard then stays quiet —
// the lease guard's posture, failing open rather than prompting on every edit.
//
// WARN, NEVER REFUSE. A default route the client installs is judged separately,
// by tunnelDefault, as the route guard judges any default route.
import (
	"encoding/json"
	"net/netip"
	"strings"
)

// DHCPClientChange is one side of a DHCP client write.
type DHCPClientChange struct {
	Present   bool
	Disabled  bool
	Interface string
	// Address is what the client holds, "a.b.c.d/len" as RouterOS reports it;
	// empty when it holds nothing.
	Address string
}

// CheckDHCPClientEdit judges one DHCP client write. `dialled` is every address
// MikroDash reaches this router at.
func CheckDHCPClientEdit(dialled []string, action string, before, after DHCPClientChange) Verdict {
	if !before.Present || before.Disabled || strings.TrimSpace(before.Address) == "" {
		return Verdict{Level: "none"}
	}
	stillHolds := after.Present && !after.Disabled &&
		strings.TrimSpace(after.Interface) == strings.TrimSpace(before.Interface)
	if stillHolds {
		return Verdict{Level: "none"}
	}
	held := strings.TrimSpace(before.Address)
	if i := strings.IndexByte(held, '/'); i >= 0 {
		held = held[:i]
	}
	h, err := netip.ParseAddr(held)
	if err != nil {
		return Verdict{Level: "none"}
	}
	for _, d := range dialled {
		a, err := netip.ParseAddr(strings.TrimSpace(d))
		if err != nil || a.Unmap() != h.Unmap() {
			continue
		}
		act := actionWord(action)
		b, _ := json.Marshal([]string{"dhcp-client-cutoff", act, before.Interface, h.String()})
		return Verdict{Level: "warn", Code: "dhcp-client-cutoff",
			Detail:      map[string]any{"address": h.String(), "interface": before.Interface, "action": act},
			Fingerprint: string(b)}
	}
	return Verdict{Level: "none"}
}
