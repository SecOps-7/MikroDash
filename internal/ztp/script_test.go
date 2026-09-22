package ztp

import (
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden scripts")

// Fixed inputs, so the output is stable. The keys are shaped like keys and are
// no one's: a repeated letter plus the padding.
var (
	fakeKey  = strings.Repeat("A", 42) + "E="
	fakeKey2 = strings.Repeat("B", 42) + "E="
	inst     = Instance{ID: "inst-0123", PublicKey: fakeKey, Endpoint: "vpn.example.net", Port: 13231,
		Server: netip.MustParseAddr("10.249.0.1")}
	expires = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
)

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from its golden file; if the change is meant, re-run with -update and review the diff", name)
	}
}

func scripts() map[string]string {
	return map[string]string{
		"remote.rsc": RemoteScript(inst, Remote{Label: "Branch 12", Token: "tok-remote", PrivateKey: fakeKey2,
			Address: netip.MustParseAddr("10.249.1.7"), Expires: expires}),
		"local.rsc": LocalScript(inst, Local{Label: "Office", Token: "tok-local", Password: "pw-FakeFakeFake",
			From: netip.MustParseAddr("192.0.2.10"), URL: "http://192.0.2.10:3081/", Expires: expires}),
		"generic.rsc": GenericScript(inst, Batch{Token: "tok-batch", PrivateKey: fakeKey2,
			Enrolment: netip.MustParsePrefix("10.249.255.0/24"), Expires: expires}),
	}
}

// The remote script is the one run end to end on the lab CHR (cmd/ztpprobe
// z10): a change to it should be read in its diff.
func TestTheBootstrapScriptsAreStable(t *testing.T) {
	for name, s := range scripts() {
		golden(t, name, s)
	}
}

// WHAT WAS MEASURED THE HARD WAY STAYS TRUE, in every script.
func TestTheScriptsKeepWhatWasMeasured(t *testing.T) {
	for name, s := range scripts() {
		// z8: a WireGuard interface given a port another holds comes up
		// disabled and silent. The bootstrap never names a port.
		if strings.Contains(s, "listen-port") {
			t.Errorf("%s names a listen port", name)
		}
		// z10: `get [find …]` on a service with several entries fails the whole
		// import. Every service read walks ids.
		if regexp.MustCompile(`/ip service\n[^/]*get \[find`).MatchString(s) {
			t.Errorf("%s reads a service with get [find …]", name)
		}
		// The user is never the one a router managed by hand already has.
		if strings.Contains(s, `name="mikrodash"`) || UserName == "mikrodash" {
			t.Errorf("%s uses the user name a hand-managed router already has", name)
		}
		// Every interface created is also explicitly enabled (z8: created over
		// the API it can come up disabled).
		// Interface adds are the guarded `do={ add name=…` form; the enrolment
		// script and its scheduler are plain adds and are not interfaces.
		// The user add shares the form and the name prefix; an interface add is
		// the one followed by an optional private key and then its comment.
		adds := regexp.MustCompile(`do=\{ add name=("mikrodash-ztp[^"]*")( private-key="[^"]*")? comment=`).FindAllStringSubmatch(s, -1)
		for _, m := range adds {
			if !strings.Contains(s, "set [find name="+m[1]+"] disabled=no") {
				t.Errorf("%s creates %s and never enables it", name, m[1])
			}
		}
		if len(adds) == 0 && name != "local.rsc" {
			t.Errorf("%s creates no tunnel interface", name)
		}
		if !strings.Contains(s, "THIS FILE HOLDS A SECRET") {
			t.Errorf("%s does not say it holds a secret", name)
		}
	}
}

// NOTHING SUPPLIED ESCAPES ITS QUOTES. A label cannot leave its comment line,
// and the text of a hostile value reaches the script only inside a literal,
// where its quote is escaped, so the `"` that would end the literal early never
// appears unescaped before it.
func TestNoSuppliedValueEscapesItsQuotes(t *testing.T) {
	hostileLabel := "x\n/system reset-configuration\n# "
	hostileToken := `t"; /user remove [find]; :put "`
	hostileHost := `evil"; /system reboot; "`
	in := Instance{ID: "id\"$(x)", PublicKey: fakeKey, Endpoint: hostileHost, Port: 13231, Server: inst.Server}
	rd := Remote{Label: hostileLabel, Token: hostileToken, PrivateKey: fakeKey2,
		Address: netip.MustParseAddr("10.249.1.7"), Expires: expires}
	ld := Local{Label: hostileLabel, Token: hostileToken, Password: `p"$[x]`,
		From: netip.MustParseAddr("192.0.2.10"), URL: `http://192.0.2.10:3081/"; /system reboot; "`, Expires: expires}
	bd := Batch{Token: hostileToken, PrivateKey: fakeKey2, Enrolment: netip.MustParsePrefix("10.249.255.0/24"), Expires: expires}
	// BOTH LAYERS: the file, and the enrolment script as the router stores and
	// runs it. The second is what matters for a value only it carries (the
	// token): quoted again to embed it, a value it forgot to quote would be
	// escaped in the file and live once stored.
	for name, s := range map[string]string{
		"remote": RemoteScript(in, rd), "remote enrolment": remoteEnrol(in, rd),
		"local": LocalScript(in, ld), "local enrolment": localEnrol(in, ld),
		"generic": GenericScript(in, bd), "generic enrolment": genericEnrol(in, bd),
	} {
		// COMMENT LINES ARE NEVER EVALUATED, and oneLine keeps a value on its
		// line, so only the code is checked; the label's own line is checked for
		// having stayed a comment.
		var code []string
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "/system reset-configuration") {
				t.Errorf("%s: a label broke out of its comment into a command", name)
			}
			if !strings.HasPrefix(line, "#") {
				code = append(code, line)
			}
		}
		if unescaped.MatchString(strings.Join(code, "\n")) {
			t.Errorf("%s: a supplied value ended its literal early", name)
		}
	}
	// The control: the same check fires on the text written unescaped, so a
	// pass above is not the check finding nothing to look at.
	if !unescaped.MatchString(`add x="` + hostileToken + `"`) {
		t.Fatal("the check does not fire on an unescaped value")
	}
}

// unescaped is a supplied value's dangerous text after a quote that is NOT
// escaped (`\"` is the escaped form, and fine): the literal ended early.
var unescaped = regexp.MustCompile(`(^|[^\\])"; (/user remove|/system reboot)|(^|[^\\])\$\(x\)`)

func init() {
	// Keep the regexp honest about the escaped form it must NOT match.
	if unescaped.MatchString(`add x="t\"; /user remove [find]"`) {
		panic("unescaped matches an escaped quote")
	}
}
