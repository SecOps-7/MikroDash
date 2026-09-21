package cfgtpl

import (
	"strings"
	"testing"
)

func resolve(t *testing.T, src string, vals map[string]string, live map[string][]map[string]string) (string, []EnsureResult) {
	t.Helper()
	out, res, err := ResolveEnsure(mustParse(t, src), vals, live)
	if err != nil {
		t.Fatalf("%q: %v", src, err)
	}
	s, err := Render(out, vals)
	if err != nil {
		t.Fatalf("rendering the resolved template: %v", err)
	}
	return s, res
}

func TestEnsureAddsOnlyWhatIsAbsent(t *testing.T) {
	live := map[string][]map[string]string{
		"/interface/list": {{".id": "*1", "name": "WAN"}, {".id": "*2", "name": "LAN"}},
	}
	got, res := resolve(t, "/interface list\nensure name=WAN\nensure name=MGMT", nil, live)
	if got != "/interface list\nadd name=MGMT\n" {
		t.Errorf("rendered %q", got)
	}
	if len(res) != 2 || res[0].Added || !res[1].Added {
		t.Errorf("results %+v", res)
	}
}

// Every argument is the identity: a row that differs in one is not the row.
func TestEnsureMatchesOnEveryArgument(t *testing.T) {
	live := map[string][]map[string]string{
		"/interface/list/member": {{"list": "WAN", "interface": "ether1"}},
	}
	got, _ := resolve(t, "/interface list member\nensure list=WAN interface=ether1\nensure list=WAN interface=ether2",
		nil, live)
	if got != "/interface list member\nadd list=WAN interface=ether2\n" {
		t.Errorf("rendered %q", got)
	}
}

// The API answers true where an export writes yes.
func TestEnsureReadsTheAPIsBooleans(t *testing.T) {
	live := map[string][]map[string]string{"/ip/pool": {{"name": "p", "disabled": "true"}}}
	if got, _ := resolve(t, "/ip pool\nensure name=p disabled=yes", nil, live); got != "" {
		t.Errorf("a row the API reports as disabled=true was not found for disabled=yes: %q", got)
	}
}

// THE REMOVE BEFORE IT COUNTS. A canned template clears its own rows and
// ensures them again; judged against the live rows alone, nothing would come
// back.
func TestEnsureSeesTheLinesBeforeIt(t *testing.T) {
	live := map[string][]map[string]string{
		"/ip/firewall/address-list": {{"list": "bogons", "address": "0.0.0.0/8", "comment": "mdcfg:x"}},
	}
	src := "/ip firewall address-list\nremove [ find comment=mdcfg:x ]\nensure list=bogons address=0.0.0.0/8 comment=mdcfg:x"
	got, res := resolve(t, src, nil, live)
	if !strings.Contains(got, "add list=bogons") || !res[0].Added {
		t.Errorf("the removed row was treated as present: %q", got)
	}
	// And an ensure after an add of the same row adds nothing twice.
	got, _ = resolve(t, "/interface list\nensure name=A\nensure name=A", nil, map[string][]map[string]string{"/interface/list": nil})
	if strings.Count(got, "add name=A") != 1 {
		t.Errorf("one row ensured twice was added %d times", strings.Count(got, "add name=A"))
	}
	// A disable changes what a later ensure sees.
	got, _ = resolve(t, "/ip pool\ndisable [ find name=p ]\nensure name=p disabled=no",
		nil, map[string][]map[string]string{"/ip/pool": {{"name": "p", "disabled": "false"}}})
	if !strings.Contains(got, "add name=p") {
		t.Errorf("an ensure after a disable did not see it: %q", got)
	}
}

// A placeholder is filled for the comparison, and still left for Render to
// quote: the router gets Render's text, never ensure's.
func TestEnsureKeepsPlaceholdersForRender(t *testing.T) {
	vals := map[string]string{"n": `x" ; /system reboot`}
	out, _, err := ResolveEnsure(mustParse(t, "/interface list\nensure name={{n}}"), vals,
		map[string][]map[string]string{"/interface/list": nil})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := out.Lines[0].Args[0].Value.Var(); v != "n" {
		t.Fatalf("the placeholder was replaced before Render: %+v", out.Lines[0].Args[0].Value)
	}
	s, _ := Render(out, vals)
	if s != "/interface list\nadd name=\"x\\\" ; /system reboot\"\n" {
		t.Errorf("rendered %q", s)
	}
	live := map[string][]map[string]string{"/interface/list": {{"name": vals["n"]}}}
	if got, _ := resolve(t, "/interface list\nensure name={{n}}", vals, live); got != "" {
		t.Errorf("the filled value was not compared: %q", got)
	}
}

func TestEnsureRefusesWhatItCannotTell(t *testing.T) {
	live := map[string][]map[string]string{"/interface/list": nil}
	for _, src := range []string{
		"/interface list\nremove 0\nensure name=A",
		"/interface list\nunset [ find name=A ] comment\nensure name=A",
	} {
		if _, _, err := ResolveEnsure(mustParse(t, src), nil, live); err == nil {
			t.Errorf("%q: resolved, though a change before the ensure cannot be replayed", src)
		}
	}
	if _, _, err := ResolveEnsure(mustParse(t, "/ip pool\nensure name=p"), nil, live); err == nil {
		t.Error("an ensure in a menu whose rows were not read was resolved")
	}
	if _, _, err := ResolveEnsure(mustParse(t, "/interface list\nensure name={{n}}"), nil, live); err == nil {
		t.Error("an ensure with a missing value was resolved")
	}
	// Other menus pass through untouched, row numbers and all.
	if _, _, err := ResolveEnsure(mustParse(t, "/ip firewall filter\nset 0 disabled=yes\n/interface list\nensure name=A"), nil, live); err != nil {
		t.Errorf("a row number in a menu with no ensure was refused: %v", err)
	}
}
