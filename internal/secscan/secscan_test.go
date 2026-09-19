package secscan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/pages"
)

// in builds Inputs from menu rows; a nil slice is an absent menu.
func in(menus map[string][]Row) Inputs {
	out := Inputs{Rows: map[string][]Row{}, Absent: map[string]bool{},
		Now: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)}
	for m, rows := range menus {
		if rows == nil {
			out.Absent[m] = true
			continue
		}
		out.Rows[m] = rows
	}
	return out
}

func one(kv ...string) []Row {
	r := Row{}
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i]] = kv[i+1]
	}
	return []Row{r}
}

// The defconf's input and forward chains, which pass every firewall check.
var defconf = []Row{
	{"chain": "input", "action": "accept", "connection-state": "established,related,untracked"},
	{"chain": "input", "action": "drop", "connection-state": "invalid"},
	{"chain": "input", "action": "accept", "protocol": "icmp"},
	{"chain": "input", "action": "drop", "in-interface-list": "!LAN"},
	{"chain": "forward", "action": "fasttrack-connection", "connection-state": "established,related"},
	{"chain": "forward", "action": "accept", "connection-state": "established,related,untracked"},
	{"chain": "forward", "action": "drop", "connection-state": "invalid"},
	{"chain": "forward", "action": "drop", "connection-nat-state": "!dstnat", "in-interface-list": "WAN"},
}

var openInput = []Row{{"chain": "input", "action": "accept", "dst-port": "8728", "protocol": "tcp"}}

