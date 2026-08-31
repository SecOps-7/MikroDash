package guard

// The inherited-profile guard, for /interface/wifi.
//
// On the modern wireless stack an interface can take its SSID, security and
// channel from a shared `/interface/wifi/configuration` profile rather than
// carrying them inline. Writing one of those values onto the INTERFACE does not
// edit the profile — it creates a local override that shadows it. On the radio
// you are looking at that is exactly what you asked for. On its sibling, which
// is still following the profile, it is a silent divergence: the two SSIDs that
// used to move together stop doing so, and nothing on screen said as much.
//
// So this warns, and only for the case that is actually surprising.
//
// IT DOES NOT FIRE FOR A PROFILE ONLY ONE INTERFACE USES. An override there
// splits nothing — there is no sibling to diverge from — and a prompt on every
// save of a defconf router is how a warning becomes furniture people learn to
// click through.
//
// Detecting inheritance at all is a COMPARISON rather than a lookup: RouterOS's
// `print detail config` (directly-set values only) has no dependable binary-API
// equivalent, so the collector decides a field is inherited when the profile
// defines it and the interface's effective value still equals it. That fails
// toward "not inherited", which suppresses a warning rather than blocking a
// write — the right direction for something that is only ever advisory.

import (
	"encoding/json"
	"sort"

	"mikrodash/internal/routeros"
)

// inheritableField pairs a submission field with the interface key carrying its
// effective value. A DOTTED key is how RouterOS reports an inherited value, so
// this is also the list of things an override would shadow.
type inheritableField struct {
	Field string
	ROS   string
}

// Inheritable is an ORDERED slice, not a map, and that is not cosmetic:
// `detail.fields` is built by walking it, and the original walks
// `Object.entries` — insertion order. A Go map would randomise the order of a
// field list the browser renders into a sentence.
var Inheritable = []inheritableField{
	{"ssid", "configuration.ssid"},
	{"authTypes", "security.authentication-types"},
	{"passphrase", "security.passphrase"},
	{"band", "channel.band"},
	{"frequency", "channel.frequency"},
	{"width", "channel.width"},
}

// WifiValues is a submission, with the PRESENCE of each field carried
// separately.
//
// The original tests `hasOwnProperty`, and a Go map cannot tell an absent field
// from an empty one — the same distinction selfguard's ValueSet exists for. It
// matters most for `passphrase`, which is write-only: absent means "leave it",
// and present-but-empty means the same thing, but present-and-set is always a
// change.
type WifiValues struct {
	Values map[string]string
	Set    map[string]bool
}

func (v WifiValues) has(field string) bool { return v.Set[field] }

// CheckInherit answers: would this write override a profile more than one
// interface shares?
//
// `before` is the RAW freshly-read RouterOS row, as every other guard receives
// it. `siblings` is every row in the menu, so the share count comes from the
// same read the write is checked against rather than from the collector's last
// tick.
func CheckInherit(values WifiValues, before routeros.Reply,
	siblings []routeros.Reply, action string) Verdict {

	// A create cannot override anything — there is no existing row whose values
	// came from a profile. A delete removes the interface, profile and all.
	if action != "update" || before == nil {
		return Verdict{Level: "none"}
	}

	profile := before["configuration"]
	if profile == "" {
		return Verdict{Level: "none"}
	}

	// How many interfaces follow this profile. One is not a divergence.
	sharedBy := 0
	for _, r := range siblings {
		if r != nil && r["configuration"] == profile {
			sharedBy++
		}
	}
	if sharedBy < 2 {
		return Verdict{Level: "none"}
	}

	// Which submitted fields are both inherited today and actually changing.
	var changing []string
	for _, f := range Inheritable {
		if !values.has(f.Field) {
			continue
		}
		next := values.Values[f.Field]
		// A passphrase is write-only: it is never read back, so "is it
		// changing" cannot be answered by comparison. A non-empty one is always
		// a change, and a blank one means leave it alone.
		if f.Field == "passphrase" {
			if next != "" {
				changing = append(changing, f.Field)
			}
			continue
		}
		if next != before[f.ROS] {
			changing = append(changing, f.Field)
		}
	}
	if len(changing) == 0 {
		return Verdict{Level: "none"}
	}

	return Verdict{
		Level: "warn", Code: "wifi-inherit",
		Detail: map[string]any{
			"profile":   profile,
			"sharedBy":  sharedBy,
			"fields":    changing,
			"interface": before["name"],
		},
		Fingerprint: wifiFingerprint(profile, changing, sharedBy),
	}
}

func wifiFingerprint(profile string, fields []string, sharedBy int) string {
	f := append([]string(nil), fields...)
	sort.Strings(f)
	b, _ := json.Marshal([]any{"wifi-inherit", profile, f, sharedBy})
	return string(b)
}
