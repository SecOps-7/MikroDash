package oidc

// Where to send the browser after a sign-in.
//
// ── TWO IMPLEMENTATIONS OF ONE RULE ─────────────────────────────────────────
//
// `safeNext()` in web/src/entry/login.ts:71 decides where the login PAGE
// navigates. This one decides what goes in a Location header. They must agree,
// and "they looked the same when I wrote them" is not a mechanism - so both are
// driven from testdata/safenext-cases.json.
//
// IT MIRRORS THE BROWSER'S RULE RATHER THAN IMPROVING ON IT. The live check
// rejects control characters below 0x20 and not 0x7f; this does the same. A
// stricter server would disagree with the page about a link the page then
// accepts, and a disagreement between two guards is how one of them quietly
// becomes the only one.
//
// ── WHY THE SERVER CHECKS AT ALL, WHEN THE PAGE ALREADY DOES ───────────────
//
// The browser is not an enforcement point. `loginFor` in internal/server needs
// no such check because its `next` is the server's own r.URL.Path, but this one
// arrives from a query string and ends up in a Location header. Emitting a
// redirect this process would refuse to accept is exactly how two guards become
// one.

import (
	"net/url"
	"strings"
)

// safeNextBase is the origin every candidate is resolved against.
//
// Its scheme and host are arbitrary and never appear in the result: they exist
// so that ResolveReference behaves as `new URL(raw, window.location.origin)`
// does, which is what makes a bare relative path resolve rather than be refused.
var safeNextBase = &url.URL{Scheme: "http", Host: "mikrodash.invalid", Path: "/"}

// safeNext returns a same-origin path to redirect to, or "/".
//
// There is no error path, for the reason the live comment gives: a bad `next` is
// not worth a message, and saying "that redirect looked hostile" tells whoever
// sent the link that the check exists.
func safeNext(raw string) string {
	const home = "/"
	if raw == "" {
		return home
	}
	// 1. A control character is refused before anything parses it. net/http
	// happens to reject a header containing a newline, and relying on a
	// library's incidental behaviour for a response-splitting defence is how it
	// stops being one.
	if strings.ContainsFunc(raw, func(r rune) bool { return r < 0x20 }) {
		return home
	}
	// 2. A LEADING "//" OR "/\" IS PROTOCOL-RELATIVE: a browser reads both as
	// another origin. Checked on the RAW value rather than on the parsed path,
	// because Go's url.Parse percent-encodes the backslash to %5C and a check
	// on the parsed form never sees it. Found by the shared corpus on its first
	// run, which is what the corpus is for.
	if strings.HasPrefix(raw, "//") || strings.HasPrefix(raw, `/\`) {
		return home
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return home
	}
	// 3. A SCHEME OR AN AUTHORITY IS REFUSED OUTRIGHT rather than resolved and
	// compared. Comparing would mean naming an origin here, and the sentinel
	// host below would then be a string an attacker could simply type - which
	// the corpus also caught.
	//
	// THIS IS STRICTER THAN THE BROWSER'S TWIN, deliberately and in the safe
	// direction: the page accepts an absolute URL that happens to match its own
	// origin, and this refuses it. Nothing in the app emits one - `loginFor`
	// builds a path - so the divergence is unreachable in practice. Recorded
	// rather than left for somebody to rediscover.
	if ref.Scheme != "" || ref.Host != "" || ref.Opaque != "" {
		return home
	}
	// Resolved against an absolute base so a bare relative path becomes one, as
	// `new URL(raw, origin)` does in the browser. The base can never be
	// targeted: anything carrying a host was refused above.
	u := safeNextBase.ResolveReference(ref)
	// 4. Only path + query + fragment is returned - never the parsed URL - so
	// nothing can smuggle credentials or a port through.
	p := u.EscapedPath()
	if u.RawQuery != "" {
		p += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		p += "#" + u.EscapedFragment()
	}
	// 5. And the RESULT must still be a path, after all of that.
	if !strings.HasPrefix(p, "/") {
		return home
	}
	if len(p) > 1 && (p[1] == '/' || p[1] == '\\') {
		return home
	}
	return p
}
