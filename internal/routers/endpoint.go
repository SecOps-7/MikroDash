package routers

// `sameEndpoint` — whether a stored router password may be reused for a
// connection test.
//
// ── THE WHOLE SECURITY PROPERTY OF /api/routers/test ────────────────────────
//
// The live comment names the attack: "A bare 'look it up by id' turns this
// route into a credential oracle: submit a stored id with an attacker-chosen
// host and the server posts the saved password to it."
//
// So the stored secret is reused only when every field deciding WHERE it goes
// and HOW it travels is unchanged — host, port and username for the first;
// TLS and its certificate check for the second. `tlsInsecure` matters as much
// as the host: turning it on accepts any certificate, which makes the same
// hostname a man-in-the-middle.
//
// Being a global admin gates the route and is explicitly NOT sufficient. The
// point is to stop a stored secret reaching a destination nobody stored it
// against, INCLUDING at the hands of an admin.
//
// NOTHING HERE READS A PASSWORD. This decides only whether the caller may.

import "strings"

// Endpoint is the five fields that decide whether two records name the same
// destination reached the same way. NOTHING ELSE BELONGS HERE — a label or a
// site does not change where a password goes, and including one would make
// admins retype after a rename.
//
// The three that arrive as JSON keep their raw form, because their COERCION is
// part of the rule rather than a detail of parsing: see each helper below.
type Endpoint struct {
	Host     string
	Port     any // number, numeric string, or absent
	Username string
	TLS      any // bool, "true"/"false", or absent
	Insecure any // bool, "true", or absent
}

// SameEndpoint reports whether `b` names the same destination as `a`, reached
// the same way.
//
// A NIL RECORD NEVER MATCHES, including two nils — `if (!a || !b) return false`.
func SameEndpoint(a, b *Endpoint) bool {
	if a == nil || b == nil {
		return false
	}
	// AN EMPTY HOST NEVER MATCHES, not even another empty host. Without this
	// two half-filled records compare equal and a password is reused against
	// nothing in particular.
	if endpointHost(a.Host) == "" {
		return false
	}
	return endpointHost(a.Host) == endpointHost(b.Host) &&
		endpointPort(a.Port) == endpointPort(b.Port) &&
		endpointUser(a.Username) == endpointUser(b.Username) &&
		endpointTLS(a.TLS) == endpointTLS(b.TLS) &&
		endpointInsecure(a.Insecure) == endpointInsecure(b.Insecure)
}

// endpointHost is `String(r.host || ”).trim().toLowerCase()`.
//
// TRIMMED AND LOWERCASED, because DNS is case-insensitive and two records
// differing only in case name the same machine. Refusing them would make an
// admin retype a password for no reason.
func endpointHost(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

// endpointUser is `String(r.username || ”).trim()`.
//
// TRIMMED BUT NOT LOWERCASED, and the asymmetry with the host is deliberate:
// RouterOS logins ARE case-sensitive, so `Admin` is a different account from
// `admin` and the stored password must not follow. A stray space in a form
// field is not a different user.
func endpointUser(v string) string { return strings.TrimSpace(v) }

// endpointPort is `parseInt(r.port || '8729', 10)`.
//
// THE DEFAULT IS REACHED BY FALSINESS, so 0, "" and absent all become 8729 —
// not only absent. A port comparing a literal zero against the default would
// refuse a record the live app accepts.
func endpointPort(v any) int {
	switch x := v.(type) {
	case nil:
		return 8729
	case int:
		if x == 0 {
			return 8729
		}
		return x
	case float64: // every JSON number decodes to this
		if x == 0 {
			return 8729
		}
		return int(x)
	case string:
		if x == "" {
			return 8729
		}
		return jsParseInt(x, 8729)
	default:
		return 8729
	}
}

// endpointTLS is `r.tls !== false && r.tls !== 'false'`.
//
// DEFAULTS ON, and only two values turn it off: the boolean and the STRING.
// Both are checked explicitly upstream — which is exactly what
// `endpointInsecure` below does NOT do.
func endpointTLS(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "false"
	default:
		return true
	}
}

// endpointInsecure is `r.tlsInsecure === true || r.tlsInsecure === 'true'`.
//
// EXPLICIT, like endpointTLS above, and for the same reason: a form field can
// arrive as the STRING "false", and JavaScript truthiness makes any non-empty
// string true.
//
// ── THIS WAS THE OTHER WAY ROUND UNTIL 2026-08-27 ───────────────────────────
//
// The live helper was `!!(r.tlsInsecure || r.tlsInsecure === 'true')`, so
// `"false"` satisfied the first test and never reached the second: a record
// saying certificate checking is ON read as OFF — the strictest setting turned
// laxest by coercion, inside the one function deciding whether a stored
// credential may be reused.
//
// It failed CLOSED, which is why it survived: the two sides then disagreed, the
// stored password was refused, and the admin was asked to retype it. Wrong in a
// safe direction is still wrong. Found by this port, filed in `ToDo.md`, and
// FIXED UPSTREAM in 2af8164 — this side follows rather than keeping the quirk,
// because the quirk is gone and `tools/same-endpoint-cases.js` regenerates from
// the live implementation.
//
// A bare `bool` is all this ever needs from a stored record; the `any` is for
// the request, where the string forms arrive.
func endpointInsecure(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		// ONLY the exact word. "false", "1", "yes" and "" are all OFF — the live
		// test is `=== 'true'`, not truthiness, and a port widening it would
		// re-open the defect from the other side.
		return x == "true"
	default:
		return false
	}
}

// jsParseInt is `parseInt(s, 10)`: leading digits win, and anything with no
// leading digit falls back.
//
// `parseInt("8728abc")` is 8728, not an error — a port using a strict integer
// parse would fall back to the default and compare two different ports as
// equal.
func jsParseInt(s string, fallback int) int {
	s = strings.TrimSpace(s)
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	start := i
	n := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		n = n*10 + int(s[i]-'0')
		i++
	}
	if i == start {
		return fallback
	}
	if neg {
		return -n
	}
	return n
}

// The three coercions, exported for the one caller that must dial with the
// values `sameEndpoint` compared.
//
// ── WHY THEY ARE EXPORTED RATHER THAN RE-DERIVED AT THE CALL SITE ───────────
//
// `POST /api/routers/test` decides two things from the same request: whether a
// stored password may be reused (SameEndpoint) and where to dial. If those two
// coerced the fields independently they could disagree — the port would compare
// port 8729 and dial port 0, or compare TLS on and dial it off — and the
// disagreement would be silent, because each half is individually correct.
//
// One coercion, used twice. `EndpointInsecure` in particular MUST be the same
// function: it is the one whose live behaviour is surprising (the string
// "false" turns the certificate check ON, see below), and a caller writing the
// obvious thing at the dial site would send the password over a connection the
// match had approved for a different certificate policy.
func EndpointPort(v any) int      { return endpointPort(v) }
func EndpointTLS(v any) bool      { return endpointTLS(v) }
func EndpointInsecure(v any) bool { return endpointInsecure(v) }
