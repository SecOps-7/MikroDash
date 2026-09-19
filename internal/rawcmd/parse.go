// Package rawcmd parses ONE RouterOS command written in CLI form.
//
// It exists for the assistant's raw command tools, which send a command the
// model composed rather than a row the resource registry describes. Everything
// else in this app reaches a router through a declared menu and a validated
// field; this is the one path that does not, so the parser is an ALLOW-LIST of
// shapes and refuses anything it does not recognise rather than passing it
// through and hoping the router refuses it.
//
// ── WHAT IT ACCEPTS ─────────────────────────────────────────────────────────
//
//	/ip/address/print
//	/ip/firewall/filter/add chain=input action=accept comment="from the office"
//	/interface/set .id=*3 disabled=no
//
// A menu path, a verb, and `name=value` words. That is all.
//
// ── WHAT IT REFUSES, AND WHY EACH ONE ───────────────────────────────────────
//
//	a newline or a carriage return   two commands in one string
//	;                                the CLI's command separator
//	a leading : or a :word           the scripting language (:do, :execute, :put)
//	[ ]                              command substitution — [find], [/system…]
//	{ }                              a script block
//	$                                a variable reference
//	`                                nothing in RouterOS uses it; it is shell habit
//	\                                escaping, which only matters if a quote does
//	a control character              anything that cannot be typed is not typed
//
// The refusals are on the WHOLE string, quoted values included. A comment
// containing a semicolon is a legitimate thing to want and is refused here
// anyway: the cost is an operator who has to write that comment on the page, and
// the alternative is a parser that decides what is inside a quote and what is
// not, which is the class of decision that gets this wrong.
//
// ── IT DOES NOT DECIDE POLICY ───────────────────────────────────────────────
//
// Who may run a command, whether the setting allows it at all, and whether it
// needs confirming are the executor's questions. This answers one: is this a
// single, well-formed RouterOS command, and what are its parts.
package rawcmd

import (
	"fmt"
	"regexp"
	"strings"

	"mikrodash/internal/audit"
)

// Command is one parsed command.
type Command struct {
	// Menu is the RouterOS menu, with no trailing verb: "/ip/address".
	Menu string
	// Verb is the word after the menu: "print", "add", "set".
	Verb string
	// Words are the arguments in API form, "=name=value", in the order given.
	Words []string
	// Text is the command as it will be recorded in the audit trail: the parts,
	// rebuilt, rather than whatever the model typed.
	Text string
}

// APIPath is what the RouterOS API expects: the menu and the verb.
func (c Command) APIPath() string { return c.Menu + "/" + c.Verb }

var (
	segmentRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	verbRe    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	nameRe    = regexp.MustCompile(`^\.?[a-z][a-z0-9.-]*$`)
)

// verbs is the allow-list of commands this parser will build.
//
// ── A LIST, BECAUSE THE SHAPE CANNOT DECIDE ─────────────────────────────────
//
// `/user/print` and `/ip/address` are both a slash-separated pair: nothing in
// the text says which word is a menu and which is a verb. Without a list, the
// second parses as the menu `/ip` with the verb `address`, and the operator is
// asked to confirm a command that was never a command.
//
// It is also the narrower gate. Every verb here is one a MikroDash page already
// issues or one that only reads; RouterOS has others (`reset-configuration`,
// `netinstall`) that nothing in this app sends and that nobody should reach
// through a chat window. A verb missing from this list is refused, not passed on
// for the router to judge.
var verbs = map[string]bool{
	// Reads.
	"print": true, "get": true, "export": true, "monitor": true,
	// Ordinary writes, the ones the resource engine issues.
	"add": true, "set": true, "remove": true, "enable": true, "disable": true,
	"move": true, "comment": true, "unset": true,
	// The page actions, which `run_action` also offers.
	"renew": true, "release": true, "apply-changes": true, "unschedule": true,
	"upgrade": true, "reset-counters": true, "downgrade": true, "check-for-updates": true,
}

// readVerbs are the verbs that only READ. The executor treats the rest as
// changes; it confirms both, because a raw read can reach menus the page
// permission matrix never mapped.
var readVerbs = map[string]bool{
	"print": true, "get": true, "export": true, "monitor": true,
}

// IsReadVerb reports whether a verb only reads.
func IsReadVerb(verb string) bool { return readVerbs[verb] }

// forbidden is checked against the whole string, quoted values included.
var forbidden = []struct {
	sub    string
	reason string
}{
	{"\n", "a newline, which would be two commands"},
	{"\r", "a carriage return, which would be two commands"},
	{";", "a semicolon, which the CLI reads as a command separator"},
	{"[", "a bracket, which the CLI reads as command substitution"},
	{"]", "a bracket, which the CLI reads as command substitution"},
	{"{", "a brace, which the CLI reads as a script block"},
	{"}", "a brace, which the CLI reads as a script block"},
	{"$", "a dollar, which the CLI reads as a variable"},
	{"`", "a backtick"},
	{"\\", "a backslash"},
}

