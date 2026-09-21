package cfgtpl

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ParseError is a refusal, and where it happened.
type ParseError struct {
	Line int
	Msg  string
}

func (e *ParseError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

var (
	menuWord = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	argName  = regexp.MustCompile(`^[a-z][a-z0-9.-]*$`)
	varName  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	wholeVar = regexp.MustCompile(`^\{\{([a-z][a-z0-9_]{0,31})\}\}$`)
)

// verbs is every command a template may use. `ensure` is MikroDash's own: add
// the row if no row matches all its arguments. It never reaches a router —
// Render refuses it — because it is resolved against the router's live rows
// first, into an `add` or into nothing.
var verbs = map[string]bool{
	"add": true, "set": true, "remove": true, "enable": true,
	"disable": true, "unset": true, "ensure": true,
}

// selectorVerbs may carry a `[ find … ]`.
var selectorVerbs = map[string]bool{
	"set": true, "remove": true, "enable": true, "disable": true, "unset": true,
}

type logical struct {
	text string
	line int
}

// Parse reads a template or an export.
//
// ── ONE-LINERS DO NOT MOVE THE CURRENT MENU ─────────────────────────────────
//
// A line that is only a path (`/ip firewall filter`) changes the menu that
// later relative lines (`add …`) run in. A line with a path AND a command
// (`/system identity set name=edge`) runs in that path and leaves the current
// menu where it was, which is how the RouterOS console behaves: the prompt does
// not move for an absolute command. It cannot make a router do something this
// parse did not read, because what is sent is Format's output, which writes an
// explicit header before every change of menu.
func Parse(src string) (*Template, error) {
	if !utf8.ValidString(src) {
		return nil, &ParseError{1, "the text is not valid UTF-8"}
	}
	src = strings.ReplaceAll(src, "\r\n", "\n")
	lines, err := join(src)
	if err != nil {
		return nil, err
	}
	t := &Template{}
	var menu []string
	for _, lg := range lines {
		l, m, err := parseLine(lg, menu)
		if err != nil {
			return nil, err
		}
		menu = m
		if l != nil {
			t.Lines = append(t.Lines, *l)
		}
	}
	return t, nil
}

// join turns physical lines into logical ones, following the scripting manual's
// "Line joining" and "Comments" rules exactly:
//
//   - a backslash at the end of a line joins it to the next;
//   - INSIDE a string literal it continues the token, and the next line's
//     indent is dropped: `!reboo\` + `    t` is `!reboot` (measured, from a
//     test router's own export);
//   - OUTSIDE one it "does not continue a token": `a=1\` + `b=2` is TWO words,
//     so it becomes a space, never nothing;
//   - `#` outside a string, at the start of a word, begins a comment that runs
//     to the end of its physical line and "cannot continue".
func join(src string) ([]logical, error) {
	var (
		out            []logical
		b              strings.Builder
		comment        strings.Builder
		start, line    = 1, 1
		inQ, inComment bool
		last           byte = ' '
	)
	flush := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			out = append(out, logical{s, start})
		}
		b.Reset()
		last = ' '
	}
	put := func(c byte) { b.WriteByte(c); last = c }
	endComment := func() error {
		if strings.HasSuffix(strings.TrimRight(comment.String(), " \t"), `\`) {
			return &ParseError{line, "a comment cannot continue onto the next line"}
		}
		comment.Reset()
		inComment = false
		return nil
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inComment {
			if c == '\n' {
				if err := endComment(); err != nil {
					return nil, err
				}
				flush()
				line++
				start = line
			} else {
				comment.WriteByte(c)
			}
			continue
		}
		switch {
		case c == '\\' && i+1 < len(src) && src[i+1] == '\n':
			i++
			line++
			for i+1 < len(src) && (src[i+1] == ' ' || src[i+1] == '\t') {
				i++
			}
			if !inQ {
				put(' ')
			}
		case c == '\\' && inQ:
			put(c)
			if i+1 < len(src) {
				i++
				put(src[i])
			}
		case c == '"':
			inQ = !inQ
			put(c)
		case c == '#' && !inQ && (last == ' ' || last == '\t'):
			inComment = true
		case c == '\n':
			if inQ {
				return nil, &ParseError{start, "a quoted value is not closed before the end of the line"}
			}
			flush()
			line++
			start = line
		default:
			put(c)
		}
	}
	if inQ {
		return nil, &ParseError{start, "a quoted value is not closed"}
	}
	if inComment {
		if err := endComment(); err != nil {
			return nil, err
		}
	}
	flush()
	return out, nil
}

// parseLine reads one logical line. It returns the command (nil for a header)
// and the menu relative lines will use after it.
func parseLine(lg logical, cur []string) (*Line, []string, error) {
	fail := func(f string, a ...any) (*Line, []string, error) {
		return nil, nil, &ParseError{lg.line, fmt.Sprintf(f, a...)}
	}
	if strings.HasPrefix(lg.text, ":") {
		w, _, _ := strings.Cut(lg.text, " ")
		return fail("RouterOS scripting (%s) is not allowed in a template", w)
	}
	toks, err := tokenize(lg.text)
	if err != nil {
		return fail("%s", err)
	}
	i, menu, verb := 0, cur, ""
	if strings.HasPrefix(toks[0].s, "/") {
		menu = nil
		for _, w := range strings.Split(toks[0].s[1:], "/") {
			if w != "" {
				menu = append(menu, w)
			}
		}
		i = 1
		for i < len(toks) && !toks[i].bracket && !strings.Contains(toks[i].s, "=") &&
			!verbs[toks[i].s] && menuWord.MatchString(toks[i].s) {
			menu = append(menu, toks[i].s)
			i++
		}
		// The slash form puts the verb in the path: /ip/firewall/filter/add.
		if n := len(menu); n > 0 && verbs[menu[n-1]] {
			verb, menu = menu[n-1], menu[:n-1]
		}
		if len(menu) == 0 {
			return fail("the menu path is empty")
		}
		for _, w := range menu {
			if !menuWord.MatchString(w) {
				return fail("%q is not a menu name", w)
			}
		}
	} else if cur == nil {
		return fail("a command comes before any menu: start with a path such as /ip firewall filter")
	}
	if verb == "" {
		if i == len(toks) {
			// A header: it moves the current menu, and is not a command.
			return nil, menu, nil
		}
		v := toks[i].s
		if !verbs[v] {
			return fail("%q is not a command a template may use "+
				"(add, set, remove, enable, disable, unset, ensure)", v)
		}
		verb = v
		i++
	}
	l := &Line{Num: lg.line, Menu: append([]string(nil), menu...), Verb: verb}
	if i < len(toks) && toks[i].bracket {
		if !selectorVerbs[verb] {
			return fail("a [ find ] selector only goes with set, remove, enable, disable or unset")
		}
		find, err := parseFind(toks[i].s)
		if err != nil {
			return fail("%s", err)
		}
		l.HasFind, l.Find = true, find
		i++
	}
	args, pos, err := readArgs(toks[i:], true)
	if err != nil {
		return fail("%s", err)
	}
	l.Args, l.Pos = args, pos
	// A command — with or without its own path — leaves the current menu alone.
	return l, cur, nil
}

// readArgs reads `name=value` words and, when allowed, positional values.
//
// ── `name=` FOLLOWED BY WHITESPACE TAKES THE NEXT WORD AS ITS VALUE ─────────
//
// A real export wraps long lines AFTER the `=`:
//
//	add chain=input connection-state=\
//	    established,related,untracked
//
// Outside a string the continuation is a word break (join), so this arrives as
// `connection-state= established,…`. RouterOS's own exports only round-trip
// because its reader gives the next word to the `=` — and the manual shows
// whitespace after `=` as correct syntax (`from= 1`). Reading it as an empty
// value and a stray positional was this package's first bug, caught by writing
// the test on the real shape before any test ran.
//
// The one case that stays ambiguous is refused: `a= b=c` — is `b=c` a's value
// or a second argument? An export never writes it; it writes `a=""`.
func readArgs(toks []token, allowPos bool) ([]Arg, []Value, error) {
	var (
		args []Arg
		pos  []Value
	)
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.bracket {
			return nil, nil, errors.New("only one [ find … ] is allowed, directly after the command")
		}
		name, val, isArg := strings.Cut(t.s, "=")
		if !isArg || strings.HasPrefix(t.s, `"`) {
			if !allowPos {
				return nil, nil, fmt.Errorf("%q: expected name=value", t.s)
			}
			v, err := parseValue(t.s)
			if err != nil {
				return nil, nil, err
			}
			pos = append(pos, v)
			continue
		}
		if !argName.MatchString(name) {
			return nil, nil, fmt.Errorf("%q is not a property name", name)
		}
		if val == "" {
			if i+1 >= len(toks) {
				return nil, nil, fmt.Errorf(`%s= has no value; write %s="" for an empty one`, name, name)
			}
			next := toks[i+1]
			_, _, nextIsArg := strings.Cut(next.s, "=")
			if next.bracket || (nextIsArg && !strings.HasPrefix(next.s, `"`)) {
				return nil, nil, fmt.Errorf(`%s= is followed by another setting; write %s="" for an empty value`, name, name)
			}
			val = next.s
			i++
		}
		v, err := parseValue(val)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %s", name, err)
		}
		args = append(args, Arg{name, v})
	}
	return args, pos, nil
}

