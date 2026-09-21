package cfgtpl

import (
	"fmt"
	"strings"
)

// MaxPart is the most a single file sent to a router may hold, in bytes.
//
// MEASURED, not documented (docs/routeros-api-surface.md, cmd/importprobe):
// `/file/set contents=` accepts 60416 bytes and refuses 61440 with a clean
// trap — but at 102400 bytes it gets NO trap: the router closes the API
// connection, and every command after it on that connection fails. That
// connection is the shared session every collector uses for the router, so an
// oversized write would blind the dashboard for it. The limit is therefore
// held here, with a margin, before anything is sent.
const MaxPart = 60000

// Format writes a template back as template text: placeholders kept, comments
// dropped, one command per line under a menu header whenever the menu changes.
func Format(t *Template) string {
	s, _ := write(t, nil)
	return s
}

// Render fills every placeholder and writes the text a router is sent.
//
// It does not judge the VALUES — that is typing, and the analyser's — because
// its safety does not depend on them: every value, whatever it holds, leaves as
// one string literal (QuoteROS). What it does refuse is a template that is not
// finished: a placeholder with no value, which it will not quietly render as
// empty, and an `ensure` line, which has to be resolved against the router's
// live rows first.
func Render(t *Template, vals map[string]string) (string, error) {
	for _, l := range t.Lines {
		if l.Verb == "ensure" {
			return "", &ParseError{l.Num, "an ensure line must be resolved against the router before it is sent"}
		}
	}
	return write(t, func(name string) (string, error) {
		v, ok := vals[name]
		if !ok {
			return "", fmt.Errorf("no value was given for {{%s}}", name)
		}
		return v, nil
	})
}

// Fill is t with every placeholder replaced by its value, as literal text:
// what the router will receive, in a form the analyser can judge. It is for
// ANALYSIS only; the router is sent Render's output, which quotes each value.
func Fill(t *Template, vals map[string]string) (*Template, error) {
	fill := func(v Value) (Value, error) {
		if _, lit := v.Literal(); lit {
			return v, nil
		}
		var b strings.Builder
		for _, p := range v.Parts {
			if p.Var == "" {
				b.WriteString(p.Lit)
				continue
			}
			x, ok := vals[p.Var]
			if !ok {
				return v, fmt.Errorf("no value was given for {{%s}}", p.Var)
			}
			b.WriteString(x)
		}
		return Value{Parts: []Part{{Lit: b.String()}}, Quoted: true}, nil
	}
	fillArgs := func(as []Arg) ([]Arg, error) {
		out := make([]Arg, len(as))
		for i, a := range as {
			v, err := fill(a.Value)
			if err != nil {
				return nil, err
			}
			out[i] = Arg{Name: a.Name, Value: v}
		}
		return out, nil
	}
	out := &Template{Lines: make([]Line, len(t.Lines))}
	for i, l := range t.Lines {
		var err error
		if l.Args, err = fillArgs(l.Args); err != nil {
			return nil, &ParseError{l.Num, err.Error()}
		}
		if l.Find, err = fillArgs(l.Find); err != nil {
			return nil, &ParseError{l.Num, err.Error()}
		}
		pos := make([]Value, len(l.Pos))
		for j, v := range l.Pos {
			if pos[j], err = fill(v); err != nil {
				return nil, &ParseError{l.Num, err.Error()}
			}
		}
		l.Pos = pos
		out.Lines[i] = l
	}
	return out, nil
}

// RenderCommands renders each command as ONE absolute line,
// `/ip address add address=…`, for a script that must wrap every command on
// its own: the export + reset bootstrap puts each inside
// `:do { … } on-error={ … }` so that one failing line cannot abort the rest
// (measured, cmd/importprobe m8 and m12).
//
// The line is write's own output for that command, header joined to it, so
// every value still leaves through QuoteROS: a rendered value can hold no
// unquoted `{`, `}` or `;` to break out of the wrapping.
func RenderCommands(t *Template, vals map[string]string) ([]string, error) {
	out := make([]string, 0, len(t.Lines))
	for _, l := range t.Lines {
		s, err := Render(&Template{Lines: []Line{l}}, vals)
		if err != nil {
			return nil, err
		}
		hdr, cmd, _ := strings.Cut(strings.TrimSuffix(s, "\n"), "\n")
		out = append(out, hdr+" "+cmd)
	}
	return out, nil
}

