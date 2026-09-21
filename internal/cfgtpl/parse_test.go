package cfgtpl

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// realShape is a synthetic export in the real dialect, modelled on a test
// router's own `/export`: space-form headers; a `\` continuation that wraps
// AFTER an `=` so the value is on the next line; one that falls INSIDE a quoted
// string mid-word (`!reboo\` + `t`); a `[ find ]` selector; and the identifying
// comments an export opens with. Every value is invented; every address is
// TEST-NET.
const realShape = `# 2026-09-21 12:00:00 by RouterOS 7.24.4
# software id = TEST-0000
#
# model = RB0000
# serial number = 000000000000
/interface ethernet
set [ find default-name=ether1 ] l2mtu=1596
/ip firewall filter
add action=accept chain=input comment=\
    "defconf: accept established,related,untracked" connection-state=\
    established,related,untracked
add action=drop chain=input comment="defconf: drop all not coming from LAN" \
    in-interface-list=!LAN
/user group
add name=ro policy="read,write,test,api,!local,!telnet,!ssh,!ftp,!reboo\
    t,!policy"
/system identity
set name=edge-01
`

// shape renders a parse as comparable text, ignoring line numbers and whether
// a value was quoted — the two things Format is free to change.
func shape(t *Template) string {
	var b strings.Builder
	val := func(v Value) string {
		var p []string
		for _, x := range v.Parts {
			if x.Var != "" {
				p = append(p, "V:"+x.Var)
			} else {
				p = append(p, fmt.Sprintf("L:%q", x.Lit))
			}
		}
		return "[" + strings.Join(p, " ") + "]"
	}
	for _, l := range t.Lines {
		fmt.Fprintf(&b, "%s %s", l.Path(), l.Verb)
		if l.HasFind {
			b.WriteString(" find{")
			for _, a := range l.Find {
				b.WriteString(" " + a.Name + "=" + val(a.Value))
			}
			b.WriteString(" }")
		}
		for _, v := range l.Pos {
			b.WriteString(" pos=" + val(v))
		}
		for _, a := range l.Args {
			b.WriteString(" " + a.Name + "=" + val(a.Value))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func mustParse(t *testing.T, src string) *Template {
	t.Helper()
	tp, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return tp
}

// ── THE REAL SHAPE ──────────────────────────────────────────────────────────

func TestParsesARealExport(t *testing.T) {
	got := shape(mustParse(t, realShape))
	want := `/interface/ethernet set find{ default-name=[L:"ether1"] } l2mtu=[L:"1596"]
/ip/firewall/filter add action=[L:"accept"] chain=[L:"input"] comment=[L:"defconf: accept established,related,untracked"] connection-state=[L:"established,related,untracked"]
/ip/firewall/filter add action=[L:"drop"] chain=[L:"input"] comment=[L:"defconf: drop all not coming from LAN"] in-interface-list=[L:"!LAN"]
/user/group add name=[L:"ro"] policy=[L:"read,write,test,api,!local,!telnet,!ssh,!ftp,!reboot,!policy"]
/system/identity set name=[L:"edge-01"]
`
	if got != want {
		t.Errorf("parse of a real export's shape is wrong.\n got:\n%s\nwant:\n%s", got, want)
	}
}

// A backslash continues a TOKEN only inside a string; outside one it is a word
// break. `a=1\` + `b=2` is two arguments, and `name=\` + `value` is one.
func TestContinuationFollowsTheManual(t *testing.T) {
	cases := []struct{ src, want string }{
		{"/ip dns\nset a=1\\\nb=2\n", `/ip/dns set a=[L:"1"] b=[L:"2"]` + "\n"},
		{"/ip dns\nset servers=\\\n    192.0.2.1\n", `/ip/dns set servers=[L:"192.0.2.1"]` + "\n"},
		{"/ip dns\nset comment=\"ab\\\n    cd\"\n", `/ip/dns set comment=[L:"abcd"]` + "\n"},
		{"/ip dns\nset comment=\\\n    \"x y\"\n", `/ip/dns set comment=[L:"x y"]` + "\n"},
	}
	for _, c := range cases {
		got := shape(mustParse(t, c.src))
		if got != c.want {
			t.Errorf("%q\n got %s\nwant %s", c.src, got, c.want)
		}
	}
}

// The identifying comments an export opens with never reach Format's output.
func TestFormatDropsTheIdentifyingComments(t *testing.T) {
	out := Format(mustParse(t, realShape))
	for _, w := range []string{"software id", "serial number", "model =", "#"} {
		if strings.Contains(out, w) {
			t.Errorf("Format kept %q:\n%s", w, out)
		}
	}
}

// ── WHAT IS REFUSED ─────────────────────────────────────────────────────────

func TestRefusesWhatIsNotConfiguration(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"scripting", ":if (1=1) do={ /system reboot }", "scripting"},
		{"local", ":local x 5", "scripting"},
		{"dollar outside quotes", "/ip dns\nset servers=$srv", "'$'"},
		{"dollar inside quotes", "/ip dns\nset comment=\"$[/system reboot]\"", "'$'"},
		{"semicolon", "/ip dns\nset a=1; /system reboot", "';'"},
		{"command substitution", "/ip dns\nset a=[/system reboot]", "[ ]"},
		{"nested brackets", "/ip dns\nset [ find name=[/system reboot] ] a=1", "inside a [ ]"},
		{"where", "/ip dns static\nremove [ find where name=x ]", "'where'"},
		{"not find", "/ip dns static\nremove [ print ]", "only [ find"},
		{"unknown verb", "/system\nreboot", "not a command"},
		{"export verb", "/ip firewall filter\nexport", "not a command"},
		{"backtick", "/ip dns\nset a=`x`", "backtick"},
		{"lone backslash", "/ip dns\nset a=\\x", "backslash"},
		{"script block", "/ip dns\nset a={x}", "placeholder"},
		{"empty unquoted", "/ip dns\nset comment=", "no value"},
		{"ambiguous empty", "/ip dns\nset comment= servers=1", "another setting"},
		{"hash in bare", "/ip dns\nset comment=a#b", "'#'"},
		{"unterminated", "/ip dns\nset comment=\"abc", "not closed"},
		{"comment continues", "/ip dns\n# a comment \\\nset a=1", "cannot continue"},
		{"no menu", "set a=1", "before any menu"},
		{"unknown escape", "/ip dns\nset comment=\"a\\qb\"", "not a RouterOS escape"},
		{"lowercase hex", "/ip dns\nset comment=\"\\c3\"", "not a RouterOS escape"},
		{"raw control", "/ip dns\nset comment=\"a\x01b\"", "control"},
		{"non-ascii bare", "/ip dns\nset comment=café", "ASCII"},
		{"bidi override", "/ip dns\nset comment=\"a\u202eb\"", "direction"},
		{"zero width", "/ip dns\nset comment=\"a\u200bb\"", "invisible"},
		{"find on add", "/ip dns static\nadd [ find name=x ] name=y", "only goes with"},
	}
	for _, c := range cases {
		_, err := Parse(c.src)
		if err == nil {
			t.Errorf("%s: %q was accepted", c.name, c.src)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: refused, but for the wrong reason: %v (want it to mention %q)", c.name, err, c.want)
		}
	}
}

// ── PLACEHOLDERS ────────────────────────────────────────────────────────────

func TestPlaceholderPositions(t *testing.T) {
	ok := []string{
		"/ip dns\nset servers={{dns}}",
		"/ip dns\nset comment=\"site {{site}} dns\"",
		"/ip dns static\nset [ find name={{host}} ] address=192.0.2.1",
		"/ip dns static\nset [ find name=\"a {{host}}\" ] address=192.0.2.1",
	}
	for _, src := range ok {
		if _, err := Parse(src); err != nil {
			t.Errorf("%q should parse: %v", src, err)
		}
	}
	bad := []string{
		"/ip dns\nset servers=10.{{x}}.0.1", // part of a bare value
		"/ip {{menu}}\nset a=1",             // a menu
		"/ip dns\n{{verb}} a=1",             // a verb
		"/ip dns\nset {{name}}=1",           // a property name
		"/ip dns\nset a={{Bad}}",            // not a variable name
		"/ip dns\nset a=\"{{x\"",            // not closed
		"/ip dns\nset a=\"{{1x}}\"",         // not a variable name
	}
	for _, src := range bad {
		if _, err := Parse(src); err == nil {
			t.Errorf("%q should be refused", src)
		}
	}
}

func TestVarsAndMenus(t *testing.T) {
	tp := mustParse(t, "/ip dns\nset servers={{dns}} comment=\"{{site}} {{dns}}\"\n/system identity\nset name={{site}}")
	if got := strings.Join(tp.Vars(), ","); got != "dns,site" {
		t.Errorf("Vars = %s, want dns,site (first appearance first, each once)", got)
	}
	if got := strings.Join(tp.Menus(), ","); got != "/ip/dns,/system/identity" {
		t.Errorf("Menus = %s", got)
	}
}

// ── ESCAPES ─────────────────────────────────────────────────────────────────

// Every documented escape decodes to what the manual says it does.
func TestEscapesDecodeAsDocumented(t *testing.T) {
	src := "/ip dns\nset comment=\"q\\\" b\\\\ d\\$ n\\n r\\r t\\t s\\_ a\\a b\\b f\\f v\\v h\\48\\49\"\n"
	v, _ := mustParse(t, src).Lines[0].Arg("comment")
	got, _ := v.Literal()
	want := "q\" b\\ d$ n\n r\r t\t s  a\a b\b f\f v\v hHI"
	if got != want {
		t.Errorf("decoded %q, want %q", got, want)
	}
}

// ── ROUND TRIP ──────────────────────────────────────────────────────────────

// What Format writes, Parse reads back as the same structure. This is what
// makes a stored template safe to edit and re-save.
func TestFormatRoundTrips(t *testing.T) {
	srcs := []string{
		realShape,
		"/ip dns\nset servers={{dns}} comment=\"site {{site}}: \\\"x\\\" \\$5 [a] {b}\"",
		"/ip dns static\nset [ find name={{host}} ] address=192.0.2.1 comment=\"\"",
		"/ipsec policy\nset 0 protocol=gre",
		"/interface list\nensure name=WAN\n/interface list member\nensure list=WAN interface={{wan}}",
		"/ip dns\nset comment=\"caf\\C3\\A9 \\7B\\7Bnot a var\\7D\\7D\"",
	}
	for _, src := range srcs {
		first := mustParse(t, src)
		out := Format(first)
		second, err := Parse(out)
		if err != nil {
			t.Errorf("Format's own output does not parse: %v\n%s", err, out)
			continue
		}
		if a, b := shape(first), shape(second); a != b {
			t.Errorf("round trip changed the template.\nbefore:\n%s\nafter:\n%s\nvia:\n%s", a, b, out)
		}
	}
}

// ── RENDER ──────────────────────────────────────────────────────────────────

func TestRenderRefusesWhatIsNotFinished(t *testing.T) {
	tp := mustParse(t, "/ip dns\nset servers={{dns}}")
	if _, err := Render(tp, map[string]string{}); err == nil {
		t.Error("a placeholder with no value was rendered; it must be refused, never sent as empty")
	}
	en := mustParse(t, "/interface list\nensure name=WAN")
	if _, err := Render(en, nil); err == nil {
		t.Error("an ensure line was rendered; it must be resolved against the router first")
	}
}

// ── THE PROPERTY THAT MATTERS: A VALUE IS ONE INERT LITERAL ──────────────────
//
// Whatever a variable holds, Render must write it as exactly one RouterOS value
// that decodes back to those same bytes, on the one line it belongs to.
//
// THE DECODER BELOW IS WRITTEN FROM THE MANUAL, NOT FROM THIS PACKAGE. A check
// that used cfgtpl's own parser to read cfgtpl's own output would agree with
// itself whatever it did; this one knows only what RouterOS does with a string.

func TestRenderedValueIsOneInertLiteral(t *testing.T) {
	tp := mustParse(t, "/ip dns static\nadd name=p.test comment={{c}}")
	hostile := []string{
		`x" ; /user add name=evil group=full password=p ; :put "`,
		`abc\`,
		"10.0.0.0/24\n/system reboot",
		"x\r/system reset-configuration",
		`$[/system reboot]`,
		`$(/system reboot)`,
		`[/system reboot]`,
		`$foo`,
		`{{c}}`,
		`}}{{`,
		`#not a comment`,
		`;`,
		`?`,
		`"`,
		`\`,
		`\\\\`,
		"",
		"café",
		"\u202e reversed",
		strings.Repeat("A", 4000),
	}
	var all strings.Builder
	for i := 0; i < 256; i++ {
		all.WriteByte(byte(i))
	}
	hostile = append(hostile, all.String())
	r := rand.New(rand.NewSource(1))
	alphabet := []byte("\"\\$[]{};#?`'\n\r\t ab=/!:\x00\x7f\xff")
	for i := 0; i < 5000; i++ {
		n := r.Intn(24)
		b := make([]byte, n)
		for j := range b {
			b[j] = alphabet[r.Intn(len(alphabet))]
		}
		hostile = append(hostile, string(b))
	}

	for _, v := range hostile {
		out, err := Render(tp, map[string]string{"c": v})
		if err != nil {
			t.Fatalf("Render refused %q: %v", v, err)
		}
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) != 2 || lines[0] != "/ip dns static" {
			t.Fatalf("value %q produced %d lines, not a header and one command:\n%s", v, len(lines), out)
		}
		const lead = "add name=p.test comment="
		if !strings.HasPrefix(lines[1], lead) {
			t.Fatalf("value %q disturbed the command before it: %q", v, lines[1])
		}
		got, rest, ok := manualDecode(lines[1][len(lead):])
		if !ok {
			t.Fatalf("value %q is not one well-formed RouterOS value: %q", v, lines[1])
		}
		if rest != "" {
			t.Fatalf("value %q left text after itself — something escaped the literal: %q", v, rest)
		}
		if got != v {
			t.Fatalf("value %q decoded as %q: not the same bytes", v, got)
		}
	}
}

// manualDecode reads ONE value from the start of s by the scripting manual's
// rules ("Constant Escape Sequences"; "hex numbers should use capital
// letters"), and returns its bytes and whatever follows it.
func manualDecode(s string) (string, string, bool) {
	if s == "" {
		return "", "", false
	}
	if s[0] != '"' {
		i := strings.IndexAny(s, " \t")
		if i < 0 {
			i = len(s)
		}
		w := s[:i]
		// A bare word may not hold anything the console would act on.
		if strings.ContainsAny(w, "\"\\$[]{};#?`=") {
			return "", "", false
		}
		return w, s[i:], true
	}
	one := map[byte]byte{'"': '"', '\\': '\\', '$': '$', 'n': '\n', 'r': '\r',
		't': '\t', '_': ' ', 'a': 7, 'b': 8, 'f': 12, 'v': 11}
	hex := func(c byte) (byte, bool) {
		switch {
		case c >= '0' && c <= '9':
			return c - '0', true
		case c >= 'A' && c <= 'F':
			return c - 'A' + 10, true
		}
		return 0, false
	}
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			return b.String(), s[i+1:], true
		case '$':
			return "", "", false // unescaped: it would expand
		case '\\':
			if i+1 >= len(s) {
				return "", "", false
			}
			if d, ok := one[s[i+1]]; ok {
				b.WriteByte(d)
				i++
				continue
			}
			if i+2 < len(s) {
				hi, ok1 := hex(s[i+1])
				lo, ok2 := hex(s[i+2])
				if ok1 && ok2 {
					b.WriteByte(hi<<4 | lo)
					i += 2
					continue
				}
			}
			return "", "", false
		default:
			if c < 0x20 || c >= 0x7f {
				return "", "", false // a raw control or non-ASCII byte in the text
			}
			b.WriteByte(c)
		}
	}
	return "", "", false // never closed
}

