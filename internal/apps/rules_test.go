package apps

import (
	"strings"
	"testing"
)

// The rules come first at each step, and only at that step: a rule's AS number
// does not beat a hostname the catalogue knows.
func TestRulesComeFirstAtEachStep(t *testing.T) {
	c := Default().WithRules([]Rule{
		{ID: "u_films", Name: "Films", Color: "#aa0000", Domains: []DomainRule{{Domain: "netflix.com", Subdomains: true}}},
		{ID: "u_lan", Name: "Lan", Color: "#00aa00", ASNs: []uint{13335}, Ports: []PortRule{{Proto: "udp", From: 53, To: 53}}},
	})
	if m := c.Classify([]string{"www.netflix.com"}, 0, "tcp", 443); m.App == nil || m.App.ID != "u_films" || m.Via != ViaName {
		t.Errorf("a rule's domain over the catalogue's: %+v", m)
	}
	if m := c.Classify(nil, 0, "udp", 53); m.App == nil || m.App.ID != "u_lan" || m.Via != ViaPort {
		t.Errorf("a rule's port over the catalogue's DNS: %+v", m)
	}
	if m := c.Classify(nil, 13335, "tcp", 8443); m.App == nil || m.App.ID != "u_lan" || m.Via != ViaASN {
		t.Errorf("a rule's network over the catalogue's Cloudflare: %+v", m)
	}
	if m := c.Classify([]string{"zoom.us"}, 13335, "tcp", 443); m.App == nil || m.App.ID != "zoom" {
		t.Errorf("a catalogue hostname still beats a rule's network: %+v", m)
	}
	if m := Default().Classify([]string{"www.netflix.com"}, 0, "tcp", 443); m.App == nil || m.App.ID != "netflix" {
		t.Errorf("the default catalogue changed: %+v", m)
	}
}

// A custom app's icon is a built-in logo, filled with its own colour; an
// exact domain does not take the names under it.
func TestWithRulesBuildsTheCustomApp(t *testing.T) {
	c := Default().WithRules([]Rule{{ID: "u_x", Name: "X", Color: "#000000", Icon: "apple", Category: "other",
		Domains: []DomainRule{{Domain: "only.example"}}}})
	a, ok := c.App("u_x")
	if !ok || !a.Custom || a.Icon != "apple" || a.Logo == "" {
		t.Fatalf("custom app %+v", a)
	}
	if a.Logo == "mask" && !a.Dark {
		t.Error("a black mask is not marked dark")
	}
	if m := c.Classify([]string{"only.example"}, 0, "tcp", 443); m.App == nil || m.App.ID != "u_x" {
		t.Errorf("exact domain: %+v", m)
	}
	if m := c.Classify([]string{"sub.only.example"}, 0, "tcp", 443); m.App != nil {
		t.Errorf("an exact domain named a subdomain: %+v", m)
	}
}