// write is Format and Render's one writer. A nil sub keeps placeholders.
//
// THE FORM IS AN EXPORT'S: a header line whenever the menu changes, then
// commands relative to it. It is what RouterOS emits and imports, and it is
// chosen over one-line `/menu/path/verb …` commands because whether a
// `[ find ]` in such a line resolves in the command's menu or at the root is
// the kind of question two readers answer differently.
func write(t *Template, sub func(string) (string, error)) (string, error) {
	var b strings.Builder
	cur := ""
	for _, l := range t.Lines {
		if hdr := "/" + strings.Join(l.Menu, " "); hdr != cur {
			b.WriteString(hdr)
			b.WriteByte('\n')
			cur = hdr
		}
		b.WriteString(l.Verb)
		if l.HasFind {
			b.WriteString(" [ find")
			for _, a := range l.Find {
				s, err := emit(a.Value, sub)
				if err != nil {
					return "", &ParseError{l.Num, err.Error()}
				}
				b.WriteString(" " + a.Name + "=" + s)
			}
			b.WriteString(" ]")
		}
		for _, v := range l.Pos {
			s, err := emit(v, sub)
			if err != nil {
				return "", &ParseError{l.Num, err.Error()}
			}
			b.WriteString(" " + s)
		}
		for _, a := range l.Args {
			s, err := emit(a.Value, sub)
			if err != nil {
				return "", &ParseError{l.Num, err.Error()}
			}
			b.WriteString(" " + a.Name + "=" + s)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// emit writes one value.
func emit(v Value, sub func(string) (string, error)) (string, error) {
	if sub == nil {
		if name, ok := v.Var(); ok {
			return "{{" + name + "}}", nil
		}
		if lit, ok := v.Literal(); ok {
			return emitLiteral(lit), nil
		}
		// Placeholders among literal text: always quoted, which is the only
		// form the parser accepts them in.
		var b strings.Builder
		b.WriteByte('"')
		for _, p := range v.Parts {
			if p.Var != "" {
				b.WriteString("{{" + p.Var + "}}")
			} else {
				b.WriteString(escapeInner(p.Lit))
			}
		}
		b.WriteByte('"')
		return b.String(), nil
	}
	var b strings.Builder
	for _, p := range v.Parts {
		if p.Var == "" {
			b.WriteString(p.Lit)
			continue
		}
		s, err := sub(p.Var)
		if err != nil {
			return "", err
		}
		b.WriteString(s)
	}
	return emitLiteral(b.String()), nil
}

// SplitParts cuts rendered text into pieces no larger than max bytes, each one
// importable by itself: every piece opens with the menu header in force where
// it begins. A single command longer than max is refused — a command cannot be
// cut — rather than sent to break the connection it rides on.
func SplitParts(rendered string, max int) ([]string, error) {
	var (
		parts []string
		b     strings.Builder
		hdr   string
	)
	for n, line := range strings.Split(strings.TrimRight(rendered, "\n"), "\n") {
		if line == "" {
			continue
		}
		isHdr := strings.HasPrefix(line, "/") && !strings.Contains(line, "=")
		size := len(line) + 1
		if len(hdr)+1+size > max {
			return nil, fmt.Errorf("line %d is %d bytes: longer than one file may be (%d)", n+1, size, max)
		}
		if b.Len()+size > max {
			parts = append(parts, b.String())
			b.Reset()
			if !isHdr && hdr != "" {
				b.WriteString(hdr + "\n")
			}
		}
		if isHdr {
			hdr = line
		}
		b.WriteString(line + "\n")
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts, nil
}
