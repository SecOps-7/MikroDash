package fleetimport

import (
	"encoding/csv"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestTheDownloadableTemplatePlansAsReady reads the template the browser
// offers (web/src/pages/device-import.ts) and runs it through this planner,
// the preview's own: every example row must come back ready, each as its kind.
// The template and the planner live in two languages, and a column renamed on
// one side only would ship a template whose own examples are refused.
func TestTheDownloadableTemplatePlansAsReady(t *testing.T) {
	src, err := os.ReadFile("../../web/src/pages/device-import.ts")
	if err != nil {
		t.Fatal(err)
	}
	head := regexp.MustCompile(`(?s)TEMPLATE_HEADERS = \[(.*?)\]`).FindSubmatch(src)
	if head == nil {
		t.Fatal("TEMPLATE_HEADERS has moved; this check reads nothing")
	}
	var headers []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(head[1], -1) {
		headers = append(headers, string(m[1]))
	}
	tpl := regexp.MustCompile(`(?s)export const TEMPLATE = .*?;\n`).Find(src)
	var lines []string
	for _, m := range regexp.MustCompile(`\+ '([^']*)\\r\\n'`).FindAllSubmatch(tpl, -1) {
		lines = append(lines, string(m[1]))
	}
	if len(headers) < 10 || len(lines) < 2 {
		t.Fatalf("read %d headers and %d rows off the template", len(headers), len(lines))
	}
	recs, err := csv.NewReader(strings.NewReader(strings.Join(lines, "\n"))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]func(*Row, string){
		"type": func(r *Row, v string) { r.Type = v }, "host": func(r *Row, v string) { r.Host = v },
		"name": func(r *Row, v string) { r.Name = v }, "port": func(r *Row, v string) { r.Port = v },
		"tls": func(r *Row, v string) { r.TLS = v }, "tls_insecure": func(r *Row, v string) { r.TLSInsecure = v },
		"username": func(r *Row, v string) { r.Username = v }, "password": func(r *Row, v string) { r.Password = v },
		"credential_profile": func(r *Row, v string) { r.Profile = v }, "sites": func(r *Row, v string) { r.Sites = v },
		"uplink_interface": func(r *Row, v string) { r.Uplink = v }, "ping_target": func(r *Row, v string) { r.PingTarget = v },
		"scheme": func(r *Row, v string) { r.Scheme = v },
	}
	var rows []Row
	for i, rec := range recs {
		r := Row{Line: i + 2}
		for j, h := range headers {
			f := set[h]
			if f == nil {
				t.Fatalf("the template has a column %q the planner has no field for", h)
			}
			if j < len(rec) {
				f(&r, rec[j])
			}
		}
		rows = append(rows, r)
	}
	res := Plan(rows, Fleet{})
	kinds := map[string]bool{}
	for _, v := range res.Rows {
		if v.Status != "ready" {
			t.Errorf("template row %d is %s: %s", v.Line, v.Status, v.Reason)
		}
		kinds[v.Kind] = true
	}
	if !kinds["routeros"] || !kinds["swos"] {
		t.Errorf("the template's examples cover %v; it promises one row of each kind", kinds)
	}
}
