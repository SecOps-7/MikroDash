package cfgtpl

import (
	"strings"
	"testing"

	"mikrodash/internal/guard"
	"mikrodash/internal/resource"
)

// menuLine writes one command for a registry menu, in the header form.
func menuLine(menu, cmd string) string {
	return "/" + strings.Join(strings.Split(strings.TrimPrefix(menu, "/"), "/"), " ") + "\n" + cmd + "\n"
}

func codes(fs []Finding) string {
	var c []string
	for _, f := range fs {
		c = append(c, f.Level+":"+f.Code)
	}
	return strings.Join(c, ",")
}

// ── EVERY CODE FIELD, READ FROM THE REGISTRY ────────────────────────────────
//
// Not a hand-picked list: the registry is walked, so a code field added to it
// later is covered without anyone remembering this test exists.
func TestEveryCodeFieldIsRefusedInAnAddition(t *testing.T) {
	n := 0
	for _, r := range resource.All() {
		for _, f := range r.Fields {
			if !f.Code || !argName.MatchString(f.ROS) {
				continue
			}
			n++
			src := menuLine(r.Menu, "add "+f.ROS+`="x"`)
			tp, err := Parse(src)
			if err != nil {
				t.Errorf("%s %s: the probe line does not parse: %v", r.Menu, f.ROS, err)
				continue
			}
			if got := Worst(Analyze(tp, Additions)); got != Refuse {
				t.Errorf("%s %s= is code and an addition setting it is %q, not refused", r.Menu, f.ROS, got)
			}
			if got := Worst(Analyze(tp, Full)); got != Refuse && got != Ack {
				t.Errorf("%s %s= in a full export must at least be acknowledged; got %q", r.Menu, f.ROS, got)
			}
		}
	}
	if n < 10 {
		t.Fatalf("found %d code fields in the registry; the walk is not reaching them", n)
	}
}

func TestEverySecretFieldWarnsWhenWrittenLiterally(t *testing.T) {
	n := 0
	for _, r := range resource.All() {
		for _, f := range r.Fields {
			if f.Type != resource.TypeSecret || !argName.MatchString(f.ROS) {
				continue
			}
			n++
			lit, _ := Parse(menuLine(r.Menu, "set "+f.ROS+`="p4ss"`))
			found := false
			for _, x := range Analyze(lit, Full) {
				if x.Code == "literal-secret" {
					found = true
				}
			}
			if !found {
				t.Errorf("%s %s= written literally is not flagged; it would be stored with the template", r.Menu, f.ROS)
			}
			ph, _ := Parse(menuLine(r.Menu, "set "+f.ROS+"={{pw}}"))
			for _, x := range Analyze(ph, Full) {
				if x.Code == "literal-secret" {
					t.Errorf("%s %s= as a placeholder was flagged as a literal credential", r.Menu, f.ROS)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("found no secret fields in the registry; the walk is not reaching them")
	}
}

// ── MENUS THAT ARE DANGEROUS TO CONFIGURE AT ALL ────────────────────────────

func TestDangerousMenus(t *testing.T) {
	cases := []struct {
		src            string
		addition, full string
	}{
		{"/system script\nadd name=x source=y", Refuse, Ack},
		{"/system scheduler\nadd name=x on-event=y", Refuse, Ack},
		// The menu itself, not only its code field: a scheduler or script with
		// no code set is still one the next edit fills in.
		{"/system script\nadd name=x", Refuse, Ack},
		{"/system scheduler\nadd name=x interval=1m", Refuse, Ack},
		{"/certificate\nadd name=x common-name=x", Refuse, Refuse},
		{"/file\nset [ find name=x ] contents=y", Refuse, Refuse},
		{"/container\nadd remote-image=x", Refuse, Refuse},
		{"/user\nadd name=x group=full", Refuse, Ack},
		{"/user group\nadd name=x policy=read", Refuse, Ack},
		{"/user settings\nset minimum-password-length=12", "", ""},
		{"/ip dns\nset servers=192.0.2.53", "", ""},
	}
	for _, c := range cases {
		tp := mustParse(t, c.src)
		if got := Worst(Analyze(tp, Additions)); got != c.addition {
			t.Errorf("%q as an addition: %q (%s), want %q", c.src, got, codes(Analyze(tp, Additions)), c.addition)
		}
		if got := Worst(Analyze(tp, Full)); got != c.full {
			t.Errorf("%q in a full export: %q, want %q", c.src, got, c.full)
		}
	}
}

// An addition may only touch menus the app knows; a full export may carry any.
func TestAnAdditionMayOnlyTouchKnownMenus(t *testing.T) {
	unknown := mustParse(t, "/some menu nobody knows\nset a=1")
	if got := Worst(Analyze(unknown, Additions)); got != Refuse {
		t.Errorf("an unknown menu in an addition was %q, not refused", got)
	}
	if got := Worst(Analyze(unknown, Full)); got != "" {
		t.Errorf("an unknown menu in a full export was %q; a real export carries menus the app has no page for", got)
	}
	for menu := range extraMenus {
		tp := mustParse(t, menuLine(menu, "set a=1"))
		for _, f := range Analyze(tp, Additions) {
			if f.Code == "unknown-menu" {
				t.Errorf("%s is on the settings allow-list but was refused as unknown", menu)
			}
		}
	}
}

func TestStructuralRefusals(t *testing.T) {
	cases := []struct{ src, want string }{
		{"/ip firewall filter\nset 0 disabled=yes", "refuse:positional"},
		{"/ip firewall filter\nremove", "refuse:no-selector"},
		{"/ip firewall filter\ndisable", "refuse:no-selector"},
		{"/ip firewall filter\nremove [ find ]", "ack:every-row"},
		{"/ip firewall filter\nremove [ find comment=mdcfg:x ]", ""},
	}
	for _, c := range cases {
		got := codes(Analyze(mustParse(t, c.src), Additions))
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.src, got, c.want)
		}
	}
	// A row number is how an export names a default row, so a full export may.
	if got := codes(Analyze(mustParse(t, "/ip firewall filter\nset 0 disabled=yes"), Full)); got != "" {
		t.Errorf("a positional in a full export: %q, want nothing", got)
	}
}

// ── LIVE: AGAINST ONE ROUTER'S MANAGEMENT PATH ──────────────────────────────

// lab is a resolved management path: MikroDash at 192.0.2.9, arriving on
// ether1, over api-ssl on 8729.
var lab = LiveContext{
	FW:         guard.FWContext{Resolved: true, Addresses: []string{"192.0.2.9"}, Interfaces: []string{"ether1"}, APIPort: 8729},
	APIService: "api-ssl",
}

func TestFirewallLockout(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"a bare drop matches everything", "add chain=input action=drop", "ack:lockout-firewall"},
		{"a reject counts too", "add chain=input action=reject", "ack:lockout-firewall"},
		{"on our port", "add chain=input action=drop protocol=tcp dst-port=8729", "ack:lockout-firewall"},
		{"on our interface", "add chain=input action=drop in-interface=ether1", "ack:lockout-firewall"},
		{"not TCP", "add chain=input action=drop protocol=udp dst-port=53", ""},
		{"not our port", "add chain=input action=drop protocol=tcp dst-port=22", ""},
		{"not our source", "add chain=input action=drop src-address=198.51.100.0/24", ""},
		{"not our interface", "add chain=input action=drop in-interface=ether9", ""},
		{"an accept", "add chain=input action=accept", ""},
		{"the forward chain", "add chain=forward action=drop", ""},
		{"disabled", "add chain=input action=drop disabled=yes", ""},
	}
	for _, c := range cases {
		tp := mustParse(t, "/ip firewall filter\n"+c.line)
		if got := codes(AnalyzeLive(tp, lab)); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	raw := mustParse(t, "/ip firewall raw\nadd chain=prerouting action=drop")
	if got := codes(AnalyzeLive(raw, lab)); got != "ack:lockout-firewall" {
		t.Errorf("a raw prerouting drop: %q", got)
	}
	// NOT KNOWING OUR ADDRESS IS NOT A PASS.
	unknown := LiveContext{FW: guard.FWContext{Resolved: false}, APIService: "api-ssl"}
	if got := codes(AnalyzeLive(mustParse(t, "/ip firewall filter\nadd chain=input action=drop src-address=198.51.100.0/24"), unknown)); got != "ack:lockout-unknown" {
		t.Errorf("with our address unresolved, a drop was %q; it must say it cannot tell", got)
	}
}

