package store

// The Terminal page's install-wide switch.
//
// ── WHY THIS IS NOT `aiAllowRawCommands` ────────────────────────────────────
//
// Both switches turn on a path that leaves the resource registry, and it is
// tempting to make one serve both. They answer different questions. The AI one
// asks "may a MODEL compose a RouterOS command"; this one asks "may an OPERATOR
// type one". An install can reasonably want the second without the first - a
// console for the person at the keyboard, and nothing autonomous - and wanting
// the first without the second is just as coherent. One key could not express
// either preference, and an operator switching on a terminal would silently be
// switching on something else.
//
// ── ABSENT MEANS FALSE ──────────────────────────────────────────────────────
//
// Same reasoning as store.AIAllowRawCommands, and more so: the Terminal page
// does not even parse what it sends. The zero value of a missing key is the
// right one, so an install that upgrades into this feature has it switched off
// and somebody has to decide to turn it on.
//
// This is ONE of three gates, and the weakest of them, because it is a property
// of the install rather than of the person. The other two are per call: the
// caller must be a signed-in global administrator, and must hold write access
// to the `terminal` page. See internal/server/terminal.go, which is where all
// three are checked together and where the fourth - the RouterOS user's own
// policy, enforced by the router - is written down.
func TerminalEnabled(s Settings) bool {
	v, _ := s["terminalEnabled"].(bool)
	return v
}

// TerminalReadyKey is the derived key the browser reads to decide whether to
// show the Terminal nav item at all. It has NO default and is never stored:
// `Merge` drops any key that is neither a default nor encrypted.
//
// It carries the INSTALL-WIDE half of the gate only. Whether the viewer reading
// it may actually run anything is per-person and arrives on `term:caps`, the
// way the Tools page's `mayWrite` does - a payload the server computes for one
// connection, which a settings projection cannot.
const TerminalReadyKey = "terminalReady"

// WithTerminalReady returns a copy carrying the derived flag, for projection
// through PageSettings. A COPY: writing a derived key into the live merged
// settings would put a key with no default into the map the save path later
// writes, and `Merge` would drop it on the next read. See WithAIReady, which
// this mirrors deliberately rather than inventing a second shape.
func WithTerminalReady(s Settings) Settings {
	out := make(Settings, len(s))
	for k, v := range s {
		out[k] = v
	}
	out[TerminalReadyKey] = TerminalEnabled(s)
	return out
}
