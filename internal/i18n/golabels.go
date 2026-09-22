package i18n

import (
	"regexp"
	"strings"

	"mikrodash/internal/areas"
	"mikrodash/internal/pages"
	"mikrodash/internal/resource"
)

// ── THE LABELS DECLARED IN GO ───────────────────────────────────────────────
//
// A generated page's words are not in the frontend's source: the resource
// schema (field labels and help, action labels, a resource's own label and
// title), the areas (page, tab and panel titles) and the pages (header titles)
// are declared in Go and reach the browser as data. The browser renders them
// through tl(), in the few files the drift gate allows, and they are read here
// from the registries themselves, so the catalog holds exactly what those
// files can be given.
//
// NOT HERE, DELIBERATELY: select options and placeholders. An option is a
// RouterOS value (`any`, `none`, `drop`), and a placeholder is an example of
// one (`auto`, `bridge`, `pool.ntp.org`); translating either would show the
// operator a word the router does not accept.

// GoLabels is every label declared in Go that the browser shows, with where it
// was declared ("internal/resource", "internal/areas", "internal/pages").
func GoLabels() map[string][]string {
	out := map[string][]string{}
	add := func(s, from string) {
		if s = strings.TrimSpace(s); s == "" {
			return
		}
		for _, f := range out[s] {
			if f == from {
				return
			}
		}
		out[s] = append(out[s], from)
	}
	const res, ar, pg = "internal/resource", "internal/areas", "internal/pages"
	for _, r := range resource.All() {
		add(r.Label, res)
		add(r.Title, res)
		for _, f := range r.Fields {
			add(f.Label, res)
			add(f.Help, res)
		}
		for _, a := range r.Actions {
			add(a.Label, res)
		}
	}
	for _, a := range areas.All() {
		add(a.Title, ar)
		for _, t := range a.Tables {
			add(t.Title, ar)
			for _, c := range t.Columns {
				add(ColumnLabel(c), ar)
			}
			if t.GroupBy != "" {
				add(ColumnLabel(t.GroupBy), ar)
			}
		}
		for _, p := range a.Panels {
			add(p.Title, ar)
		}
	}
	for _, p := range pages.All {
		add(p.Title, pg)
	}
	return out
}

var camelHump = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// ColumnLabel is a generated table's header for a field name, `nextPool` to
// "Next Pool": the same split as `columnLabel` in web/src/pages/area.ts, which
// the table draws before any schema has been read. The two must agree for a
// header to find its translation; one that does not stays English.
func ColumnLabel(name string) string {
	s := camelHump.ReplaceAllString(name, "$1 $2")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
