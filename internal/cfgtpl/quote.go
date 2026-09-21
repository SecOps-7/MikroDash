package cfgtpl

import (
	"fmt"
	"regexp"
	"strings"
)

// QuoteROS writes s as ONE RouterOS string literal. It is the only way a value
// becomes router text, which is what makes the rest of this package safe to be
// wrong in: whatever a value holds, it leaves as an inert string.
//
// ── THE ESCAPES, CHECKED AGAINST MIKROTIK'S OWN LIST ────────────────────────
//
// The scripting manual's "Constant Escape Sequences" lists exactly:
//
//	\"  \\  \n  \r  \t  \$  \_  \a  \b  \f  \v  and \XX, "hex numbers should
//	use capital letters"
//
// `\?` IS NOT ON IT. Both designs this was built from assumed it was, and
// emitting an escape RouterOS does not define is precisely the kind of guess
// this function exists to not make. So:
//
//	\  "      backslash-escaped, as documented
//	$         `\$`, the documented way — otherwise it expands a variable, and
//	          `$[…]` runs a command
//	?         hex: it is special in the console and has no documented escape
//	[ ]       hex: inert inside a string without `$`, escaped anyway — the cost
//	          is nothing and it removes a question
//	{ }       hex: so a literal `{{` can never read back as a placeholder
//	controls, DEL and every byte of non-ASCII text
//	          hex, upper case, one byte at a time, as `/export` itself writes
//	          non-ASCII
//
// The output is printable ASCII in which nothing can end the literal early.
func QuoteROS(s string) string { return `"` + escapeInner(s) + `"` }

// escapeInner is QuoteROS without the surrounding quotes, for Format's mixed
// values, where a placeholder sits between escaped literal parts.
func escapeInner(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' || c == '"' || c == '$':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 0x20 || c >= 0x7f || c == '?' || c == '[' || c == ']' || c == '{' || c == '}':
			fmt.Fprintf(&b, "\\%02X", c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// bareSafe is the shape a value may be written in WITHOUT quotes.
//
// Deliberately narrow: no whitespace, no quote, no `$ [ ] { } \ ; # = ?`, no
// backtick, nothing non-ASCII. `!` is in it because RouterOS negates with it
// (`in-interface-list=!LAN`), and whether a quoted `"!LAN"` still negates is
// not something to find out on a customer's firewall. `,` for lists, `:` for
// IPv6, MACs and times, `/` for prefixes, `*` for ids.
//
// Anything outside it is quoted, which is always correct; this only decides
// when the shorter form is ALSO correct.
var bareSafe = regexp.MustCompile(`^[A-Za-z0-9._,:/@+*!%-]+$`)

// emitLiteral writes a finished value: bare when that is unambiguous, quoted
// otherwise. An empty value is `""`, never an empty word.
func emitLiteral(s string) string {
	if bareSafe.MatchString(s) {
		return s
	}
	return QuoteROS(s)
}