// cases holds, for every check, inputs it must fail and inputs it must pass.
// TestEveryCheckHasAFailingAndAPassingCase holds this table to the catalogue
// in both directions.
var cases = map[string][2]map[string][]Row{
	"mgmt.plaintext-services": {
		{"/ip/service": {{"name": "telnet", "port": "23", "disabled": "false"}}},
		{"/ip/service": {{"name": "telnet", "port": "23", "disabled": "true"}, {"name": "ssh", "disabled": "false"}}},
	},
	"mgmt.unrestricted-services": {
		{"/ip/service": {{"name": "ssh", "disabled": "false", "available-from": ""}}},
		{"/ip/service": {{"name": "ssh", "disabled": "false", "available-from": "10.0.0.0/8"},
			{"name": "dhcpclient", "dynamic": "true"}}},
	},
	"mgmt.ssl-no-certificate": {
		{"/ip/service": {{"name": "api-ssl", "disabled": "false", "certificate": "none"}}},
		// The connection rows RouterOS 7.24 adds (dynamic, no certificate) are
		// not the service: found live, where they failed this check.
		{"/ip/service": {{"name": "api-ssl", "disabled": "false", "certificate": "api"},
			{"name": "api-ssl", "disabled": "false", "dynamic": "true"}}},
	},
	"mgmt.mac-telnet": {
		{"/tool/mac-server": one("allowed-interface-list", "all")},
		{"/tool/mac-server": one("allowed-interface-list", "LAN")},
	},
	"mgmt.mac-winbox": {
		{"/tool/mac-server/mac-winbox": one("allowed-interface-list", "all")},
		{"/tool/mac-server/mac-winbox": one("allowed-interface-list", "none")},
	},
	"mgmt.mac-ping":  {{"/tool/mac-server/ping": one("enabled", "true")}, {"/tool/mac-server/ping": one("enabled", "false")}},
	"mgmt.discovery": {{"/ip/neighbor/discovery-settings": one("discover-interface-list", "static")}, {"/ip/neighbor/discovery-settings": one("discover-interface-list", "LAN")}},
	"mgmt.romon":     {{"/tool/romon": one("enabled", "true")}, {"/tool/romon": one("enabled", "false")}},
	"mgmt.bandwidth-server": {
		{"/tool/bandwidth-server": one("enabled", "true", "authenticate", "false")},
		{"/tool/bandwidth-server": one("enabled", "false", "authenticate", "false")},
	},
	"mgmt.ssh-crypto":     {{"/ip/ssh": one("strong-crypto", "false")}, {"/ip/ssh": one("strong-crypto", "true")}},
	"mgmt.ssh-forwarding": {{"/ip/ssh": one("forwarding-enabled", "both")}, {"/ip/ssh": one("forwarding-enabled", "no")}},

	"fw.input-default-drop": {{"/ip/firewall/filter": openInput}, {"/ip/firewall/filter": defconf}},
	"fw.input-established":  {{"/ip/firewall/filter": openInput}, {"/ip/firewall/filter": defconf}},
	"fw.input-invalid":      {{"/ip/firewall/filter": openInput}, {"/ip/firewall/filter": defconf}},
	"fw.forward-wan":        {{"/ip/firewall/filter": openInput}, {"/ip/firewall/filter": defconf}},
	"fw.forward-invalid":    {{"/ip/firewall/filter": openInput}, {"/ip/firewall/filter": defconf}},
	"fw.dns-open-resolver": {
		{"/ip/dns": one("allow-remote-requests", "true"), "/ip/firewall/filter": openInput},
		{"/ip/dns": one("allow-remote-requests", "true"), "/ip/firewall/filter": {
			{"chain": "input", "action": "drop", "protocol": "udp", "dst-port": "53", "in-interface": "ether1"}}},
	},
	"fw.wan-in-lan": {
		{"/interface/list/member": {{"interface": "ether1", "list": "WAN"}, {"interface": "ether1", "list": "LAN"}}},
		{"/interface/list/member": {{"interface": "ether1", "list": "WAN"}, {"interface": "bridge", "list": "LAN"}}},
	},
	"fw.ipv6-input": {
		{"/ipv6/settings": one("disable-ipv6", "false"), "/ipv6/firewall/filter": {}},
		{"/ipv6/settings": one("disable-ipv6", "false"), "/ipv6/firewall/filter": {
			{"chain": "input", "action": "reject", "reject-with": "icmp-no-route"}}},
	},
	"fw.rp-filter":  {{"/ip/settings": one("rp-filter", "no")}, {"/ip/settings": one("rp-filter", "loose")}},
	"fw.syncookies": {{"/ip/settings": one("tcp-syncookies", "false")}, {"/ip/settings": one("tcp-syncookies", "true")}},

	"svc.upnp":  {{"/ip/upnp": one("enabled", "true")}, {"/ip/upnp": one("enabled", "false")}},
	"svc.socks": {{"/ip/socks": one("enabled", "true")}, {"/ip/socks": one("enabled", "false")}},
	"svc.proxy": {{"/ip/proxy": one("enabled", "true")}, {"/ip/proxy": one("enabled", "false")}},
	"svc.smb": {
		{"/ip/smb": one("enabled", "auto", "status", "running")},
		{"/ip/smb": one("enabled", "auto", "status", "disabled")},
	},
	"svc.pptp": {{"/interface/pptp-server/server": one("enabled", "true")}, {"/interface/pptp-server/server": one("enabled", "false")}},
	"svc.l2tp-no-ipsec": {
		{"/interface/l2tp-server/server": one("enabled", "true", "use-ipsec", "no")},
		{"/interface/l2tp-server/server": one("enabled", "true", "use-ipsec", "required")},
	},
	"svc.snmp-public": {
		{"/snmp": one("enabled", "true"), "/snmp/community": {{"name": "public", "security": "none"}}},
		{"/snmp": one("enabled", "false"), "/snmp/community": {{"name": "public", "security": "none"}}},
	},
	"svc.snmp-write": {
		{"/snmp": one("enabled", "true"), "/snmp/community": {{"name": "ops", "write-access": "true"}}},
		{"/snmp": one("enabled", "true"), "/snmp/community": {{"name": "ops", "write-access": "false"}}},
	},
	"svc.snmp-anywhere": {
		{"/snmp": one("enabled", "true"), "/snmp/community": {{"name": "ops", "addresses": "::/0"}}},
		{"/snmp": one("enabled", "true"), "/snmp/community": {{"name": "ops", "addresses": "10.0.0.5/32"}}},
	},
	"svc.cloud-ddns": {{"/ip/cloud": one("ddns-enabled", "yes")}, {"/ip/cloud": one("ddns-enabled", "auto")}},

	"acct.admin-user": {
		{"/user": {{"name": "admin", "disabled": "false"}}},
		{"/user": {{"name": "admin", "disabled": "true"}, {"name": "ops", "disabled": "false"}}},
	},
	"acct.no-address": {
		{"/user": {{"name": "ops", "address": ""}}},
		{"/user": {{"name": "ops", "address": "10.0.0.0/8"}}},
	},
	"acct.password-policy": {{"/user/settings": one("minimum-password-length", "0")}, {"/user/settings": one("minimum-password-length", "12")}},
	"acct.full-users": {
		{"/user": {{"name": "a", "group": "full"}, {"name": "b", "group": "full"}, {"name": "c", "group": "full"}},
			"/user/group": {{"name": "full", "policy": "read,write,policy,test"}}},
		{"/user": {{"name": "a", "group": "full"}, {"name": "b", "group": "read"}, {"name": "c", "group": "read"}},
			"/user/group": {{"name": "full", "policy": "read,write,policy"}, {"name": "read", "policy": "read,!write,!policy"}}},
	},
	"acct.plaintext-sessions": {
		{"/user/active": {{"name": "ops", "via": "telnet"}}},
		{"/user/active": {{"name": "ops", "via": "winbox"}}},
	},

	"sys.update": {
		{"/system/package/update": one("installed-version", "7.24.3", "latest-version", "7.24.4")},
		{"/system/package/update": one("installed-version", "7.24.4", "latest-version", "7.24.4")},
	},
	"sys.firmware": {
		{"/system/routerboard": one("current-firmware", "7.23.3", "upgrade-firmware", "7.24.3")},
		{"/system/routerboard": one("current-firmware", "7.24.3", "upgrade-firmware", "7.24.3")},
	},
	"sys.ntp": {{"/system/ntp/client": one("enabled", "false")}, {"/system/ntp/client": one("enabled", "true")}},
	"sys.remote-log": {
		{"/system/logging": {{"action": "remote"}}, "/system/logging/action": {{"name": "remote", "target": "remote", "remote": "0.0.0.0"}}},
		{"/system/logging": {{"action": "syslog"}}, "/system/logging/action": {{"name": "syslog", "target": "remote", "remote": "10.0.0.9"}}},
	},
	"sys.script-permissions": {
		{"/system/script": {{"name": "backup", "dont-require-permissions": "true"}}},
		{"/system/script": {{"name": "backup", "dont-require-permissions": "false"}}},
	},
	"sys.device-mode": {{"/system/device-mode": one("mode", "advanced")}, {"/system/device-mode": one("mode", "home")}},

	"wifi.open": {
		{"/interface/wifi/security": {{"name": "guest", "authentication-types": ""}}, "/interface/wireless/security-profiles": nil},
		{"/interface/wifi/security": {{"name": "home", "authentication-types": "wpa2-psk,wpa3-psk"}}, "/interface/wireless/security-profiles": nil},
	},
	"wifi.weak": {
		{"/interface/wifi/security": nil, "/interface/wireless/security-profiles": {{"name": "old", "mode": "dynamic-keys",
			"authentication-types": "wpa2-psk", "unicast-ciphers": "tkip,aes-ccm"}}},
		{"/interface/wifi/security": nil, "/interface/wireless/security-profiles": {{"name": "ok", "mode": "dynamic-keys",
			"authentication-types": "wpa2-psk", "unicast-ciphers": "aes-ccm"}}},
	},
	"wifi.wps": {
		{"/interface/wifi/security": {{"name": "home", "wps": "push-button"}}},
		{"/interface/wifi/security": {{"name": "home", "wps": "disable"}}},
	},
	"cert.expiring": {
		{"/certificate": {{"name": "api", "invalid-after": "2026-10-01 00:00:00"}}},
		{"/certificate": {{"name": "api", "invalid-after": "2027-09-07 13:29:17"}}},
	},
}

