package guard

// Would this IP address change cut the path MikroDash reaches the router over?
//
// The IP Addresses page (#97) can change, disable or remove an address. The
// address on the subnet MikroDash connects from is the one the router answers
// it on, so changing it is the Phase 3 lockout #97 names.
//
// ── IT ANSWERS ONLY THE QUESTION IT CAN PROVE ───────────────────────────────
//
// Like routePath, it asks one thing: was the address, before the change, live
// on a subnet that contains an address the router sees MikroDash arriving from?
// It does not model a second path that might survive, and it does not guess
// whether a new address would take over.
//
// WARN, NEVER REFUSE, and, as for routes, it does not fail open: when the
// router's view of where MikroDash connects from cannot be read, a change to a
// live address warns that the guard cannot tell.
import (
	"encoding/json"
	"net/netip"
	"sort"
	"strings"

	"mikrodash/internal/routeros"
)

// AddressChange is one side of an address write: the row before it, or after it.
type AddressChange struct {
	// Present is whether the address exists on this side (false before a create
	// and after a delete).
	Present   bool
	Address   string // as RouterOS holds it, prefix length included
	Interface string
	Disabled  bool
}

func (a AddressChange) live() bool { return a.Present && !a.Disabled }

func (a AddressChange) sameEffect(o AddressChange) bool {
	return a.Present == o.Present && a.Disabled == o.Disabled &&
		strings.TrimSpace(a.Address) == strings.TrimSpace(o.Address) &&
		strings.TrimSpace(a.Interface) == strings.TrimSpace(o.Interface)
}

// CheckAddressEdit judges one address write, of either family.
//
// `activeRows` is /user/active and `usernames` the logins MikroDash uses.
// `action` is create, update or delete.
func CheckAddressEdit(activeRows []routeros.Reply, usernames []string,
	action string, before, after AddressChange) Verdict {

	// A comment moves nothing, and an address that was not live was not carrying
	// MikroDash's connection, so changing or removing it cannot cut it.
	if before.sameEffect(after) || !before.live() {
		return Verdict{Level: "none"}
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(before.Address))
	if err != nil {
		return Verdict{Level: "none"}
	}
	act := "update"
	if action == "delete" {
		act = "delete"
	}

	addrs, resolved := SelfAddresses(activeRows, usernames)
	if !resolved {
		return Verdict{
			Level: "warn", Code: "address-cutoff-unknown",
			Detail:      map[string]any{"prefix": before.Address, "interface": before.Interface, "action": act},
			Fingerprint: addressFingerprint("address-cutoff-unknown", act, before.Address, nil),
		}
	}
	subnet := prefix.Masked()
	for _, a := range addrs {
		ip, err := netip.ParseAddr(a)
		if err != nil {
			continue
		}
		ip = ip.Unmap()
		if subnet.Contains(ip) {
			return Verdict{
				Level: "warn", Code: "address-cutoff",
				Detail: map[string]any{"address": ip.String(), "prefix": before.Address,
					"interface": before.Interface, "action": act},
				Fingerprint: addressFingerprint("address-cutoff", act, before.Address, addrs),
			}
		}
	}
	return Verdict{Level: "none"}
}

// addressFingerprint binds an acknowledgement to these inputs, recomputed from a
// fresh read on the retry, as routePath's is.
func addressFingerprint(code, action, address string, addrs []string) string {
	a := append([]string(nil), addrs...)
	sort.Strings(a)
	b, _ := json.Marshal([]any{code, action, strings.TrimSpace(address), a})
	return string(b)
}
