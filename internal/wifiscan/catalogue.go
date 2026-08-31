package wifiscan

import "strings"

// WifiEndpoint is the stack whose frequency-scan command this port knows.
//
// Read from the live collector by the corpus generator and asserted against
// there, so a change on that side fails a test rather than silently widening
// what this port will scan.
// CORRECTED 2026-08-29. This was `/interface/wifi/registration-table/print`,
// which is `WL_ENDPOINTS.wifi` in the live collector — the registration table,
// twelve lines above the `SSID_ENDPOINTS.wifi` this guard actually mirrors. The
// corpus generator's anchor was a bare `wifi:` and matched the first one.
//
// Nothing could see it while the port's only caller passed this constant to
// `ParseCatalogue` as the endpoint: the guard compared the constant with itself
// and would have accepted any value. It surfaced when the catalogue moved to the
// Wireless collector, which passes the menu that ACTUALLY answered.
const WifiEndpoint = "/interface/wifi/print"

// ParseCatalogue turns the wireless collector's rows into the catalogue the
// Frequency Analyser's dialog is drawn from.
//
// IT RETURNS NOTHING FOR THE LEGACY STACK, AND THAT IS A REFUSAL RATHER THAN A
// GAP. The live comment: legacy `/interface/wireless` "scan command differs and
// there is no device here to verify it against. Report none rather than offering
// a picker that cannot work." Treating both stacks alike would put a
// working-looking button in front of an operator on legacy hardware, and the
// failure would arrive after the radio was already off the air.
func ParseCatalogue(rows []map[string]any, endpoint string) []Catalogue {
	if endpoint != WifiEndpoint {
		return nil
	}
	out := make([]Catalogue, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		name := strings.TrimSpace(rosString(r["name"]))
		if name == "" {
			continue
		}
		out = append(out, Catalogue{
			Name:   name,
			ID:     rosString(r[".id"]),
			Master: rosBool(r["master"]),
			// Which radio a virtual AP rides on. Needed because taking a radio off
			// the air takes every SSID on it down too, and the clients are almost
			// never on the radio's own interface.
			MasterInterface: rosString(r["master-interface"]),
			// A PRESENCE TEST ON A NAME, not a boolean — `!!r['configuration.manager']`.
			// An empty string is not managed. The key is dotted, which is easy to
			// lose to a naive struct mapping.
			CapsmanManaged: rosString(r["configuration.manager"]) != "",
			Disabled:       rosBool(r["disabled"]),
			Running:        rosBool(r["running"]),
		})
	}
	return out
}

// rosBool is `x === 'true' || x === true`.
//
// RouterOS answers the STRING "true", so the string arm is the one that fires in
// production; the boolean arm is for a fixture replayed through JSON. Reading
// this as a plain truthiness test would make the string "false" true — every
// radio would read as disabled and the dialog would offer none of them.
func rosBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "true"
	}
	return false
}

func rosString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
