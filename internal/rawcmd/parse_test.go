package rawcmd

import "testing"

// TestTheParserRefusesEverythingItDoesNotRecognise.
//
// This is the one path in the app where a string a MODEL wrote becomes a router
// command, so the table is written as the attack list it is: each entry is a way
// of turning one command into two, into a substitution, or into a script. A
// parser that accepted any of these would hand the router something nobody
// declared and nobody could review.
//
// Each refusal is paired with the ACCEPTED command it is a variant of, so an
// entry cannot pass because the parser refuses everything.
func TestTheParserRefusesEverythingItDoesNotRecognise(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"a second command after a newline", "/ip/address/print\n/user/remove .id=*1"},
		{"a carriage return", "/ip/address/print\r/user/remove .id=*1"},
		{"a second command after a semicolon", "/ip/address/print; /user/remove .id=*1"},
		{"a semicolon inside a value", `/ip/dns/static/add name=a comment="x; /user/remove .id=*1"`},
		{"a script command", ":do { /user/remove .id=*1 }"},
		{"a script word after the menu", "/system/script/run :put 1"},
		{"command substitution", "/user/remove [find name=mikrodash]"},
		{"substitution inside a value", `/ip/dns/static/add name=a comment=[find]`},
		{"a script block", "/system/scheduler/add on-event={/user/remove .id=*1}"},
		{"a variable", "/ip/address/print where=$x"},
		{"a backtick", "/ip/address/print `x`"},
		{"a backslash escape", `/ip/dns/static/add name=a comment="x\" ; y"`},
		{"a control character", "/ip/address/print\x07"},
		{"a NUL", "/ip/address/print\x00"},
		{"no menu path", "print"},
		{"a menu with no verb", "/ip/address"},
		{"a positional row number", "/interface/set 0 disabled=yes"},
		{"a bare word", "/ip/address/print detail"},
		{"an upper-case menu", "/IP/address/print"},
		{"a menu that starts with a digit", "/2ip/address/print"},
		{"a verb this app never sends", "/system/reset-configuration"},
		{"a verb that is really a menu", "/ip/firewall/filter"},
		{"an argument with no name", "/ip/address/add =10.0.0.1/24"},
		{"an argument name with a space", "/ip/address/add the name=x"},
		{"an unclosed quote", `/ip/dns/static/add name="x`},
		{"a quote inside a value", `/ip/dns/static/add name=a"b`},
		{"nothing at all", "   "},
	} {
		if cmd, err := Parse(tc.in); err == nil {
			t.Errorf("%s was ACCEPTED as %s %v", tc.name, cmd.APIPath(), cmd.Words)
		}
	}
}

// TestTheParserAcceptsAnOrdinaryCommand — the control. Without these the table
// above would pass against a parser that refused everything, including the
// commands this feature exists to run.
func TestTheParserAcceptsAnOrdinaryCommand(t *testing.T) {
	for _, tc := range []struct {
		name, in   string
		menu, verb string
		words      []string
		text       string
	}{
		{"a read", "/ip/address/print", "/ip/address", "print", []string{}, "/ip/address/print"},
		{"a read with whitespace around it", "  /system/resource/print  ",
			"/system/resource", "print", []string{}, "/system/resource/print"},
		{"a one-level menu", "/user/print", "/user", "print", []string{}, "/user/print"},
		{"a hyphenated menu and verb", "/system/package/apply-changes",
			"/system/package", "apply-changes", []string{}, "/system/package/apply-changes"},
		{"an add with arguments", "/ip/firewall/filter/add chain=input action=accept",
			"/ip/firewall/filter", "add", []string{"=chain=input", "=action=accept"},
			"/ip/firewall/filter/add chain=input action=accept"},
		{"a set addressing one row", "/interface/set .id=*3 disabled=no",
			"/interface", "set", []string{"=.id=*3", "=disabled=no"},
			"/interface/set .id=*3 disabled=no"},
		{"a quoted value with spaces", `/ip/dns/static/add name=db comment="from the office"`,
			"/ip/dns/static", "add", []string{"=name=db", "=comment=from the office"},
			"/ip/dns/static/add name=db comment=from the office"},
		{"a value with an = in it", "/ip/address/add address=10.0.0.1/24 comment=a=b",
			"/ip/address", "add", []string{"=address=10.0.0.1/24", "=comment=a=b"},
			"/ip/address/add address=10.0.0.1/24 comment=a=b"},
		{"an empty value", "/ip/dns/static/set .id=*1 comment=",
			"/ip/dns/static", "set", []string{"=.id=*1", "=comment="},
			"/ip/dns/static/set .id=*1 comment="},
	} {
		cmd, err := Parse(tc.in)
		if err != nil {
			t.Errorf("%s was refused: %v", tc.name, err)
			continue
		}
		if cmd.Menu != tc.menu || cmd.Verb != tc.verb {
			t.Errorf("%s parsed as %q + %q, want %q + %q", tc.name, cmd.Menu, cmd.Verb, tc.menu, tc.verb)
		}
		if len(cmd.Words) != len(tc.words) {
			t.Errorf("%s parsed %v, want %v", tc.name, cmd.Words, tc.words)
			continue
		}
		for i := range tc.words {
			if cmd.Words[i] != tc.words[i] {
				t.Errorf("%s argument %d is %q, want %q", tc.name, i, cmd.Words[i], tc.words[i])
			}
		}
		if cmd.Text != tc.text {
			t.Errorf("%s records %q, want %q", tc.name, cmd.Text, tc.text)
		}
	}
}

// TestTheRecordedTextIsRebuiltRatherThanEchoed. The audit trail must carry what
// the server understood, not the string it was handed: the two differ exactly
// where the difference matters — surrounding whitespace, quotes, the spacing
// between arguments.
func TestTheRecordedTextIsRebuiltRatherThanEchoed(t *testing.T) {
	in := `   /ip/dns/static/add    name=db   comment="from the office"   `
	cmd, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Text == in {
		t.Error("the recorded text is the raw input")
	}
	if cmd.Text != "/ip/dns/static/add name=db comment=from the office" {
		t.Errorf("recorded %q", cmd.Text)
	}
}

// TestOnlyDeclaredVerbsAreBuilt. The allow-list is what stops `/ip/address`
// parsing as the menu `/ip` with the verb `address`, and it is also the narrower
// gate: a verb no page issues is refused here rather than sent for the router to
// judge.
func TestOnlyDeclaredVerbsAreBuilt(t *testing.T) {
	if _, err := Parse("/system/reset-configuration"); err == nil {
		t.Error("a verb this app never sends was accepted")
	}
	if _, err := Parse("/ip/address/print"); err != nil {
		t.Errorf("a declared verb was refused: %v", err)
	}
}

// TestReadVerbsAreNamed. The executor confirms every command either way, so this
// only decides how one is DESCRIBED — but a write described as a read is the
// wrong sentence in front of somebody about to approve it.
func TestReadVerbsAreNamed(t *testing.T) {
	for _, v := range []string{"print", "get", "export", "monitor"} {
		if !IsReadVerb(v) {
			t.Errorf("%q is not recognised as a read", v)
		}
	}
	for _, v := range []string{"add", "set", "remove", "enable", "disable", "move",
		"apply-changes", "upgrade", "reboot", "reset-configuration"} {
		if IsReadVerb(v) {
			t.Errorf("%q is treated as a read", v)
		}
	}
}
