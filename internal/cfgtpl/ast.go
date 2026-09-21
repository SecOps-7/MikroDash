package cfgtpl

import (
	"sort"
	"strings"
)

// Part is one piece of a value: literal text, or a placeholder naming a
// variable. Exactly one of the two is set.
type Part struct {
	// Lit is the literal bytes, already decoded from any escape sequences.
	Lit string
	// Var is a placeholder's variable name. Lit is empty when this is set.
	Var string
}

// Value is an argument's value as read.
type Value struct {
	Parts []Part
	// Quoted records whether the source wrote the value in quotes. It decides
	// ONE thing — whether a placeholder may share the value with literal text —
	// and is not written back: Format and Render choose quoting from the value's
	// content, so a value never leaves in a form this package did not pick.
	Quoted bool
}

// Var reports whether the value is exactly one placeholder, and which.
func (v Value) Var() (string, bool) {
	if len(v.Parts) == 1 && v.Parts[0].Var != "" {
		return v.Parts[0].Var, true
	}
	return "", false
}

// Literal reports the value's text when it holds no placeholder.
func (v Value) Literal() (string, bool) {
	var b strings.Builder
	for _, p := range v.Parts {
		if p.Var != "" {
			return "", false
		}
		b.WriteString(p.Lit)
	}
	return b.String(), true
}

// Arg is `name=value`.
type Arg struct {
	Name  string
	Value Value
}

// Line is one command.
type Line struct {
	// Num is the physical line it starts on, 1-based, for messages.
	Num int
	// Menu is the menu in words: "ip", "firewall", "filter".
	Menu []string
	// Verb is one of add, set, remove, enable, disable, unset, ensure.
	Verb string
	// HasFind is true when a `[ find … ]` selector is present. With no Find
	// terms it selects EVERY row of the menu, which is worth noticing.
	HasFind bool
	Find    []Arg
	// Pos is the positional values after the verb — `set 0 protocol=gre`.
	// Real exports carry them for default items; whether one is allowed is the
	// analyser's question, not the parser's.
	Pos  []Value
	Args []Arg
}

// Path is the menu in slash form, "/ip/firewall/filter".
func (l Line) Path() string { return "/" + strings.Join(l.Menu, "/") }

// Arg returns the named argument's value.
func (l Line) Arg(name string) (Value, bool) {
	for _, a := range l.Args {
		if a.Name == name {
			return a.Value, true
		}
	}
	return Value{}, false
}

// Template is a parsed body.
type Template struct {
	Lines []Line
}

// Vars lists every placeholder the template uses, first appearance first, each
// once.
func (t *Template) Vars() []string {
	seen := map[string]bool{}
	var out []string
	add := func(v Value) {
		for _, p := range v.Parts {
			if p.Var != "" && !seen[p.Var] {
				seen[p.Var] = true
				out = append(out, p.Var)
			}
		}
	}
	for _, l := range t.Lines {
		for _, a := range l.Find {
			add(a.Value)
		}
		for _, v := range l.Pos {
			add(v)
		}
		for _, a := range l.Args {
			add(a.Value)
		}
	}
	return out
}

// Menus lists every menu the template touches, in slash form, sorted, each
// once. It is the template's SCOPE: what a drift check exports, and what the
// Library shows as chips.
func (t *Template) Menus() []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range t.Lines {
		p := l.Path()
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