type token struct {
	s       string
	bracket bool
}

// tokenize splits a logical line into words, a quoted string staying whole,
// and refuses — outside quotes — everything that is not configuration.
func tokenize(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\t' {
			i++
			continue
		}
		if c == '[' {
			j, err := bracketEnd(s, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{s[i : j+1], true})
			i = j + 1
			continue
		}
		start, inQ := i, false
		for i < len(s) {
			c := s[i]
			if inQ {
				if c == '\\' {
					i += 2
					continue
				}
				if c == '"' {
					inQ = false
				}
				i++
				continue
			}
			if c == ' ' || c == '\t' {
				break
			}
			switch {
			case c == '"':
				inQ = true
			case c == ';':
				return nil, errors.New("';' joins two commands, which a template may not do")
			case c == '$':
				return nil, errors.New("'$' expands a variable; write it inside quotes as \\$")
			case c == '`':
				return nil, errors.New("a backtick has no meaning in RouterOS")
			case c == '\\':
				return nil, errors.New(`a backslash outside quotes is only allowed at the end of a line`)
			case c == '#':
				return nil, errors.New("a value containing '#' must be quoted")
			case c == '[' || c == ']':
				return nil, errors.New("[ ] may only open a line's [ find … ] selector")
			case c < 0x20 || c == 0x7f:
				return nil, errors.New("a control character outside quotes")
			case c >= 0x80:
				return nil, errors.New("text outside quotes must be plain ASCII; quote it")
			}
			i++
		}
		out = append(out, token{s[start:i], false})
	}
	if len(out) == 0 {
		return nil, errors.New("an empty line")
	}
	return out, nil
}

