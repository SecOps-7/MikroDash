package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mikrodash/internal/cfgtpl"
)

func TestATemplateIsCheckedBeforeItIsStored(t *testing.T) {
	ok := cfgTemplateIn{Name: "  DNS  ", Body: "# resolvers\n/ip dns\nset servers={{dns}}\n",
		Variables: []cfgtpl.VarDef{{Name: "dns", Type: "ipv4-list"}}}
	c, err := checkTemplate(ok, cfgtpl.Additions)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "DNS" || c.Scope != `["/ip/dns"]` || c.Fingerprint == "" {
		t.Errorf("checked as %+v", c)
	}
	// A comment changes nothing a router would receive, so not the fingerprint.
	again := ok
	again.Body = "/ip dns\nset servers={{dns}}"
	if c2, _ := checkTemplate(again, cfgtpl.Additions); c2.Fingerprint != c.Fingerprint {
		t.Error("a comment moved the fingerprint; drift would call it an edit")
	}

	cases := []struct {
		name string
		in   cfgTemplateIn
		want string
		line int
	}{
		{"no name", cfgTemplateIn{Body: "/ip dns\nset a=1"}, "name", 0},
		{"long name", cfgTemplateIn{Name: strings.Repeat("x", 81), Body: "/ip dns\nset a=1"}, "name", 0},
		{"scripting, with its line", cfgTemplateIn{Name: "x", Body: "/ip dns\nset a=1\n:delay 1"}, "scripting", 3},
		{"empty", cfgTemplateIn{Name: "x", Body: "# nothing"}, "no commands", 0},
		{"undeclared", cfgTemplateIn{Name: "x", Body: "/ip dns\nset servers={{dns}}"}, "not declared", 0},
		{"unused", cfgTemplateIn{Name: "x", Body: "/ip dns\nset a=1",
			Variables: []cfgtpl.VarDef{{Name: "dns", Type: "ipv4"}}}, "never used", 0},
		{"secret default", cfgTemplateIn{Name: "x", Body: "/ip dns\nset a={{pw}}",
			Variables: []cfgtpl.VarDef{{Name: "pw", Type: "secret", Default: "hunter2"}}}, "default", 0},
	}
	for _, k := range cases {
		_, err := checkTemplate(k.in, cfgtpl.Additions)
		ie, isInput := err.(*cfgInputError)
		if !isInput || !strings.Contains(ie.Msg, k.want) || ie.Line != k.line {
			t.Errorf("%s: %v (line %v), want %q on line %d", k.name, err, ie, k.want, k.line)
		}
	}
}

// A refused line is a finding, not a refusal to save: a draft is worth keeping.
func TestAFindingDoesNotStopASave(t *testing.T) {
	c, err := checkTemplate(cfgTemplateIn{Name: "x", Body: "/system script\nadd name=x source=y"}, cfgtpl.Additions)
	if err != nil {
		t.Fatalf("refused to save: %v", err)
	}
	if cfgtpl.Worst(c.Findings) != cfgtpl.Refuse {
		t.Errorf("findings %+v; the deploy must still see the refusal", c.Findings)
	}
}

// Sign-in off makes everybody a global administrator for READING; a template
// is code, so changing one needs somebody who actually signed in.
func TestOnlyASignedInAdminChangesTemplates(t *testing.T) {
	if cfgMayChange(&Session{AuthMode: "none"}, true) {
		t.Error("sign-in off may change templates")
	}
	if cfgMayChange(&Session{AuthMode: "modern", Username: "op"}, false) {
		t.Error("a non-administrator may change templates")
	}
	if !cfgMayChange(&Session{AuthMode: "modern", Username: "admin"}, true) {
		t.Error("a signed-in administrator may not change templates")
	}
	if cfgMayChange(nil, true) {
		t.Error("no session may change templates")
	}
}

func TestTheTemplateRoutes(t *testing.T) {
	mux := newServeMuxFor(t)
	for _, c := range []struct{ method, path string }{
		{"GET", cfgPrefix + "templates"}, {"GET", cfgPrefix + "templates/abc"},
		{"POST", cfgPrefix + "templates"}, {"PUT", cfgPrefix + "templates/abc"},
		{"DELETE", cfgPrefix + "templates/abc"}, {"POST", cfgPrefix + "templates/abc/clone"},
	} {
		if _, p := mux.Handler(httptest.NewRequest(c.method, c.path, nil)); p == "" {
			t.Errorf("%s %s is not routed", c.method, c.path)
		}
	}
	// No route writes on a GET.
	if _, p := mux.Handler(httptest.NewRequest("GET", cfgPrefix+"templates/abc/clone", nil)); p != "" && !strings.HasPrefix(p, "GET") {
		t.Errorf("GET clone matched %q", p)
	}
}

func TestACloneNameFits(t *testing.T) {
	if got := cloneName("DNS", 1); got != "DNS (copy)" {
		t.Error(got)
	}
	if got := cloneName("DNS", 3); got != "DNS (copy 3)" {
		t.Error(got)
	}
	if got := cloneName(strings.Repeat("x", 80), 12); len(got) > 80 || !strings.HasSuffix(got, "(copy 12)") {
		t.Errorf("%q (%d)", got, len(got))
	}
}

func newServeMuxFor(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	(&Server{}).registerConfig(mux)
	return mux
}