// ── SPLITTING ───────────────────────────────────────────────────────────────

func TestSplitPartsStandAlone(t *testing.T) {
	var src strings.Builder
	src.WriteString("/ip dns static\n")
	for i := 0; i < 400; i++ {
		fmt.Fprintf(&src, "add name=host-%d.test address=192.0.2.%d\n", i, i%250+1)
	}
	src.WriteString("/system identity\nset name=edge\n")
	out, err := Render(mustParse(t, src.String()), nil)
	if err != nil {
		t.Fatal(err)
	}
	const max = 2000
	parts, err := SplitParts(out, max)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) < 2 {
		t.Fatalf("expected several parts, got %d", len(parts))
	}
	var commands int
	for i, p := range parts {
		if len(p) > max {
			t.Errorf("part %d is %d bytes, over %d", i, len(p), max)
		}
		if !strings.HasPrefix(p, "/") {
			t.Errorf("part %d does not open with a menu header, so it cannot be imported alone:\n%.80s", i, p)
		}
		// Each part must itself be a template that parses.
		tp, err := Parse(p)
		if err != nil {
			t.Errorf("part %d does not parse on its own: %v", i, err)
			continue
		}
		commands += len(tp.Lines)
	}
	if commands != 401 {
		t.Errorf("the parts carry %d commands, want 401 — a command was lost or doubled", commands)
	}
	if _, err := SplitParts("/ip dns\nset comment="+strings.Repeat("x", 300)+"\n", 100); err == nil {
		t.Error("a command longer than one part was split or passed; it must be refused")
	}
}