// bracketEnd finds the `]` closing the `[` at i.
func bracketEnd(s string, i int) (int, error) {
	inQ := false
	for j := i + 1; j < len(s); j++ {
		c := s[j]
		if inQ {
			if c == '\\' {
				j++
				continue
			}
			if c == '"' {
				inQ = false
			}
			continue
		}
		switch c {
		case '"':
			inQ = true
		case '[':
			return 0, errors.New("a [ ] inside a [ ] is command substitution, which a template may not use")
		case ']':
			return j, nil
		}
	}
	return 0, errors.New("a [ is not closed")
}

// parseFind reads `[ find name=value … ]`, and nothing else a bracket can hold.
func parseFind(s string) ([]Arg, error) {
	inner := strings.TrimSpace(s[1 : len(s)-1])
	if inner == "" {
		return nil, errors.New("an empty [ ]; a selector is [ find name=value ]")
	}
	toks, err := tokenize(inner)
	if err != nil {
		return nil, err
	}
	if toks[0].s != "find" {
		return nil, fmt.Errorf("only [ find … ] is allowed in brackets, not [ %s … ]", toks[0].s)
	}
	for _, t := range toks[1:] {
		if t.s == "where" {
			return nil, errors.New("a 'where' expression is not allowed; select with [ find name=value ]")
		}
	}
	args, _, err := readArgs(toks[1:], false)
	if err != nil {
		return nil, fmt.Errorf("in [ find ]: %s", err)
	}
	return args, nil
}