func TestServiceLockout(t *testing.T) {
	cases := []struct{ line, want string }{
		{"set [ find name=api-ssl ] disabled=yes", "refuse:own-service"},
		{"set [ find ] disabled=yes", "refuse:own-service"},
		{"disable [ find name=api-ssl ]", "refuse:own-service"},
		{"set [ find name=api-ssl ] port=9999", "refuse:own-service"},
		{"set [ find name=api-ssl ] address=192.0.2.0/24", "ack:own-service-address"},
		{"set [ find name=winbox ] disabled=yes", "ack:recovery-path"},
		{"set [ find name=ssh ] disabled=yes", "ack:recovery-path"},
		{"set [ find name=telnet ] disabled=yes", ""},
		{"set [ find name=api ] disabled=yes", ""}, // not the one MikroDash uses here
	}
	for _, c := range cases {
		tp := mustParse(t, "/ip service\n"+c.line)
		if got := codes(AnalyzeLive(tp, lab)); got != c.want {
			t.Errorf("%q: %q, want %q", c.line, got, c.want)
		}
	}
}

func TestInterfacesAndTheBridge(t *testing.T) {
	cases := []struct{ src, want string }{
		{"/interface bridge\nset [ find name=bridge ] vlan-filtering=yes", "ack:vlan-filtering"},
		{"/interface ethernet\nset [ find name=ether1 ] disabled=yes", "ack:lockout-interface"},
		{"/interface ethernet\ndisable [ find name=ether1 ]", "ack:lockout-interface"},
		{"/interface ethernet\nset [ find name=ether9 ] disabled=yes", ""},
		{"/ip address\nremove [ find address=192.0.2.9/24 ]", "ack:lockout-address"},
	}
	for _, c := range cases {
		if got := codes(AnalyzeLive(mustParse(t, c.src), lab)); got != c.want {
			t.Errorf("%q: %q, want %q", c.src, got, c.want)
		}
	}
}

func TestWorst(t *testing.T) {
	if Worst(nil) != "" {
		t.Error("no findings should be no level")
	}
	fs := []Finding{{Level: Warn}, {Level: Refuse}, {Level: Ack}}
	if Worst(fs) != Refuse {
		t.Errorf("Worst = %q, want refuse", Worst(fs))
	}
}