// Parse reads one command, or says why it will not.
//
// The error is written for the model to relay to a person: it names what was
// refused rather than restating the input, which would put a crafted string back
// in front of whoever reads the transcript.
func Parse(in string) (Command, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return Command{}, fmt.Errorf("no command was given")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return Command{}, fmt.Errorf("the command contains a control character")
		}
	}
	for _, f := range forbidden {
		if strings.Contains(s, f.sub) {
			return Command{}, fmt.Errorf("the command contains %s", f.reason)
		}
	}
	if strings.HasPrefix(s, ":") {
		return Command{}, fmt.Errorf("that is a script command, not a menu command")
	}
	if !strings.HasPrefix(s, "/") {
		return Command{}, fmt.Errorf("a command must start with a menu path, such as /ip/address/print")
	}

	fields, err := split(s)
	if err != nil {
		return Command{}, err
	}
	head := fields[0]
	parts := strings.Split(strings.TrimPrefix(head, "/"), "/")
	if len(parts) < 2 {
		return Command{}, fmt.Errorf("a command needs a menu and a verb, such as /ip/address/print")
	}
	verb := parts[len(parts)-1]
	segments := parts[:len(parts)-1]
	if !verbRe.MatchString(verb) || !verbs[verb] {
		return Command{}, fmt.Errorf("%q is not a command this app will send; the command must "+
			"end in a verb such as print, add, set or remove", cut(verb))
	}
	for _, seg := range segments {
		if !segmentRe.MatchString(seg) {
			return Command{}, fmt.Errorf("%q is not part of a menu path", seg)
		}
	}

	words := make([]string, 0, len(fields)-1)
	shown := make([]string, 0, len(fields))
	shown = append(shown, "/"+strings.Join(segments, "/")+"/"+verb)
	for _, f := range fields[1:] {
		eq := strings.Index(f, "=")
		if eq <= 0 {
			// A BARE WORD IS REFUSED, and that includes the CLI's positional
			// row numbers: `set 0 disabled=yes` addresses whatever is first in
			// a list that another session can reorder. The API form is `.id=`,
			// which names one row.
			return Command{}, fmt.Errorf("every argument must be name=value; %q is not", cut(f))
		}
		name, value := f[:eq], f[eq+1:]
		if !nameRe.MatchString(name) {
			return Command{}, fmt.Errorf("%q is not an argument name", cut(name))
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		if strings.Contains(value, `"`) {
			return Command{}, fmt.Errorf("the value of %q has a quote inside it", name)
		}
		words = append(words, "="+name+"="+value)
		if Sensitive(name) {
			shown = append(shown, name+"="+audit.Set)
		} else {
			shown = append(shown, name+"="+value)
		}
	}
	return Command{
		Menu: "/" + strings.Join(segments, "/"), Verb: verb,
		Words: words, Text: strings.Join(shown, " "),
	}, nil
}

// split breaks the command into fields, keeping a double-quoted value whole.
//
// Quotes are the ONE piece of syntax honoured here, because a comment or an SSID
// with a space in it is ordinary. They must wrap a whole value: a quote that
// opens and never closes is an error rather than a value that runs to the end.
func split(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			cur.WriteRune(r)
		case r == ' ' && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if inQuote {
		return nil, fmt.Errorf("the command has an unclosed quote")
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no command was given")
	}
	return out, nil
}

// cut bounds what an error message repeats back.
func cut(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}

// sensitiveRe is RouterOS's own list of sensitive parameters ("List of menus
// with sensitive parameters", help.mikrotik.com): a name that is, or ends in
// `-`, password, passphrase, secret(s), key, key-N, key-val, pin or cak. That
// covers private-key, preshared-key, wpa2-pre-shared-key, tcp-md5-key,
// static-key-0, sim-pin and the rest of the list without naming each.
//
// NOT audit.IsCredentialField, which is broad on purpose and matches `pass`:
// masking mangle's `passthrough` in a print would hide real configuration
// from the model. ZeroTier's `identity` is left out for the same reason; the
// word is ordinary everywhere else.
var sensitiveRe = regexp.MustCompile(`(^|-)(password|passphrase|secrets?|key|key-[0-9]|key-val|pin|cak)$`)

// Sensitive reports whether a RouterOS property's value must never be shown:
// in a command's Text (the dialog, the audit trail, the model) or in a raw
// reply. A public key is public.
func Sensitive(name string) bool {
	return name != "public-key" && sensitiveRe.MatchString(name)
}