// parseValue reads one value, quoted or bare.
func parseValue(raw string) (Value, error) {
	if strings.HasPrefix(raw, `"`) {
		end := closingQuote(raw)
		if end != len(raw)-1 {
			return Value{}, errors.New("text follows a closing quote")
		}
		parts, err := parseQuoted(raw[1:end])
		return Value{Parts: parts, Quoted: true}, err
	}
	if raw == "" {
		return Value{}, errors.New(`an unquoted value is empty; write name="" for an empty one`)
	}
	if m := wholeVar.FindStringSubmatch(raw); m != nil {
		return Value{Parts: []Part{{Var: m[1]}}}, nil
	}
	if strings.ContainsAny(raw, "{}") {
		return Value{}, errors.New("a placeholder must be the whole unquoted value, or sit inside quotes")
	}
	if strings.Contains(raw, `"`) {
		return Value{}, errors.New("a quote in the middle of a value")
	}
	return Value{Parts: []Part{{Lit: raw}}}, nil
}

// closingQuote is the index of the quote that closes the one at 0, or -1.
func closingQuote(s string) int {
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return -1
}

// parseQuoted decodes the inside of a quoted string into literal parts and
// placeholders.
//
// The escapes are MikroTik's documented set and no other: `\" \\ \$ \n \r \t
// \_ \a \b \f \v` and `\XX` in CAPITAL hex, which is also what keeps `\b`
// (backspace) and `\BA` (a byte) apart. Anything else after a backslash is
// refused rather than guessed at.
func parseQuoted(s string) ([]Part, error) {
	var (
		parts []Part
		lit   strings.Builder
	)
	flush := func() {
		if lit.Len() > 0 {
			parts = append(parts, Part{Lit: lit.String()})
			lit.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\':
			if i+1 >= len(s) {
				return nil, errors.New("a backslash ends the value")
			}
			if d, ok := simpleEscapes[s[i+1]]; ok {
				lit.WriteByte(d)
				i += 2
				continue
			}
			if i+2 < len(s) && isHexUpper(s[i+1]) && isHexUpper(s[i+2]) {
				lit.WriteByte(hexVal(s[i+1])<<4 | hexVal(s[i+2]))
				i += 3
				continue
			}
			return nil, fmt.Errorf(`\%c is not a RouterOS escape`, s[i+1])
		case c == '$':
			return nil, errors.New(`'$' inside quotes expands a variable or runs a command; write \$ for a dollar sign`)
		case c == '{' && strings.HasPrefix(s[i:], "{{"):
			end := strings.Index(s[i:], "}}")
			if end < 0 {
				return nil, errors.New("a {{ is not closed")
			}
			name := s[i+2 : i+end]
			if !varName.MatchString(name) {
				return nil, fmt.Errorf("%q is not a variable name (lower case, digits and _, starting with a letter)", name)
			}
			flush()
			parts = append(parts, Part{Var: name})
			i += end + 2
		case c < 0x20 || c == 0x7f:
			return nil, errors.New("a control character inside a value; write it as an escape such as \\n")
		default:
			lit.WriteByte(c)
			i++
		}
	}
	flush()
	for _, p := range parts {
		if err := visibleText(p.Lit); err != nil {
			return nil, err
		}
	}
	return parts, nil
}

// simpleEscapes is the documented one-letter set, decoded.
var simpleEscapes = map[byte]byte{
	'"': '"', '\\': '\\', '$': '$', 'n': '\n', 'r': '\r', 't': '\t',
	'_': ' ', 'a': 0x07, 'b': 0x08, 'f': 0x0c, 'v': 0x0b,
}

// visibleText refuses the characters that make text read differently from what
// it is: bidirectional overrides, zero-width characters, line separators and the
// C1 controls. A template is reviewed by a person before it runs, and what they
// read must be what the router gets.
func visibleText(s string) error {
	for _, r := range s {
		switch {
		case r >= 0x80 && r <= 0x9f,
			r >= 0x200b && r <= 0x200f,
			r >= 0x202a && r <= 0x202e,
			r >= 0x2060 && r <= 0x2064,
			r >= 0x2066 && r <= 0x2069,
			r == 0x2028, r == 0x2029, r == 0xfeff:
			return fmt.Errorf("an invisible or direction-changing character (U+%04X) is not allowed", r)
		}
	}
	return nil
}

func isHexUpper(c byte) bool { return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') }

func hexVal(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'A' + 10
}