// Check cleans what is typed, refuses what cannot be used, blocks a second
// claim and notes what is taken from a built-in app.
func TestCheckCleansAndJudges(t *testing.T) {
	cats := map[string]bool{"gaming": true}
	r, conflicts, err := Default().Check(Rule{Name: " My Game ", Color: "#ABCDEF", Category: "gaming",
		Domains: []DomainRule{{Domain: "https://Play.Example.com/path"}, {Domain: "*.cdn.example.com"}, {Domain: "play.example.com"}}},
		nil, cats)
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "u_my-game" || r.Name != "My Game" || r.Color != "#abcdef" || len(r.Domains) != 2 ||
		r.Domains[0].Domain != "play.example.com" || !r.Domains[1].Subdomains || r.Domains[1].Domain != "cdn.example.com" {
		t.Errorf("cleaned rule %+v", r)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts with nothing: %+v", conflicts)
	}

	for _, bad := range []Rule{
		{Name: "", Color: "#000000"},
		{Name: "A", Color: "red"},
		{Name: "A", Color: "#000000", Domains: []DomainRule{{Domain: "localhost"}}},
		{Name: "A", Color: "#000000", Domains: []DomainRule{{Domain: "bad_label.com"}}},
		{Name: "A", Color: "#000000", Ports: []PortRule{{Proto: "tcp", From: 400, To: 500}}},
		{Name: "A", Color: "#000000", Ports: []PortRule{{Proto: "icmp", From: 1}}},
		{Name: "A", Color: "#000000", ASNs: []uint{0}},
		{Name: "A", Color: "#000000", Category: "nope"},
		{Name: "A", Color: "#000000", Icon: "no-such-logo"},
		{ID: "no-such-app", Builtin: true},
	} {
		if _, _, err := Default().Check(bad, nil, cats); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}

	others := []Rule{{ID: "u_first", Name: "First", Color: "#111111", Domains: []DomainRule{{Domain: "shared.example"}},
		ASNs: []uint{64500}, Ports: []PortRule{{Proto: "udp", From: 5000, To: 5010}}}}
	_, conflicts, err = Default().Check(Rule{Name: "Second", Color: "#222222", Domains: []DomainRule{{Domain: "shared.example"}},
		ASNs: []uint{64500}, Ports: []PortRule{{Proto: "udp", From: 5010, To: 5020}}}, others, cats)
	if err != nil {
		t.Fatal(err)
	}
	blocking := 0
	for _, c := range conflicts {
		if c.Blocking && c.AppID == "u_first" {
			blocking++
		}
	}
	if blocking != 3 {
		t.Errorf("want the domain, network and port blocked: %+v", conflicts)
	}

	_, conflicts, _ = Default().Check(Rule{Name: "Films", Color: "#222222", Domains: []DomainRule{{Domain: "nflxvideo.net", Subdomains: true}}}, nil, cats)
	if len(conflicts) == 0 || conflicts[0].Blocking || conflicts[0].AppID != "netflix" {
		t.Errorf("taking a domain from a built-in app is noted, not blocked: %+v", conflicts)
	}
	_, conflicts, _ = Default().Check(Rule{ID: "netflix", Builtin: true, Domains: []DomainRule{{Domain: "nflxvideo.net"}}}, nil, cats)
	if len(conflicts) != 0 {
		t.Errorf("a built-in app's own domain is no conflict: %+v", conflicts)
	}
}

// An id is never a built-in app's: the prefix is outside appgen's rule, and a
// taken one is numbered.
func TestCustomIDsAreFree(t *testing.T) {
	for _, a := range Default().Apps() {
		if strings.HasPrefix(a.ID, CustomPrefix) {
			t.Fatalf("built-in app %s has the custom prefix", a.ID)
		}
	}
	if got := freeID("game", []Rule{{ID: "u_game"}, {ID: "u_game-2"}}); got != "u_game-3" {
		t.Errorf("freeID = %s", got)
	}
	// Letters outside a-z break a word, as any other character does.
	if got := slug("  Ünïcode & Co!! "); got != "n-code-co" {
		t.Errorf("slug = %q", got)
	}
}

// A rule alone answers the preview's question.
func TestRuleMatches(t *testing.T) {
	r := Rule{Domains: []DomainRule{{Domain: "a.example", Subdomains: true}, {Domain: "exact.example"}},
		ASNs: []uint{64500}, Ports: []PortRule{{Proto: "udp", From: 3000, To: 3010}}}
	for _, c := range []struct {
		names []string
		asn   uint
		proto string
		port  int
		want  bool
	}{
		{[]string{"x.a.example."}, 0, "tcp", 443, true},
		{[]string{"exact.example"}, 0, "tcp", 443, true},
		{[]string{"x.exact.example"}, 0, "tcp", 443, false},
		{nil, 64500, "tcp", 443, true},
		{nil, 0, "udp", 3005, true},
		{nil, 0, "tcp", 3005, false},
		{nil, 0, "tcp", 443, false},
	} {
		if got := r.Matches(c.names, c.asn, c.proto, c.port); got != c.want {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// A category needs a name not taken, a tone from the list, and gets an id.
func TestCheckCategory(t *testing.T) {
	c, err := CheckCategory(Category{Name: "Kids", Tone: "pink"}, Default().Categories(), nil)
	if err != nil || c.ID != "u_kids" {
		t.Errorf("%+v %v", c, err)
	}
	for _, bad := range []Category{{Name: "Kids", Tone: "#ff0000"}, {Name: "", Tone: "ok"}, {Name: "gaming", Tone: "ok"},
		{Name: "kids", Tone: "ok"}} {
		if _, err := CheckCategory(bad, Default().Categories(), []Category{c}); err == nil {
			t.Errorf("accepted %+v", bad)
		}
	}
}
