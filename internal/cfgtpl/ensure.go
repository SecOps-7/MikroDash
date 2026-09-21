package cfgtpl

import (
	"fmt"
	"strings"
)

// EnsureMenus lists the menus whose live rows ResolveEnsure needs: every menu
// that holds an `ensure` line.
func EnsureMenus(t *Template) []string {
	var out []string
	seen := map[string]bool{}
	for _, l := range t.Lines {
		if l.Verb == "ensure" && !seen[l.Path()] {
			seen[l.Path()] = true
			out = append(out, l.Path())
		}
	}
	return out
}

// EnsureResult is what became of one `ensure` line.
type EnsureResult struct {
	Line  int  `json:"line"`
	Added bool `json:"added"`
}

// ResolveEnsure turns every `ensure` line into an `add`, or drops it, against
// one router's live rows. `live` holds the rows of every menu EnsureMenus
// named, keyed by API property name, read inside the same write-queue hold
// the import runs in.
//
// ── WHAT "ABSENT" MEANS ─────────────────────────────────────────────────────
//
// A row is present when it holds EVERY argument the ensure line writes, with
// the same value. So an ensure line names a row by exactly what it writes, and
// a template writes only what identifies the row there (`ensure name=WAN`),
// then sets anything else with `set [ find name=WAN ] …`. The alternative,
// matching on a guessed key such as name, was rejected: which property makes
// a row "the same" differs per menu (an interface-list member has no name),
// and a guess that is wrong adds a duplicate silently or skips a row silently.
// Written this way, a wrong guess cannot happen.
//
// ── THE LINES BEFORE IT COUNT ───────────────────────────────────────────────
//
// A canned template opens with `remove [ find comment="mdcfg:<id>" ]` and then
// ensures its rows again. Judged against the live rows alone, every one of
// those would be "present" and skipped, and the remove would leave the router
// without them. So the template's own add, set, remove, enable and disable
// lines are replayed onto the rows in order, and each ensure is judged at its
// place in the file. A change this cannot replay (a row number, `unset`) in a
// menu that holds an ensure is refused, not guessed past.
//
// The returned template still carries its placeholders: Render fills and
// quotes them, so an `ensure` never becomes a second way text reaches the
// router.
func ResolveEnsure(t *Template, vals map[string]string, live map[string][]map[string]string) (*Template, []EnsureResult, error) {
	needs := map[string]bool{}
	for _, m := range EnsureMenus(t) {
		needs[m] = true
		if _, ok := live[m]; !ok {
			return nil, nil, fmt.Errorf("%s: its rows were not read, so whether a row is absent cannot be told", m)
		}
	}
	rows := map[string][]map[string]string{}
	for m := range needs {
		for _, r := range live[m] {
			c := map[string]string{}
			for k, v := range r {
				c[k] = normalise(v)
			}
			rows[m] = append(rows[m], c)
		}
	}

	out := &Template{}
	var results []EnsureResult
	for _, l := range t.Lines {
		menu := l.Path()
		if !needs[menu] {
			out.Lines = append(out.Lines, l)
			continue
		}
		if len(l.Pos) > 0 || l.Verb == "unset" {
			return nil, nil, &ParseError{l.Num, fmt.Sprintf("%s %s: MikroDash cannot tell which row this changes, "+
				"and this menu holds an ensure line that depends on it", menu, l.Verb)}
		}
		args, err := concrete(l.Args, vals)
		if err != nil {
			return nil, nil, &ParseError{l.Num, err.Error()}
		}
		find, err := concrete(l.Find, vals)
		if err != nil {
			return nil, nil, &ParseError{l.Num, err.Error()}
		}
		switch l.Verb {
		case "ensure":
			present := false
			for _, r := range rows[menu] {
				if holds(r, args) {
					present = true
					break
				}
			}
			results = append(results, EnsureResult{Line: l.Num, Added: !present})
			if present {
				continue
			}
			rows[menu] = append(rows[menu], args)
			l.Verb = "add"
		case "add":
			rows[menu] = append(rows[menu], args)
		case "remove":
			kept := rows[menu][:0]
			for _, r := range rows[menu] {
				if !holds(r, find) {
					kept = append(kept, r)
				}
			}
			rows[menu] = kept
		case "set", "enable", "disable":
			if l.Verb != "set" {
				args = map[string]string{"disabled": normalise(map[string]string{"enable": "no", "disable": "yes"}[l.Verb])}
			}
			for _, r := range rows[menu] {
				if holds(r, find) {
					for k, v := range args {
						r[k] = v
					}
				}
			}
		}
		out.Lines = append(out.Lines, l)
	}
	return out, results, nil
}

// holds reports whether a row has every one of want's values. An empty want —
// `[ find ]` — matches every row, as it does on the router.
func holds(row, want map[string]string) bool {
	for k, v := range want {
		if row[k] != v {
			return false
		}
	}
	return true
}

// concrete fills a line's arguments with the run's values, normalised.
func concrete(as []Arg, vals map[string]string) (map[string]string, error) {
	m := map[string]string{}
	for _, a := range as {
		var b strings.Builder
		for _, p := range a.Value.Parts {
			if p.Var == "" {
				b.WriteString(p.Lit)
				continue
			}
			v, ok := vals[p.Var]
			if !ok {
				return nil, fmt.Errorf("no value was given for {{%s}}", p.Var)
			}
			b.WriteString(v)
		}
		m[a.Name] = normalise(b.String())
	}
	return m, nil
}

// normalise puts a template's spelling and the API's in one form: an export
// writes `yes` and `no` where the API answers `true` and `false`.
func normalise(v string) string {
	switch v {
	case "yes":
		return "true"
	case "no":
		return "false"
	}
	return v
}
