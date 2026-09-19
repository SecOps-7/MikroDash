package server

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"mikrodash/internal/areas"
	"mikrodash/internal/pages"
	"mikrodash/internal/resource"
)

// TestEveryWritablePageIsRefreshedAfterAWrite: refreshFor re-reads the
// collector behind a resource's page after a write, and a page it has no case
// for falls through to a log line. That is how the DHCP page lost it: a saved
// lease appeared only when the lease table's interval ran out (290 s on the
// Standard profile), and the only trace was "dhcpLease belongs to page "dhcp",
// which has no collector to refresh" on every save (reported 2026-09-19).
//
// A LEDGER, BOTH WAYS: every page a registry resource belongs to has a case in
// refreshFor or is a generated area (whose one default case covers them all),
// and every case names a real page some resource belongs to.
func TestEveryWritablePageIsRefreshedAfterAWrite(t *testing.T) {
	src, err := os.ReadFile("resource.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func (cn *conn) refreshFor(")
	if start < 0 {
		t.Fatal("refreshFor not found in resource.go: this ledger is reading the wrong file")
	}
	end := strings.Index(body[start:], "\n}\n")
	fn := body[start : start+end]

	cases := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\tcase ([^:]+):`).FindAllStringSubmatch(fn, -1) {
		for _, q := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(m[1], -1) {
			cases[q[1]] = true
		}
	}
	if len(cases) < 5 {
		t.Fatalf("read %d case labels from refreshFor; the parse broke", len(cases))
	}

	resourcePages := map[string][]string{}
	for _, r := range resource.All() {
		resourcePages[r.Page] = append(resourcePages[r.Page], r.Key)
	}
	var missing []string
	for page, keys := range resourcePages {
		if cases[page] {
			continue
		}
		if _, ok := areas.ByKey(page); ok {
			continue
		}
		missing = append(missing, page+" ("+strings.Join(keys, ", ")+")")
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("a write on these pages re-reads nothing, so the saved row appears only when "+
			"the page's collector next runs on its own: %v. Add a case to refreshFor.", missing)
	}

	for c := range cases {
		if !pages.Has(c) {
			t.Errorf("refreshFor has a case for %q, which is not a page", c)
			continue
		}
		if len(resourcePages[c]) == 0 {
			t.Errorf("refreshFor has a case for page %q, which no resource belongs to: it can never run", c)
		}
	}
}
