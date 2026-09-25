package guard

// The fleet-push guard, for the CAPsMAN profile menus.
//
// Everything else this engine writes has a blast radius you can see: a firewall
// rule affects one chain, a VLAN one interface. A CAPsMAN profile is different.
// MikroTik's documentation is explicit — "if you adjust any configuration
// profile that is linked to the provisioned interface, all changes will be
// pushed as soon as you apply changes to the profile". Saving a passphrase here
// reconnects every client on every CAP that follows it, and nothing on screen
// would otherwise say so.
//
// WHAT IT DOES NOT DO. It stays silent when nothing ENABLED references the
// profile. An unused profile is an unused profile, and a prompt on every save of
// one is how a warning becomes furniture people click through. It is also silent
// for the provisioning menu itself, which is why that resource declares no guard
// at all: a provisioning rule creates interfaces when a CAP joins, it does not
// push to the ones already running.
//
// REFERENCES RESOLVE ONE OR TWO LEVELS. A configuration profile is named
// directly by a provisioning rule. A security, channel or datapath profile is
// named by a CONFIGURATION profile, which is then named by a rule — so those
// three resolve transitively, and a profile referenced only by an unprovisioned
// configuration is correctly silent.

import (
	"encoding/json"
	"sort"
	"strings"

	"mikrodash/internal/routeros"
)

// ConfigField maps a resource key to the field of a configuration profile that
// names it.
var ConfigField = map[string]string{
	"capsSecurity": "security",
	"capsChannel":  "channel",
	"capsDatapath": "datapath",
}

// splitList: RouterOS comma lists arrive as one string.
func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ReferencingRules is the enabled provisioning rules that would push this
// profile.
//
// `configRows` and `provRows` are RAW RouterOS rows, read by the caller in the
// SAME TICK as the write is checked — not the collector's last tick, which may
// be two minutes old.
func ReferencingRules(resourceKey, name string,
	configRows, provRows []routeros.Reply) []routeros.Reply {

	if name == "" {
		return nil
	}

	// Which configuration profiles are in play. For capsConfig it is the profile
	// itself; for the other three it is every configuration naming it.
	var configNames []string
	if resourceKey == "capsConfig" {
		configNames = []string{name}
	} else {
		field, ok := ConfigField[resourceKey]
		if !ok {
			return nil
		}
		for _, c := range configRows {
			if c != nil && c[field] == name && c["name"] != "" {
				configNames = append(configNames, c["name"])
			}
		}
	}
	if len(configNames) == 0 {
		return nil
	}

	wanted := map[string]bool{}
	for _, n := range configNames {
		wanted[n] = true
	}

	var out []routeros.Reply
	for _, p := range provRows {
		// A disabled rule provisions nothing, so it cannot push anything either.
		if p == nil || rosTruthy(p["disabled"]) {
			continue
		}
		if wanted[p["master-configuration"]] {
			out = append(out, p)
			continue
		}
		for _, s := range splitList(p["slave-configurations"]) {
			if wanted[s] {
				out = append(out, p)
				break
			}
		}
	}
	return out
}

// rosTruthy is the original's `_bool`: true, "true" or "yes".
func rosTruthy(v string) bool { return v == "true" || v == "yes" }

// CapsPushInput is one write, as the caller has it.
type CapsPushInput struct {
	ResourceKey string
	Action      string
	Values      map[string]string
	// Before is the RAW freshly-read row.
	Before               routeros.Reply
	ConfigRows, ProvRows []routeros.Reply
	// CapCount is ADVISORY and fails soft: it comes from a read that may have
	// been refused, and a missing count costs a number in the sentence, never
	// the warning. Negative means "not known".
	CapCount int
}

// CheckPush answers: would saving this push to live CAPs?
func CheckPush(in CapsPushInput) Verdict {
	// A create references nothing yet — nothing is following it, so nothing
	// moves.
	if in.Action != "update" && in.Action != "delete" {
		return Verdict{Level: "none"}
	}

	// The name as the ROUTER currently has it. A rename is still a push of the
	// old profile, and `Before` is the freshly read row.
	name := ""
	if in.Before != nil {
		name = in.Before["name"]
	}
	if name == "" {
		name = in.Values["name"]
	}
	rules := ReferencingRules(in.ResourceKey, name, in.ConfigRows, in.ProvRows)
	if len(rules) == 0 {
		return Verdict{Level: "none"}
	}

	// Which submitted fields actually differ. A delete changes everything.
	var changed []string
	if in.Action == "delete" {
		changed = append(changed, "(removed)")
	} else {
		for k, v := range in.Values {
			// A secret never reads back, so any value submitted for one is a
			// change.
			if k == "passphrase" {
				if v != "" {
					changed = append(changed, k)
				}
				continue
			}
			before := ""
			if in.Before != nil {
				before = in.Before[k]
			}
			if v != before {
				changed = append(changed, k)
			}
		}
	}
	// MAP ITERATION ORDER IS SAFE HERE, unlike in wifiguard: `changed` feeds only
	// the fingerprint, which sorts it, and never reaches `detail`. Nothing the
	// browser renders depends on the order these were submitted in.

	ruleIDs := make([]string, 0, len(rules))
	names := make([]string, 0, len(rules))
	for _, r := range rules {
		ruleIDs = append(ruleIDs, r[".id"])
		names = append(names, firstNonEmptyStr(r["name-format"], r["master-configuration"], r[".id"]))
	}

	act := "update"
	if in.Action == "delete" {
		act = "delete"
	}
	detail := map[string]any{
		"profile": name, "rules": names, "ruleCount": len(rules), "action": act,
	}
	if in.CapCount >= 0 {
		detail["caps"] = in.CapCount
	} else {
		detail["caps"] = nil
	}

	return Verdict{
		Level: "warn", Code: "capsman-push", Detail: detail,
		Fingerprint: capsFingerprint(name, ruleIDs, changed),
	}
}

func capsFingerprint(name string, ruleIDs, fields []string) string {
	ids := append([]string(nil), ruleIDs...)
	sort.Strings(ids)
	f := append([]string(nil), fields...)
	sort.Strings(f)
	if ids == nil {
		ids = []string{}
	}
	if f == nil {
		f = []string{}
	}
	b, _ := json.Marshal([]any{"capsman-push", name, ids, f})
	return string(b)
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
