package cfgtpl

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"testing"

	"mikrodash/internal/guard"
)

// Every canned template is a template an operator could have written, held to
// the same rules, and deployable as an addition.
func TestEveryCannedTemplateIsDeployable(t *testing.T) {
	if len(CannedTemplates()) < 12 {
		t.Fatalf("the library holds %d templates", len(CannedTemplates()))
	}
	seen := map[string]bool{}
	for _, c := range CannedTemplates() {
		if seen[c.ID] {
			t.Errorf("%s appears twice", c.ID)
		}
		seen[c.ID] = true
		if c.Name == "" || c.Description == "" || c.Version < 1 {
			t.Errorf("%s: name, description and version are required", c.ID)
		}
		tp, err := Parse(c.Body)
		if err != nil {
			t.Errorf("%s does not parse: %v", c.ID, err)
			continue
		}
		if err := ValidateDefs(c.Variables); err != nil {
			t.Errorf("%s: %v", c.ID, err)
		}
		if err := CheckDeclared(tp, c.Variables); err != nil {
			t.Errorf("%s: %v", c.ID, err)
		}
		for _, f := range Analyze(tp, Additions) {
			if f.Level == Refuse {
				t.Errorf("%s line %d is refused: %s", c.ID, f.Line, f.Message)
			}
		}
		// Every default is a value its own type accepts.
		for _, d := range c.Variables {
			if d.Default != "" {
				if _, err := Validate(d, d.Default); err != nil {
					t.Errorf("%s: {{%s}}'s default %q: %v", c.ID, d.Name, d.Default, err)
				}
			}
		}
	}
	// Every body file is in the manifest, and the reverse.
	files, _ := fs.Glob(cannedFS, "canned/*.rsc")
	for _, f := range files {
		id := strings.TrimSuffix(strings.TrimPrefix(f, "canned/"), ".rsc")
		if !seen[id] {
			t.Errorf("%s has a body but no manifest entry", id)
		}
	}
	if len(files) != len(seen) {
		t.Errorf("%d bodies for %d manifest entries", len(files), len(seen))
	}
}

// Every row a canned template adds carries its tag, and the template removes
// its own tagged rows first, so applying it twice leaves one copy.
//
// A template deployed once PER INSTANCE (a VLAN per VLAN id) tags each
// instance on its own, `mdcfg:<id>:{{var}}`: one tag for all of them would
// make deploying VLAN 20 remove VLAN 10. Its removes must then name that same
// instance, or re-applying it would leave the old copy behind.
func TestCannedTemplatesAreIdempotent(t *testing.T) {
	for _, c := range CannedTemplates() {
		tp, _ := Parse(c.Body)
		tag := "mdcfg:" + c.ID
		// instance is the tag a value holds: the plain tag, or the tag with a
		// per-instance placeholder, written as the template spells it.
		instance := func(v Value) (string, bool) {
			if lit, ok := v.Literal(); ok {
				return lit, lit == tag
			}
			if len(v.Parts) == 2 && v.Parts[0].Lit == tag+":" && v.Parts[1].Var != "" {
				return tag + ":{{" + v.Parts[1].Var + "}}", true
			}
			return "", false
		}
		removed := map[string]string{}
		for _, l := range tp.Lines {
			switch l.Verb {
			case "remove":
				for _, a := range l.Find {
					if a.Name == "comment" {
						if t, ok := instance(a.Value); ok {
							removed[l.Path()] = t
						}
					}
				}
				if _, ok := removed[l.Path()]; !ok {
					removed[l.Path()] = "(other)"
				}
			case "add":
				v, _ := l.Arg("comment")
				lit, tagged := instance(v)
				name, _ := l.Arg("name")
				nm, _ := name.Literal()
				tagged = tagged || strings.HasPrefix(nm, "mdcfg-") || l.Path() == "/system/logging"
				if strings.HasPrefix(lit, tag+":") && removed[l.Path()] != lit {
					t.Errorf("%s line %d: adds instance %s to %s without removing that instance first", c.ID, l.Num, lit, l.Path())
				}
				if !tagged {
					t.Errorf("%s line %d: an added row carries neither %q nor an mdcfg- name", c.ID, l.Num, tag)
				}
				if _, ok := removed[l.Path()]; !ok {
					t.Errorf("%s line %d: adds to %s without first removing its own rows there", c.ID, l.Num, l.Path())
				}
			}
		}
	}
}

// A lock-class template accepts MikroDash's own address before its first drop
// on the input chain, and never disables the service MikroDash uses.
func TestLockClassCannedTemplatesKeepMikroDashIn(t *testing.T) {
	unknown := LiveContext{FW: guard.FWContext{Resolved: false}, APIService: "api-ssl"}
	locking := 0
	for _, c := range CannedTemplates() {
		tp, _ := Parse(c.Body)
		lock := false
		for _, f := range AnalyzeLive(tp, unknown) {
			if f.Code == "lockout-unknown" || f.Code == "own-service-address" {
				lock = true
			}
			if f.Code == "own-service" {
				t.Errorf("%s: %s", c.ID, f.Message)
			}
		}
		if !lock {
			continue
		}
		locking++
		accepted, uses := false, map[string]bool{}
		for _, v := range tp.Vars() {
			uses[v] = true
		}
		for _, l := range tp.Lines {
			chain, _ := l.Arg("chain")
			ch, _ := chain.Literal()
			action, _ := l.Arg("action")
			act, _ := action.Literal()
			if ch == "input" && act == "accept" {
				if v, ok := l.Arg("src-address"); ok {
					if name, isVar := v.Var(); isVar && name == "mgmt_src" {
						accepted = true
					}
				}
			}
			// Dropping connection-state=invalid cannot cut a management
			// session, so it may come first, as it does in MikroTik's own.
			state, _ := l.Arg("connection-state")
			st, _ := state.Literal()
			if ch == "input" && act == "drop" && st != "invalid" && !accepted && l.Path() != "/ipv6/firewall/filter" {
				t.Errorf("%s line %d drops input before accepting {{mgmt_src}}", c.ID, l.Num)
			}
		}
		if l := tp.Lines; l[0].Path() == "/ip/service" && !uses["mgmt_src"] {
			t.Errorf("%s restricts the services without keeping {{mgmt_src}}", c.ID)
		}
	}
	if locking < 3 {
		t.Errorf("only %d canned templates are lock-class; the check is not reaching them", locking)
	}
}

// ── THE LEDGER ───────────────────────────────────────────────────────────────
//
// Each body's fingerprint, so a change to a shipped template is a change to
// this file and visible in review. BOTH DIRECTIONS: a body not listed fails,
// and a listed id with no body fails. A changed body needs its version bumped
// in the manifest too; the version is what a clone records as its baseline.
var cannedLedger = map[string]string{
	"anti-bufferbloat":        "dbbf23da08e5155f@v1",
	"bridge-vlan":             "6ec9b5eb7aa100f6@v1",
	"dns-and-time":            "4c410ebe6cd5cff4@v1",
	"fair-share":              "d03fb41c11775c09@v1",
	"family-safe-dns":         "b5aaaa7e59bb63c2@v1",
	"guest-network":           "72800c2714bd9c64@v1",
	"home-firewall":           "39803d7b9336e6e6@v1",
	"iot-isolation":           "96f4838dcb3a85b7@v1",
	"ipv6-firewall":           "806602513075a7f2@v1",
	"management-lockdown":     "2bb77a14fc3ccf96@v1",
	"office-firewall":         "c768340c1d8d1cf1@v1",
	"password-policy":         "65dc954d94d4bbf6@v1",
	"privacy-essentials":      "6a72ab5c3d53c1f6@v1",
	"remote-syslog":           "07563bc3de850f2e@v1",
	"site-ntp-server":         "5877cbc7c84b5a47@v1",
	"snmp-v3":                 "1c3faaf2504b3766@v1",
	"stack-hardening":         "6c0d2ab08b2fabb0@v1",
	"three-vlan-home":         "bef6caa309782f4d@v1",
	"vlan-network":            "c34518a65d146813@v1",
	"wireguard-remote-access": "75e47df1551205d1@v1",
}

func TestTheCannedLedger(t *testing.T) {
	got := map[string]string{}
	for _, c := range CannedTemplates() {
		h := sha256.Sum256([]byte(c.Body))
		got[c.ID] = hex.EncodeToString(h[:8]) + "@v" + strconv.Itoa(c.Version)
	}
	var lines []string
	for id, h := range got {
		if cannedLedger[id] != h {
			lines = append(lines, "\t\""+id+"\": \""+h+"\",")
		}
	}
	for id := range cannedLedger {
		if _, ok := got[id]; !ok {
			t.Errorf("the ledger lists %s, which is no longer shipped", id)
		}
	}
	if len(lines) > 0 {
		sort.Strings(lines)
		t.Errorf("canned bodies changed or are new; if deliberate, bump each one's version and record:\n%s",
			strings.Join(lines, "\n"))
	}
}
