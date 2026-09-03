package safe

// Never send a raw error message to the browser.
//
// The live app states this as a hard constraint, in those words, at the one
// place a RouterOS connection error reaches a socket:
//
//	// No classifier matched → reason is still the raw message; sanitize before
//	// it reaches the browser (hard constraint: never send raw .message).
//	const safeReason = classified ? reason : sanitizeErr(e);
//
// This port had been storing `err.Error()` verbatim in `lastErr`, and `announce`
// puts that in `router:status` as `reason` — which the shell now renders as the
// whole banner text. A Go dial failure reads `dial tcp 10.0.0.2:8729: connect:
// connection refused`, so the router's management address and port were being
// shown to everyone with read access to that router, including a Read Only user
// with no business knowing it.
//
// ── THE ORDER IS THE ORIGINAL'S, AND IT IS NOT LOAD-BEARING ─────────────────
//
// Paths go first because that is the order upstream writes them in, and the
// order is kept for that reason alone. An earlier version of this comment
// claimed it mattered — that running the address rule first would leave
// `https://[addr]/rest` where paths-first gives `https:[path]`. That is wrong,
// and swapping the two lines is a mutation no case can catch: `[addr]` contains
// neither whitespace nor a quote, so any path match that would have swallowed
// the address swallows the placeholder instead and both orders converge. Checked
// against all 41 corpus inputs plus ten hand-built URL and CIDR shapes.
//
// Recorded rather than quietly corrected, because the claim read as a reason and
// would have been believed by the next person to touch this.
//
// ── THE LIMIT IS IN UTF-16 CODE UNITS ───────────────────────────────────────
//
// `String.prototype.slice` counts them, so a message padded with astral
// characters cuts at a different point than a byte or rune count would give.
// Same reasoning as `cut` in internal/store: matching the original matters more
// than the unit being the natural one for Go.

import (
	"regexp"
	"unicode/utf16"
)

var (
	// A slash and at least two characters that are not whitespace or a quote.
	sanPath = regexp.MustCompile(`/[^\s'"]{2,}`)
	// Dotted quad, optionally with a port. Deliberately loose about the octet
	// range, exactly as the original is: this is redaction, not validation, and
	// `999.1.1.1` in an error message is still worth hiding.
	sanAddr = regexp.MustCompile(`\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}(:\d+)?`)
	sanMail = regexp.MustCompile(`[\w.+-]+@[\w.-]+\.\w+`)
	// A Telegram bot token: a long numeric id, a colon, then the secret.
	// A HOSTNAME. AFTER the email rule, and the ordering is load-bearing: run
	// first, this half-matches an address into `user@[host]`, which still names
	// the domain. The live chain is path, addr, email, host, token and this
	// matches it position for position.
	//
	// ── WHY THE IPv4 RULE ABOVE WAS NOT ENOUGH ────────────────────────────
	//
	// The address form was closed from the start, which is exactly what made the
	// gap easy to miss — the obvious probe was already blocked. A hostname inside
	// a URL is caught incidentally by `sanPath`. What survived is the BARE name in
	// a resolver error, which has no leading slash:
	// "getaddrinfo ENOTFOUND build-server.internal".
	//
	// It matters because the per-user test-notification route is deliberately NOT
	// admin-gated — correctly, since it manages the caller's own channels — and
	// per-user ntfy lets that user choose the destination URL. An ordinary account
	// could enter an internal hostname, press Test, and read back whether the name
	// resolves: a resolvable one gives a connection error, an unresolvable one "no
	// such host". Distinguishable, and therefore a name oracle for the server's
	// DNS view.
	//
	// Upstream `51aac86`, reported from this port.
	sanHost  = regexp.MustCompile(`(?i)\b(?:[a-z0-9-]+\.)+[a-z]{2,}\b`)
	sanToken = regexp.MustCompile(`\b\d{6,}:[A-Za-z0-9_-]{20,}\b`)
	// A Go format verb that could not be rendered — `%!w(<nil>)` and friends —
	// together with the `; close ` that precedes the one go-routeros emits. See
	// Message for the whole story.
	sanFmtVerb = regexp.MustCompile(`(;\s*close\s*)?%![a-zA-Z]\([^)]*\)`)
)

// Message redacts anything identifying from an error bound for a browser.
//
// Exported and in its own package because TWO routes reach a browser with error
// text and they are not near each other: the session's `lastErr`, which becomes
// the connection banner, and the report endpoints' 500s, which carry SQLite
// errors — and a SQLite error names the database FILE. The live app has no
// try/catch on those endpoints at all, so it never sends one; this port does,
// which is a disclosure the live app cannot make.
func Message(msg string) string {
	if msg == "" {
		return ""
	}
	// ── AN UPSTREAM FORMATTING BUG, STRIPPED BEFORE IT IS SHOWN ──────────
	//
	// go-routeros v3.0.1 builds its login failure as
	//
	//	fmt.Errorf("could not login: %w; close %w", err, c.Close())   client.go:128
	//
	// and `Close()` returns nil on a clean close, so `%w` renders the literal
	// `%!w(<nil>)`. The operator on issue #126 was shown
	//
	//	could not login: from RouterOS device: invalid user name or password
	//	(6); close %!w(<nil>)
	//
	// across the top of the page. The useful half of that sentence is the half
	// they need; the tail is Go telling itself off, and it makes a clear message
	// look like a crash.
	//
	// Removed HERE because this is the one chokepoint every error crosses on its
	// way to a browser, and because the fix belongs upstream: patching the
	// driver's string here would be a fork, while dropping a known-empty
	// fragment is presentation. If a future driver stops emitting it this stops
	// matching and changes nothing.
	msg = sanFmtVerb.ReplaceAllString(msg, "")
	msg = sanPath.ReplaceAllString(msg, "[path]")
	msg = sanAddr.ReplaceAllString(msg, "[addr]")
	msg = sanMail.ReplaceAllString(msg, "[email]")
	msg = sanHost.ReplaceAllString(msg, "[host]")
	msg = sanToken.ReplaceAllString(msg, "[token]")
	u := utf16.Encode([]rune(msg))
	if len(u) > 200 {
		msg = string(utf16.Decode(u[:200]))
	}
	return msg
}
