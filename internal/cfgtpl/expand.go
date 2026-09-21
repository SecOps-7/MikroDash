package cfgtpl

import "strings"

// Bind is the one way from a template and what the operator typed to what
// Render and Fill are given: the values resolved, then every per-item line
// expanded. resolveValues is unexported so that no caller can skip the expansion
// and render a list selector unexpanded, where it would match no row at all.
func Bind(t *Template, defs []VarDef, given, server map[string]string) (*Template, map[string]string, error) {
	vals, err := resolveValues(defs, given, server)
	if err != nil {
		return nil, nil, err
	}
	return Expand(t, defs, vals), vals, nil
}

// Expand writes each line whose `[ find ]` selects by a whole iface-list
// placeholder once per interface in the list, that interface in the
// placeholder's place: `set [ find interface={{home_ports}} ] pvid=10` with
// ether2,ether3 is two lines, one per port. An empty list writes no line.
//
// The template language has no loops on purpose; this is the one repetition
// it has, and it stays inside the selector, where each copy is an ordinary
// line the analyser judges on its own. Anywhere else a list is its
// comma-separated text, which is how RouterOS takes `untagged=` or `dst-port=`.
func Expand(t *Template, defs []VarDef, vals map[string]string) *Template {
	lists := map[string]bool{}
	for _, d := range defs {
		if d.Type == "iface-list" {
			lists[d.Name] = true
		}
	}
	out := &Template{Lines: make([]Line, 0, len(t.Lines))}
	for _, l := range t.Lines {
		at, name := -1, ""
		for i, a := range l.Find {
			if n, ok := a.Value.Var(); ok && lists[n] {
				at, name = i, n
				break
			}
		}
		if at < 0 {
			out.Lines = append(out.Lines, l)
			continue
		}
		for _, item := range strings.Split(vals[name], ",") {
			if item = strings.TrimSpace(item); item == "" {
				continue
			}
			c := l
			c.Find = append([]Arg(nil), l.Find...)
			c.Find[at] = Arg{Name: l.Find[at].Name, Value: Value{Parts: []Part{{Lit: item}}, Quoted: true}}
			out.Lines = append(out.Lines, c)
		}
	}
	return out
}