// TestEveryCheckHasAFailingAndAPassingCase: each check fails its failing case
// and passes its passing one, so none can be a rule that always answers the
// same. The table fails in both directions: a check with no case, and a case
// naming no check.
func TestEveryCheckHasAFailingAndAPassingCase(t *testing.T) {
	ids := map[string]bool{}
	for _, c := range Checks {
		ids[c.ID] = true
		pair, ok := cases[c.ID]
		if !ok {
			t.Errorf("%s has no test case", c.ID)
			continue
		}
		if st, _ := c.Eval(in(pair[0])); st != Fail {
			t.Errorf("%s answered %s on its failing case", c.ID, st)
		}
		if st, detail := c.Eval(in(pair[1])); st != Pass {
			t.Errorf("%s answered %s %v on its passing case", c.ID, st, detail)
		}
	}
	for id := range cases {
		if !ids[id] {
			t.Errorf("the case table names %s, which is not a check", id)
		}
	}
	if len(Checks) < 40 {
		t.Fatalf("only %d checks — the catalogue shrank or the table broke", len(Checks))
	}
}

// TestAnAbsentMenuIsUnknown: a check whose menu the router does not have
// answers unknown, never pass or fail.
func TestAnAbsentMenuIsUnknown(t *testing.T) {
	absent := map[string][]Row{}
	for _, m := range Menus() {
		absent[m.Path] = nil
	}
	for _, c := range Checks {
		if st, _ := c.Eval(in(absent)); st != Unknown {
			t.Errorf("%s answered %s with every menu absent, want unknown", c.ID, st)
		}
	}
	rep := Run(in(absent))
	if rep.Unknown != len(Checks) || rep.Score != 100 {
		t.Errorf("an unanswerable scan scored %d with %d unknown, want 100 and %d", rep.Score, rep.Unknown, len(Checks))
	}
}

