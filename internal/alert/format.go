// Package alert holds the alerter's pure presentation decisions — the port of
// the four helpers in `src/alerter.js` that hold no state and send nothing.
//
// The evaluator, the cooldown maps and the delivery are NOT ported: they are
// per-router state and outbound I/O. What is here is how an alert READS.
package alert

import (
	"regexp"
	"strconv"
	"strings"
)

var placeholder = regexp.MustCompile(`\{\{(\w+)\}\}`)

// Render substitutes `{{key}}` from vars.
//
// ── ABSENT AND NULL ARE DIFFERENT, AND THAT IS NOT A TYPO ───────────────────
//
// The live guard is `vars[k] === undefined`, so a key that is ABSENT renders
// empty while a key present holding NULL renders the word "null". Reproduced
// deliberately: the difference shows up in an alert an operator reads, and
// collapsing them would quietly change what a template with a nulled variable
// says.
//
// In Go the distinction is presence in the map, which is why `vars` is
// `map[string]any` and a nil VALUE is not the same as a missing KEY.
//
// Control characters are stripped and the result is capped at 200 — IN THAT
// ORDER, so the cap counts what survives rather than what arrived.
func Render(tpl string, vars map[string]any) string {
	return placeholder.ReplaceAllStringFunc(tpl, func(m string) string {
		k := placeholder.FindStringSubmatch(m)[1]
		v, present := vars[k]
		if !present {
			return ""
		}
		s := jsString(v)
		s = strings.Map(func(r rune) rune {
			if r <= 0x1f || r == 0x7f {
				return -1
			}
			return r
		}, s)
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	})
}

var (
	reEther  = regexp.MustCompile(`(?i)^ether`)
	reWlan   = regexp.MustCompile(`(?i)^wlan|^wireless|^wifi`)
	reBridge = regexp.MustCompile(`(?i)^bridge`)
	reVlan   = regexp.MustCompile(`(?i)^vlan|\.\d+$`)
)

// IfaceType classifies an interface.
//
// An EXPLICIT type wins, and RouterOS 7's new wifi package reports `wifi` where
// the rest of the app says `wlan`, so that one is normalised.
//
// ── "unknown" AND "" FALL THROUGH; ANYTHING ELSE DOES NOT ───────────────────
//
// A type of `unknown` — or none at all — drops to name detection, while an
// unrecognised but real type (`pppoe`, say) is "other" and never reaches the
// name rules. Getting that backwards would classify a pppoe interface called
// `ether-wan` as an ether.
//
// The NAME rules are ORDERED, and the order is load-bearing: `ether1.10` is an
// ether, because `^ether` is tested before the dotted-suffix vlan pattern. The
// vlan rule only ever sees names starting with none of the earlier prefixes.
func IfaceType(name, typ string) string {
	t := strings.ToLower(typ)
	switch {
	case t == "ether":
		return "ether"
	case t == "wlan" || t == "wifi":
		return "wlan"
	case t == "bridge":
		return "bridge"
	case t == "vlan":
		return "vlan"
	case t != "" && t != "unknown":
		return "other"
	}
	switch {
	case reEther.MatchString(name):
		return "ether"
	case reWlan.MatchString(name):
		return "wlan"
	case reBridge.MatchString(name):
		return "bridge"
	case reVlan.MatchString(name):
		return "vlan"
	}
	return "other"
}

// IfaceTypeKey maps a type to the per-recipient settings key that filters it.
//
// Returned as a KEY rather than resolved here, because the interface-type filter
// is a second toggle each recipient answers separately — a user filtering to
// wlan-only is the whole point of it.
func IfaceTypeKey(t string) string {
	switch t {
	case "ether":
		return "notifIfaceEther"
	case "wlan":
		return "notifIfaceWlan"
	case "bridge":
		return "notifIfaceBridge"
	case "vlan":
		return "notifIfaceVlan"
	default:
		return "notifIfaceOther"
	}
}

// alertLabels are the types whose name would otherwise read as a database
// column.
var alertLabels = map[string]string{
	"routeros_update":  "Update Available",
	"routeros_updated": "Up To Date",
	"connectivity":     "Router Connectivity",
}

// labelAcronyms are the words the mechanical title-caser would mangle into
// "Bgp" and "Ok".
var labelAcronyms = map[string]string{
	"bgp": "BGP", "vpn": "VPN", "cpu": "CPU", "ok": "OK", "os": "OS",
}

// LabelFor is the human name for a stored alert type.
//
// `alert_type` in the database is minted by lowercasing and underscoring, which
// makes a good key and a poor label — an alert loaded from the database
// otherwise rendered as "routeros_update".
//
// Derived rather than stored, so the live socket path and the historical
// database path cannot disagree about what an alert is called, and renaming one
// needs no data migration.
//
// ACRONYMS ARE PER WORD: `cpu_ok` is "CPU OK", not "Cpu Ok".
func LabelFor(alertType string) string {
	if alertType == "" {
		return "Alert"
	}
	key := strings.ToLower(alertType)
	if l, ok := alertLabels[key]; ok {
		return l
	}
	var words []string
	for _, w := range strings.Split(key, "_") {
		if w == "" {
			continue // `filter(Boolean)`: a doubled underscore adds no word
		}
		if a, ok := labelAcronyms[w]; ok {
			words = append(words, a)
			continue
		}
		words = append(words, strings.ToUpper(w[:1])+w[1:])
	}
	return strings.Join(words, " ")
}

// jsString renders a value the way `String(x)` does for the shapes a template
// variable carries — including `null`, which becomes the word.
func jsString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return trimFloat(x)
	case int:
		return itoa(x)
	default:
		return ""
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// trimFloat renders a float the way JavaScript does: an integral value loses its
// fraction, so `42` is "42" and not "42.0".
func trimFloat(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }
