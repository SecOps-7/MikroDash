package verify

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryIdInTheAppIsDeclaredOnce: the shell and every page's markup are
// composed into ONE document by cmd/webbuild, so an id two of them declare is
// one getElementById cannot tell apart. It returns the first, and the code
// meant for the second writes into it.
//
// That happened on 2026-09-19: the traceroute map's hop list was given the id
// the Max hops <select> already had. The map wrote its list into the select,
// which erased the options, so the list never showed AND every trace ran at the
// default 15 hops. Two symptoms, neither pointing at the cause.
//
// login.html is left out: it is its own document, served to a browser that is
// not signed in, and shares its setup form's ids with the shell by design.
func TestEveryIdInTheAppIsDeclaredOnce(t *testing.T) {
	root := repoRoot(t)
	files := readFiles(t, root, "web/src/ui/", func(r string) bool {
		base := filepath.Base(r)
		return base == "shell.html" || (strings.HasPrefix(base, "page-") && hasExt(r, ".html"))
	})
	if len(files) < 30 {
		t.Fatalf("only %d markup files read - the scan broke", len(files))
	}
	idRe := regexp.MustCompile(`\sid="([^"]+)"`)
	where := map[string][]string{}
	total := 0
	for rel, src := range files {
		for _, m := range idRe.FindAllStringSubmatch(src, -1) {
			where[m[1]] = append(where[m[1]], filepath.Base(rel))
			total++
		}
	}
	if total < 500 {
		t.Fatalf("only %d ids found - the scan broke", total)
	}
	var dup []string
	for id, in := range where {
		if len(in) > 1 {
			sort.Strings(in)
			dup = append(dup, id+" ("+strings.Join(in, ", ")+")")
		}
	}
	sort.Strings(dup)
	for _, d := range dup {
		t.Errorf("id %s is declared more than once in the app document", d)
	}
	t.Logf("%d ids across %d markup files, each declared once", total, len(files))
}