// TestTheScore: the share of the answered weight that passed, with a failed
// critical check capping it at CriticalCap.
func TestTheScore(t *testing.T) {
	tot := func(sev ...Severity) *catTotals {
		c := &catTotals{}
		for i, s := range sev {
			st := Pass
			if i == 0 {
				st = Fail
			}
			c.add(s, st)
		}
		return c
	}
	// One failed medium (6) out of medium + high (18): 67.
	if s := tot(Medium, High).score(); s != 67 {
		t.Errorf("score %d, want 67", s)
	}
	// A failed critical (25) among four passed highs (73 answered): 66, capped.
	if s := tot(Critical, High, High, High, High).score(); s != CriticalCap {
		t.Errorf("a failed critical scored %d, want the cap %d", s, CriticalCap)
	}
	// A failed info weighs nothing.
	if s := tot(Info, Low).score(); s != 100 {
		t.Errorf("a failed info check cost %d points", 100-s)
	}
	if s := (&catTotals{}).score(); s != 100 {
		t.Errorf("nothing answered scored %d, want 100", s)
	}
}

// TestMenusIsTheCataloguesUnion: every menu a check names is read, with a
// proplist declared in menuSpecs; every menuSpecs entry is read by something.
// A ledger in both directions.
func TestMenusIsTheCataloguesUnion(t *testing.T) {
	read := map[string]bool{}
	for _, m := range Menus() {
		if read[m.Path] {
			t.Errorf("%s is read twice", m.Path)
		}
		read[m.Path] = true
	}
	for _, c := range Checks {
		for _, m := range c.Menus {
			if _, ok := menuSpecs[m]; !ok {
				t.Errorf("%s reads %s, which has no entry in menuSpecs", c.ID, m)
			}
			if !read[m] {
				t.Errorf("%s reads %s, which Menus() does not", c.ID, m)
			}
		}
	}
	for m := range menuSpecs {
		if !read[m] {
			t.Errorf("menuSpecs declares %s, which nothing reads", m)
		}
	}
}

// credentialShapedButNot are proplist names the credential rule matches that
// hold no secret. Each needs its reason, and the list fails if one goes unused.
var credentialShapedButNot = map[string]string{
	"minimum-password-length": "the password policy's length, a number",
}

// TestNoProplistNamesACredential: the scan reads no secret. The server's
// payload reaches the browser, so a password in a proplist is a password on
// the page. And only the firewall tables are read whole: the other menus with
// secrets in them (/user, /snmp/community, wifi security, the L2TP server,
// RoMON) return them in a bare print.
func TestNoProplistNamesACredential(t *testing.T) {
	used := map[string]bool{}
	for m, props := range menuSpecs {
		if len(props) == 0 && m != "/ip/firewall/filter" && m != "/ipv6/firewall/filter" {
			t.Errorf("%s is read without a proplist", m)
		}
		for _, p := range props {
			if !audit.IsCredentialField(p) {
				continue
			}
			if _, ok := credentialShapedButNot[p]; ok {
				used[p] = true
				continue
			}
			t.Errorf("%s reads %q, a credential", m, p)
		}
	}
	for p := range credentialShapedButNot {
		if !used[p] {
			t.Errorf("credentialShapedButNot excuses %q, which no proplist names any more", p)
		}
	}
}

// TestTheCatalogueIsWellFormed: unique IDs, a known severity and category, a
// title, why and fix for each, and every link a real page key.
func TestTheCatalogueIsWellFormed(t *testing.T) {
	keys := map[string]bool{}
	for _, k := range pages.Keys() {
		keys[k] = true
	}
	seen := map[string]bool{}
	sev := map[Severity]bool{}
	for _, s := range Severities {
		sev[s] = true
	}
	for _, c := range Checks {
		if seen[c.ID] {
			t.Errorf("check id %s is used twice", c.ID)
		}
		seen[c.ID] = true
		if !sev[c.Severity] || c.Category == "" || c.Title == "" || c.Why == "" || c.Fix == "" || len(c.Menus) == 0 {
			t.Errorf("%s is missing a severity, category, title, why, fix or menu", c.ID)
		}
		if c.Link != "" && !keys[c.Link] {
			t.Errorf("%s links to %q, which is not a page", c.ID, c.Link)
		}
	}
}

// ── REPLAYS OF THE TWO TEST ROUTERS ─────────────────────────────────────────

type fixture struct {
	Exchanges []struct {
		Cmd  string              `json:"cmd"`
		Rows []map[string]string `json:"rows"`
		Trap string              `json:"trap"`
	} `json:"exchanges"`
}

func replay(t *testing.T, router string) Report {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", router, "securityScan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	menus := map[string][]Row{}
	for _, e := range f.Exchanges {
		path := strings.TrimSuffix(e.Cmd, "/print")
		if e.Trap != "" {
			menus[path] = nil
			continue
		}
		rows := make([]Row, 0, len(e.Rows))
		for _, r := range e.Rows {
			rows = append(rows, Row(r))
		}
		menus[path] = rows
	}
	for _, m := range Menus() {
		if _, ok := menus[m.Path]; !ok {
			t.Fatalf("the %s fixture does not hold %s: recapture it", router, m.Path)
		}
	}
	return Run(in(menus))
}

func statuses(rep Report) map[string]Status {
	out := map[string]Status{}
	for _, f := range rep.Findings {
		out[f.ID] = f.Status
	}
	return out
}

func expect(t *testing.T, router string, got map[string]Status, want map[string]Status) {
	t.Helper()
	var ids []string
	for id := range want {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if got[id] != want[id] {
			t.Errorf("%s: %s is %s, want %s", router, id, got[id], want[id])
		}
	}
}

// TestTheCHRReplay: the CHR test router as it stands, with no firewall to speak
// of. Its open input chain is the critical finding, and caps the score.
func TestTheCHRReplay(t *testing.T) {
	rep := replay(t, "CHR Test")
	expect(t, "CHR Test", statuses(rep), map[string]Status{
		"fw.input-default-drop":   Fail,
		"fw.forward-wan":          Fail,
		"mgmt.plaintext-services": Fail,
		"mgmt.mac-winbox":         Fail,
		"mgmt.bandwidth-server":   Fail,
		"mgmt.ssl-no-certificate": Pass, // api-ssl has chr-api; its connection rows have none
		"acct.admin-user":         Fail,
		"sys.update":              Fail,
		"fw.ipv6-input":           Pass, // its IPv6 input ends in a reject
		"svc.snmp-public":         Pass, // SNMP is off
		"fw.dns-open-resolver":    Pass, // allow-remote-requests is off
		"sys.firmware":            Unknown,
		"wifi.open":               Pass, // the wifi package is there, with no profiles
		"cert.expiring":           Pass,
	})
	if rep.Score > CriticalCap {
		t.Errorf("CHR Test scored %d with an open input chain, want at most %d", rep.Score, CriticalCap)
	}
	t.Logf("CHR Test: score %d, failed %v, passed %d, unknown %d", rep.Score, rep.Failed, rep.Passed, rep.Unknown)
}

// TestTheHAPAC2Replay: the hAP AC2, with the defconf firewall and one real
// misconfiguration: ether1 is in the LAN list as well as the WAN list.
func TestTheHAPAC2Replay(t *testing.T) {
	rep := replay(t, "hAP AC2")
	expect(t, "hAP AC2", statuses(rep), map[string]Status{
		"fw.input-default-drop": Pass,
		"fw.input-established":  Pass,
		"fw.forward-wan":        Pass,
		"fw.dns-open-resolver":  Pass, // allow-remote is on, and port 53 is dropped on ether1
		"fw.wan-in-lan":         Fail,
		"sys.firmware":          Fail,
		"wifi.open":             Pass,
		"wifi.wps":              Pass,
	})
	for _, f := range rep.Findings {
		if f.ID == "fw.wan-in-lan" && (len(f.Detail) != 1 || f.Detail[0] != "ether1") {
			t.Errorf("fw.wan-in-lan names %v, want [ether1]", f.Detail)
		}
	}
	t.Logf("hAP AC2: score %d, failed %v, passed %d, unknown %d", rep.Score, rep.Failed, rep.Passed, rep.Unknown)
}
