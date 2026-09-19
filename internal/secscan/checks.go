package secscan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/collect"
)

// menuSpecs is each menu's proplist: exactly the properties the checks read,
// and NEVER a credential (the user's password, SNMP auth and encryption
// passwords, the wifi passphrase, the L2TP IPsec secret, RoMON's secrets are
// all left out). An empty list reads the whole row: only the firewall tables,
// where a rule's matchers are the question and any property can be one.
var menuSpecs = map[string][]string{
	"/ip/service":                           {"name", "port", "disabled", "dynamic", "available-from", "address", "certificate"},
	"/tool/mac-server":                      {"allowed-interface-list"},
	"/tool/mac-server/mac-winbox":           {"allowed-interface-list"},
	"/tool/mac-server/ping":                 {"enabled"},
	"/ip/neighbor/discovery-settings":       {"discover-interface-list"},
	"/tool/romon":                           {"enabled"},
	"/tool/bandwidth-server":                {"enabled", "authenticate"},
	"/ip/ssh":                               {"strong-crypto", "forwarding-enabled"},
	"/ip/firewall/filter":                   nil,
	"/ipv6/firewall/filter":                 nil,
	"/ipv6/settings":                        {"disable-ipv6"},
	"/ip/settings":                          {"rp-filter", "tcp-syncookies"},
	"/ip/dns":                               {"allow-remote-requests"},
	"/interface/list/member":                {"interface", "list", "disabled"},
	"/ip/upnp":                              {"enabled"},
	"/ip/socks":                             {"enabled", "auth-method"},
	"/ip/proxy":                             {"enabled"},
	"/ip/smb":                               {"enabled", "status"},
	"/interface/pptp-server/server":         {"enabled"},
	"/interface/l2tp-server/server":         {"enabled", "use-ipsec"},
	"/snmp":                                 {"enabled"},
	"/snmp/community":                       {"name", "addresses", "security", "write-access", "disabled"},
	"/ip/cloud":                             {"ddns-enabled"},
	"/user":                                 {"name", "group", "address", "disabled"},
	"/user/group":                           {"name", "policy"},
	"/user/settings":                        {"minimum-password-length"},
	"/user/active":                          {"name", "via"},
	"/system/package/update":                {"installed-version", "latest-version", "status"},
	"/system/routerboard":                   {"routerboard", "current-firmware", "upgrade-firmware"},
	"/system/ntp/client":                    {"enabled"},
	"/system/logging":                       {"action", "disabled"},
	"/system/logging/action":                {"name", "target", "remote"},
	"/system/script":                        {"name", "dont-require-permissions"},
	"/system/device-mode":                   {"mode"},
	"/interface/wifi/security":              {"name", "authentication-types", "encryption", "wps"},
	"/interface/wireless/security-profiles": {"name", "mode", "authentication-types", "unicast-ciphers"},
	"/certificate":                          {"name", "invalid-after"},
}

// factMenus are read for the overview's facts beyond what the checks read.
// Empty today: every fact comes from a menu a check already reads.
var factMenus []string

// ── Small readers ───────────────────────────────────────────────────────────

func yes(v string) bool { return v == "true" || v == "yes" }

func enabledRow(r Row) bool { return !yes(r["disabled"]) }

func verdict(bad []string) (Status, []string) {
	if len(bad) > 0 {
		return Fail, bad
	}
	return Pass, nil
}

// ── Firewall predicates ─────────────────────────────────────────────────────

// fwNotMatchers are the properties of a filter row that do not narrow what it
// matches: its identity, what it does, its counters and its flags. Any OTHER
// property with a value is a matcher, so a rule is only read as "everything"
// when nothing unrecognised narrows it: an unknown property makes a rule
// narrower, never wider, which errs towards reporting a gap.
var fwNotMatchers = map[string]bool{
	".id": true, "chain": true, "action": true, "disabled": true, "dynamic": true, "invalid": true,
	"bytes": true, "packets": true, "comment": true, "log": true, "log-prefix": true,
	"reject-with": true, "jump-target": true,
}

// catchAll is a rule that matches all traffic, or all traffic NOT from a set of
// interfaces: the defconf's `drop in-interface-list=!LAN` is the second kind.
func catchAll(r Row) bool {
	for k, v := range r {
		if fwNotMatchers[k] || v == "" {
			continue
		}
		if (k == "in-interface" || k == "in-interface-list") && strings.HasPrefix(v, "!") {
			continue
		}
		return false
	}
	return true
}

func dropping(r Row) bool { return r["action"] == "drop" || r["action"] == "reject" }

// endsInDrop: the chain has an enabled drop or reject that catches the rest.
// Order is not modelled: a catch-all drop anywhere counts, as the defconf's
// last rule does. A catch-all ACCEPT above it would defeat it, and is not
// looked for; the page says what this check reads.
func endsInDrop(rows []Row, chain string) bool {
	for _, r := range rows {
		if r["chain"] == chain && enabledRow(r) && dropping(r) && catchAll(r) {
			return true
		}
	}
	return false
}

func hasState(r Row, state string) bool {
	for _, s := range strings.Split(r["connection-state"], ",") {
		if s == state {
			return true
		}
	}
	return false
}

func acceptsEstablished(rows []Row, chain string) bool {
	for _, r := range rows {
		if r["chain"] == chain && enabledRow(r) && hasState(r, "established") &&
			(r["action"] == "accept" || r["action"] == "fasttrack-connection") {
			return true
		}
	}
	return false
}

func dropsInvalid(rows []Row, chain string) bool {
	for _, r := range rows {
		if r["chain"] == chain && enabledRow(r) && dropping(r) && hasState(r, "invalid") {
			return true
		}
	}
	return false
}

// portIn reports whether a dst-port value ("53", "53,853", "1-1024") covers p.
func portIn(spec string, p int) bool {
	for _, part := range strings.Split(spec, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(strings.TrimSpace(lo))
		if err != nil {
			continue
		}
		b := a
		if isRange {
			if b, err = strconv.Atoi(strings.TrimSpace(hi)); err != nil {
				continue
			}
		}
		if p >= a && p <= b {
			return true
		}
	}
	return false
}

// ── Accounts ────────────────────────────────────────────────────────────────

// fullGroups are the user groups granting both `write` and `policy`: a member
// can change anything, including who else can.
func fullGroups(groups []Row) map[string]bool {
	out := map[string]bool{}
	for _, g := range groups {
		has := map[string]bool{}
		for _, p := range strings.Split(g["policy"], ",") {
			has[p] = true
		}
		if has["write"] && has["policy"] {
			out[g["name"]] = true
		}
	}
	return out
}

// ── The catalogue ───────────────────────────────────────────────────────────

var plaintextServices = map[string]bool{"telnet": true, "ftp": true, "www": true, "api": true}

// Checks is the catalogue, in the order the page lists a category's checks.
var Checks = []Check{
	// ── Management access ──────────────────────────────────────────────────
	{
		ID: "mgmt.plaintext-services", Category: "Management access", Severity: High,
		Title: "Plaintext management services are enabled",
		Why:   "Telnet, FTP, HTTP and the plain API send logins and everything after them unencrypted.",
		Fix:   "Disable telnet, ftp, www and api; manage with SSH, WinBox, www-ssl and api-ssl.",
		Link:  "ip-services", Menus: []string{"/ip/service"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/ip/service")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				if !yes(r["dynamic"]) && enabledRow(r) && plaintextServices[r["name"]] {
					bad = append(bad, r["name"]+" (port "+r["port"]+")")
				}
			}
			return verdict(bad)
		},
	},
	{
		ID: "mgmt.unrestricted-services", Category: "Management access", Severity: Medium,
		Title: "Management services accept connections from any address",
		Why:   "A service with no allowed addresses answers the whole internet if the firewall ever lets it through.",
		Fix:   "Set Available From on every enabled service to the management networks.",
		Link:  "ip-services", Menus: []string{"/ip/service"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/ip/service")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				if !yes(r["dynamic"]) && enabledRow(r) && !restricted(r) {
					bad = append(bad, r["name"])
				}
			}
			return verdict(bad)
		},
	},
	{
		ID: "mgmt.ssl-no-certificate", Category: "Management access", Severity: Medium,
		Title: "An SSL service is enabled without a certificate",
		Why:   "www-ssl and api-ssl with no certificate do not work, and invite switching back to the plaintext service.",
		Fix:   "Assign a certificate to www-ssl and api-ssl, or disable them.",
		Link:  "ip-services", Menus: []string{"/ip/service"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/ip/service")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				n := r["name"]
				if (n == "www-ssl" || n == "api-ssl") && enabledRow(r) && (r["certificate"] == "" || r["certificate"] == "none") {
					bad = append(bad, n)
				}
			}
			return verdict(bad)
		},
	},
	macCheck("mgmt.mac-telnet", "MAC Telnet is allowed on every interface", "/tool/mac-server",
		"MAC Telnet gives a login prompt to anything on the same layer-2 segment, bypassing the IP firewall."),
	macCheck("mgmt.mac-winbox", "MAC WinBox is allowed on every interface", "/tool/mac-server/mac-winbox",
		"MAC WinBox gives full management to anything on the same layer-2 segment, bypassing the IP firewall."),
	onCheck("mgmt.mac-ping", "Management access", "MAC Ping is enabled", Low, "/tool/mac-server/ping",
		"It lets layer-2 neighbours discover the router.",
		"/tool mac-server ping set enabled=no", ""),
	{
		ID: "mgmt.discovery", Category: "Management access", Severity: Low,
		Title: "Neighbour discovery runs on a list that includes the WAN",
		Why:   "MNDP, CDP and LLDP announce the model, version and identity to whoever is connected.",
		Fix:   "Set IP › Neighbors › Discovery Settings to the LAN interface list.",
		Menus: []string{"/ip/neighbor/discovery-settings"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/ip/neighbor/discovery-settings")
			if !ok {
				return Unknown, nil
			}
			switch l := r["discover-interface-list"]; l {
			case "all", "static", "dynamic":
				return Fail, []string{"discover-interface-list=" + l}
			}
			return Pass, nil
		},
	},
	onCheck("mgmt.romon", "Management access", "RoMON is enabled", Medium, "/tool/romon",
		"RoMON lets any RoMON-capable neighbour manage this router over layer 2, outside the IP firewall.",
		"/tool romon set enabled=no, unless it is part of how the network is managed.", ""),
	{
		ID: "mgmt.bandwidth-server", Category: "Management access", Severity: Medium,
		Title: "The bandwidth-test server is enabled",
		Why:   "Anyone who reaches it can saturate the router's links and CPU; without authentication, with no login at all.",
		Fix:   "/tool bandwidth-server set enabled=no when no test is being run.",
		Menus: []string{"/tool/bandwidth-server"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/tool/bandwidth-server")
			if !ok {
				return Unknown, nil
			}
			if !yes(r["enabled"]) {
				return Pass, nil
			}
			if !yes(r["authenticate"]) {
				return Fail, []string{"enabled, without authentication"}
			}
			return Fail, []string{"enabled"}
		},
	},
	{
		ID: "mgmt.ssh-crypto", Category: "Management access", Severity: Low,
		Title: "SSH allows weak cryptography",
		Why:   "Without strong-crypto, SSH still offers SHA-1 and older ciphers.",
		Fix:   "/ip ssh set strong-crypto=yes",
		Menus: []string{"/ip/ssh"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/ip/ssh")
			if !ok {
				return Unknown, nil
			}
			if !yes(r["strong-crypto"]) {
				return Fail, []string{"strong-crypto=" + r["strong-crypto"]}
			}
			return Pass, nil
		},
	},
	{
		ID: "mgmt.ssh-forwarding", Category: "Management access", Severity: Low,
		Title: "SSH port forwarding is allowed",
		Why:   "A logged-in user can tunnel through the router to anything it can reach.",
		Fix:   "/ip ssh set forwarding-enabled=no",
		Menus: []string{"/ip/ssh"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/ip/ssh")
			if !ok {
				return Unknown, nil
			}
			if f := r["forwarding-enabled"]; f != "" && f != "no" {
				return Fail, []string{"forwarding-enabled=" + f}
			}
			return Pass, nil
		},
	},

	// ── Firewall ───────────────────────────────────────────────────────────
	{
		ID: "fw.input-default-drop", Category: "Firewall", Severity: Critical,
		Title: "The input chain does not drop what it has not accepted",
		Why:   "With no final drop, every service on the router is reachable from every interface, the internet included.",
		Fix:   "End the input chain with a drop for everything not from the LAN (the defconf's drop in-interface-list=!LAN).",
		Link:  "firewall", Menus: []string{"/ip/firewall/filter"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/ip/firewall/filter")
			if !ok {
				return Unknown, nil
			}
			if endsInDrop(rows, "input") {
				return Pass, nil
			}
			return Fail, []string{"no drop or reject that catches the rest of the input chain"}
		},
	},
	{
		ID: "fw.input-established", Category: "Firewall", Severity: Medium,
		Title: "The input chain does not accept established connections",
		Why:   "Without it, replies to the router's own traffic are judged rule by rule, and a final drop breaks them.",
		Fix:   "Put accept connection-state=established,related,untracked at the top of the input chain.",
		Link:  "firewall", Menus: []string{"/ip/firewall/filter"},
		Eval: fwHas("input", acceptsEstablished, "no accept for established,related"),
	},
	{
		ID: "fw.input-invalid", Category: "Firewall", Severity: Low,
		Title: "The input chain does not drop invalid packets",
		Why:   "Invalid packets are malformed or out of state, and are a common probe.",
		Fix:   "Add drop connection-state=invalid near the top of the input chain.",
		Link:  "firewall", Menus: []string{"/ip/firewall/filter"},
		Eval: fwHas("input", dropsInvalid, "no drop for connection-state=invalid"),
	},
	{
		ID: "fw.forward-wan", Category: "Firewall", Severity: High,
		Title: "Forwarded traffic from the WAN is not dropped",
		Why:   "Without it, anything on the internet that can route to the LAN reaches it, not only port-forwarded services.",
		Fix:   "Add to the forward chain: drop connection-nat-state=!dstnat in-interface-list=WAN (the defconf rule).",
		Link:  "firewall", Menus: []string{"/ip/firewall/filter"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/ip/firewall/filter")
			if !ok {
				return Unknown, nil
			}
			if endsInDrop(rows, "forward") {
				return Pass, nil
			}
			for _, r := range rows {
				if r["chain"] == "forward" && enabledRow(r) && dropping(r) && r["connection-nat-state"] == "!dstnat" &&
					(positive(r["in-interface-list"]) || positive(r["in-interface"])) {
					return Pass, nil
				}
			}
			return Fail, []string{"no drop for new connections arriving from the WAN"}
		},
	},
	{
		ID: "fw.forward-invalid", Category: "Firewall", Severity: Low,
		Title: "The forward chain does not drop invalid packets",
		Why:   "Invalid packets are malformed or out of state, and the LAN should not see them.",
		Fix:   "Add drop connection-state=invalid to the forward chain.",
		Link:  "firewall", Menus: []string{"/ip/firewall/filter"},
		Eval: fwHas("forward", dropsInvalid, "no drop for connection-state=invalid"),
	},
	{
		ID: "fw.dns-open-resolver", Category: "Firewall", Severity: High,
		Title: "The DNS server answers remote requests with nothing blocking the WAN",
		Why:   "An open resolver is used to amplify denial-of-service attacks against others, from your address.",
		Fix:   "Drop input to port 53 from the WAN, or end the input chain with a drop; or turn off Allow Remote Requests.",
		Link:  "dns", Menus: []string{"/ip/dns", "/ip/firewall/filter"},
		Eval: func(in Inputs) (Status, []string) {
			dns, ok := in.one("/ip/dns")
			if !ok {
				return Unknown, nil
			}
			if !yes(dns["allow-remote-requests"]) {
				return Pass, nil
			}
			rows, ok := in.get("/ip/firewall/filter")
			if !ok {
				return Unknown, nil
			}
			if endsInDrop(rows, "input") {
				return Pass, nil
			}
			for _, r := range rows {
				if r["chain"] == "input" && enabledRow(r) && dropping(r) && portIn(r["dst-port"], 53) {
					return Pass, nil
				}
			}
			return Fail, []string{"allow-remote-requests=yes, and no input rule drops port 53"}
		},
	},
	{
		ID: "fw.wan-in-lan", Category: "Firewall", Severity: High,
		Title: "An interface is in both the WAN and LAN lists",
		Why:   "Rules written as \"not LAN\" or \"from WAN\" then disagree about it, and the defconf's input drop never applies to it.",
		Fix:   "Remove the WAN interface from the LAN interface list.",
		Link:  "interface-lists", Menus: []string{"/interface/list/member"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/interface/list/member")
			if !ok {
				return Unknown, nil
			}
			wan, lan := map[string]bool{}, map[string]bool{}
			for _, r := range rows {
				if !enabledRow(r) {
					continue
				}
				switch strings.ToUpper(r["list"]) {
				case "WAN":
					wan[r["interface"]] = true
				case "LAN":
					lan[r["interface"]] = true
				}
			}
			var bad []string
			for i := range wan {
				if lan[i] {
					bad = append(bad, i)
				}
			}
			sort.Strings(bad)
			return verdict(bad)
		},
	},
	{
		ID: "fw.ipv6-input", Category: "Firewall", Severity: High,
		Title: "IPv6 is enabled and its input chain does not drop the rest",
		Why:   "IPv6 addresses are globally routable: an open IPv6 input chain exposes the router however tight the IPv4 one is.",
		Fix:   "End the IPv6 input chain with a drop for everything not from the LAN, or disable IPv6.",
		Link:  "firewall", Menus: []string{"/ipv6/settings", "/ipv6/firewall/filter"},
		Eval: func(in Inputs) (Status, []string) {
			s, ok := in.one("/ipv6/settings")
			if !ok {
				return Unknown, nil
			}
			if yes(s["disable-ipv6"]) {
				return Pass, nil
			}
			rows, ok := in.get("/ipv6/firewall/filter")
			if !ok {
				return Unknown, nil
			}
			if endsInDrop(rows, "input") {
				return Pass, nil
			}
			return Fail, []string{"no drop or reject that catches the rest of the IPv6 input chain"}
		},
	},
	ipSetting("fw.rp-filter", "Reverse-path filtering is off", "rp-filter", "no",
		"Without it the router accepts packets with spoofed source addresses on the wrong interface.",
		"/ip settings set rp-filter=loose (or strict on a single-homed router)."),
	ipSetting("fw.syncookies", "TCP SYN cookies are off", "tcp-syncookies", "false",
		"SYN cookies keep the router answering during a SYN flood.",
		"/ip settings set tcp-syncookies=yes"),

	// ── Exposed services ───────────────────────────────────────────────────
	onCheck("svc.upnp", "Exposed services", "UPnP is enabled", High, "/ip/upnp",
		"UPnP lets any device on the LAN open ports on the router to the internet, malware included.",
		"/ip upnp set enabled=no", ""),
	onCheck("svc.socks", "Exposed services", "The SOCKS proxy is enabled", High, "/ip/socks",
		"An open SOCKS proxy relays anyone's traffic through your address.",
		"/ip socks set enabled=no", ""),
	onCheck("svc.proxy", "Exposed services", "The web proxy is enabled", Medium, "/ip/proxy",
		"A web proxy reachable from outside becomes an open relay for anyone's traffic.",
		"/ip proxy set enabled=no, or restrict it with access rules.", ""),
	{
		ID: "svc.smb", Category: "Exposed services", Severity: Medium,
		Title: "The SMB file server is running",
		Why:   "SMB has a long history of remote vulnerabilities and should not run on a router.",
		Fix:   "/ip smb set enabled=no",
		Menus: []string{"/ip/smb"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/ip/smb")
			if !ok {
				return Unknown, nil
			}
			// enabled is yes, no or auto; status says whether auto started it.
			if r["enabled"] == "yes" || (r["status"] != "" && r["status"] != "disabled") {
				return Fail, []string{"enabled=" + r["enabled"] + ", status=" + r["status"]}
			}
			return Pass, nil
		},
	},
	onCheck("svc.pptp", "Exposed services", "A PPTP server is enabled", High, "/interface/pptp-server/server",
		"PPTP's MS-CHAPv2 authentication is broken: captured logins are cracked offline.",
		"Disable the PPTP server and move clients to WireGuard, IKEv2 or L2TP/IPsec.", "vpn"),
	{
		ID: "svc.l2tp-no-ipsec", Category: "Exposed services", Severity: Medium,
		Title: "The L2TP server runs without IPsec",
		Why:   "L2TP alone does not encrypt: tunnel traffic and logins cross the network in the clear.",
		Fix:   "/interface l2tp-server server set use-ipsec=required",
		Link:  "vpn", Menus: []string{"/interface/l2tp-server/server"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/interface/l2tp-server/server")
			if !ok {
				return Unknown, nil
			}
			if yes(r["enabled"]) && (r["use-ipsec"] == "no" || r["use-ipsec"] == "") {
				return Fail, []string{"use-ipsec=" + r["use-ipsec"]}
			}
			return Pass, nil
		},
	},
	snmpCheck("svc.snmp-public", "SNMP answers the default community or no security", Medium,
		"The default community \"public\", or security=none, lets anyone who can reach SNMP read the router's configuration.",
		"Rename or disable the public community, and set security to authorized or private.",
		func(r Row) bool { return r["name"] == "public" || r["security"] == "none" }),
	snmpCheck("svc.snmp-write", "An SNMP community has write access", High,
		"SNMP write access can change the router's configuration.",
		"Turn off write-access on every community.",
		func(r Row) bool { return yes(r["write-access"]) }),
	snmpCheck("svc.snmp-anywhere", "An SNMP community is open to any address", Medium,
		"A community with addresses 0.0.0.0/0 or ::/0 answers the whole internet if the firewall lets SNMP through.",
		"Restrict each community's addresses to the monitoring hosts.",
		func(r Row) bool {
			a := r["addresses"]
			return a == "" || strings.Contains(a, "0.0.0.0/0") || strings.Contains(a, "::/0")
		}),
	{
		ID: "svc.cloud-ddns", Category: "Exposed services", Severity: Info,
		Title: "IP Cloud DDNS publishes the router's address",
		Why:   "The router's public address is published under a predictable MikroTik hostname.",
		Fix:   "/ip cloud set ddns-enabled=no if nothing relies on it.",
		Menus: []string{"/ip/cloud"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/ip/cloud")
			if !ok {
				return Unknown, nil
			}
			if yes(r["ddns-enabled"]) {
				return Fail, []string{"ddns-enabled=yes"}
			}
			return Pass, nil
		},
	},

	// ── Accounts ───────────────────────────────────────────────────────────
	{
		ID: "acct.admin-user", Category: "Accounts", Severity: Medium,
		Title: "The default admin account is enabled",
		Why:   "Every attack on a MikroTik router tries \"admin\" first.",
		Fix:   "Create a personal full account, then disable or remove admin.",
		Link:  "users", Menus: []string{"/user"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/user")
			if !ok {
				return Unknown, nil
			}
			for _, r := range rows {
				if r["name"] == "admin" && enabledRow(r) {
					return Fail, []string{"admin"}
				}
			}
			return Pass, nil
		},
	},
	{
		ID: "acct.no-address", Category: "Accounts", Severity: Low,
		Title: "Accounts can log in from any address",
		Why:   "A stolen password works from anywhere; an allowed-address list limits where it works.",
		Fix:   "Set Allowed Address on each account to the networks it is used from.",
		Link:  "users", Menus: []string{"/user"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/user")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				if enabledRow(r) && strings.Trim(r["address"], ", ") == "" {
					bad = append(bad, r["name"])
				}
			}
			return verdict(bad)
		},
	},
	{
		ID: "acct.password-policy", Category: "Accounts", Severity: Low,
		Title: "The minimum password length is under 8",
		Why:   "Without a policy RouterOS accepts any password, a one-letter one included.",
		Fix:   "/user settings set minimum-password-length=12 minimum-categories=2",
		Link:  "users", Menus: []string{"/user/settings"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/user/settings")
			if !ok {
				return Unknown, nil
			}
			n, err := strconv.Atoi(r["minimum-password-length"])
			if err != nil {
				return Unknown, nil
			}
			if n < 8 {
				return Fail, []string{"minimum-password-length=" + strconv.Itoa(n)}
			}
			return Pass, nil
		},
	},
	{
		ID: "acct.full-users", Category: "Accounts", Severity: Info,
		Title: "More than two accounts have full rights",
		Why:   "Every account that can change policy is one more password that can hand the router over.",
		Fix:   "Give day-to-day accounts a group without the policy right.",
		Link:  "users", Menus: []string{"/user", "/user/group"},
		Eval: func(in Inputs) (Status, []string) {
			users, ok := in.get("/user")
			groups, ok2 := in.get("/user/group")
			if !ok || !ok2 {
				return Unknown, nil
			}
			full := fullGroups(groups)
			var who []string
			for _, u := range users {
				if enabledRow(u) && full[u["group"]] {
					who = append(who, u["name"])
				}
			}
			if len(who) > 2 {
				return Fail, who
			}
			return Pass, nil
		},
	},
	{
		ID: "acct.plaintext-sessions", Category: "Accounts", Severity: Medium,
		Title: "Someone is logged in over a plaintext protocol",
		Why:   "A telnet or FTP session's password crossed the network readable.",
		Fix:   "Change that account's password, and disable telnet and ftp.",
		Link:  "users", Menus: []string{"/user/active"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/user/active")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				if r["via"] == "telnet" || r["via"] == "ftp" {
					bad = append(bad, r["name"]+" via "+r["via"])
				}
			}
			return verdict(bad)
		},
	},

	// ── System ─────────────────────────────────────────────────────────────
	{
		ID: "sys.update", Category: "System", Severity: Medium,
		Title: "A newer RouterOS is available",
		Why:   "RouterOS releases carry security fixes, some for remotely exploitable bugs.",
		Fix:   "Install the update from the Packages page.",
		Link:  "packages", Menus: []string{"/system/package/update"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/system/package/update")
			if !ok || (r["latest-version"] == "" && r["status"] == "") {
				return Unknown, nil
			}
			if collect.UpdateVerdict(r["latest-version"], r["status"], r["installed-version"]) {
				return Fail, []string{r["installed-version"] + " installed, " + r["latest-version"] + " available"}
			}
			return Pass, nil
		},
	},
	{
		ID: "sys.firmware", Category: "System", Severity: Low,
		Title: "The RouterBOARD firmware is behind RouterOS",
		Why:   "The boot firmware is updated separately, and carries its own fixes.",
		Fix:   "Upgrade the firmware from the Packages page, then reboot.",
		Link:  "packages", Menus: []string{"/system/routerboard"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/system/routerboard")
			if !ok || r["current-firmware"] == "" || r["upgrade-firmware"] == "" {
				return Unknown, nil
			}
			if r["current-firmware"] != r["upgrade-firmware"] {
				return Fail, []string{r["current-firmware"] + " installed, " + r["upgrade-firmware"] + " available"}
			}
			return Pass, nil
		},
	},
	{
		ID: "sys.ntp", Category: "System", Severity: Low,
		Title: "The NTP client is disabled",
		Why:   "A wrong clock breaks certificate checks and makes the logs useless in an investigation.",
		Fix:   "Enable the NTP client with at least two servers.",
		Link:  "ntp-client", Menus: []string{"/system/ntp/client"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/system/ntp/client")
			if !ok {
				return Unknown, nil
			}
			if !yes(r["enabled"]) {
				return Fail, []string{"enabled=no"}
			}
			return Pass, nil
		},
	},
	{
		ID: "sys.remote-log", Category: "System", Severity: Low,
		Title: "Logs are not sent off the router",
		Why:   "The router's own log is lost on reboot and can be cleared by whoever gets in.",
		Fix:   "Point a logging action at a syslog server, and send the info, warning, error and critical topics to it.",
		Link:  "logging", Menus: []string{"/system/logging", "/system/logging/action"},
		Eval: func(in Inputs) (Status, []string) {
			rules, ok := in.get("/system/logging")
			actions, ok2 := in.get("/system/logging/action")
			if !ok || !ok2 {
				return Unknown, nil
			}
			// RouterOS ships a "remote" action pointing at 0.0.0.0: it exists
			// on every router and sends nowhere, so it does not count.
			remote := map[string]bool{}
			for _, a := range actions {
				if a["target"] == "remote" && a["remote"] != "" && a["remote"] != "0.0.0.0" && a["remote"] != "::" {
					remote[a["name"]] = true
				}
			}
			for _, r := range rules {
				if enabledRow(r) && remote[r["action"]] {
					return Pass, nil
				}
			}
			return Fail, []string{"no logging rule uses a remote action"}
		},
	},
	{
		ID: "sys.script-permissions", Category: "System", Severity: Medium,
		Title: "A script runs without permission checks",
		Why:   "dont-require-permissions lets a script do what its owner's policy would not, when anything with lower rights starts it.",
		Fix:   "Clear Don't Require Permissions on each listed script.",
		Link:  "scripts", Menus: []string{"/system/script"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/system/script")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				if yes(r["dont-require-permissions"]) {
					bad = append(bad, r["name"])
				}
			}
			return verdict(bad)
		},
	},
	{
		ID: "sys.device-mode", Category: "System", Severity: Info,
		Title: "Device mode allows every feature",
		Why:   "Device mode (RouterOS 7.17+) can switch off features an attacker would use, such as the scheduler, fetch and the sniffer.",
		Fix:   "Consider /system device-mode update mode=home, confirmed with the reset button.",
		Menus: []string{"/system/device-mode"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/system/device-mode")
			if !ok || r["mode"] == "" {
				return Unknown, nil
			}
			if r["mode"] == "advanced" || r["mode"] == "enterprise" {
				return Fail, []string{"mode=" + r["mode"]}
			}
			return Pass, nil
		},
	},

	// ── Wireless ───────────────────────────────────────────────────────────
	wifiCheck("wifi.open", "A wireless network has no encryption", High,
		"Anyone in range can join an open network and read its traffic.",
		"Set WPA2 or WPA3 on every security profile.",
		func(auth, _ string) bool { return strings.TrimSpace(auth) == "" }),
	wifiCheck("wifi.weak", "A wireless network allows WPA or TKIP", Medium,
		"WPA (version 1) and TKIP are broken, and let a client downgrade the whole network.",
		"Use WPA2-PSK or WPA3 with CCMP only.",
		func(auth, cipher string) bool {
			for _, a := range strings.Split(auth, ",") {
				if a == "wpa-psk" || a == "wpa-eap" {
					return true
				}
			}
			return strings.Contains(cipher, "tkip")
		}),
	{
		ID: "wifi.wps", Category: "Wireless", Severity: Medium,
		Title: "WPS is enabled",
		Why:   "WPS lets a device join by pressing a button, or by brute-forcing its PIN, without the passphrase.",
		Fix:   "Set WPS to disable on every wifi security profile.",
		Link:  "wifi-networks", Menus: []string{"/interface/wifi/security"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/interface/wifi/security")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				if w := r["wps"]; w != "" && w != "disable" {
					bad = append(bad, r["name"]+" (wps="+w+")")
				}
			}
			return verdict(bad)
		},
	},

	// ── Certificates ───────────────────────────────────────────────────────
	{
		ID: "cert.expiring", Category: "Certificates", Severity: Medium,
		Title: "A certificate has expired or expires within 30 days",
		Why:   "An expired certificate breaks the SSL services and VPNs that use it, and invites turning verification off.",
		Fix:   "Renew or replace the listed certificates.",
		Link:  "certificates", Menus: []string{"/certificate"},
		Eval: func(in Inputs) (Status, []string) {
			rows, ok := in.get("/certificate")
			if !ok {
				return Unknown, nil
			}
			var bad []string
			for _, r := range rows {
				t, err := time.Parse("2006-01-02 15:04:05", r["invalid-after"])
				if err != nil {
					continue
				}
				switch days := t.Sub(in.Now).Hours() / 24; {
				case days < 0:
					bad = append(bad, r["name"]+" (expired "+t.Format("2006-01-02")+")")
				case days < 30:
					bad = append(bad, fmt.Sprintf("%s (expires in %d days)", r["name"], int(days)))
				}
			}
			return verdict(bad)
		},
	},
}

// restricted: a service with an allowed-address list (available-from in
// RouterOS 7.24, address before it).
func restricted(r Row) bool { return strings.Trim(r["available-from"]+r["address"], ", ") != "" }

// positive is a matcher that names something rather than excluding it.
func positive(v string) bool { return v != "" && !strings.HasPrefix(v, "!") }

// ── Check builders for the repeated shapes ──────────────────────────────────

func macCheck(id, title, menu, why string) Check {
	return Check{
		ID: id, Category: "Management access", Severity: Medium, Title: title, Why: why,
		Fix:   "Set its Allowed Interface List to the LAN list (or none).",
		Menus: []string{menu},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one(menu)
			if !ok {
				return Unknown, nil
			}
			if l := r["allowed-interface-list"]; l == "all" || l == "static" || l == "dynamic" {
				return Fail, []string{"allowed-interface-list=" + l}
			}
			return Pass, nil
		},
	}
}

func fwHas(chain string, has func([]Row, string) bool, missing string) func(Inputs) (Status, []string) {
	return func(in Inputs) (Status, []string) {
		rows, ok := in.get("/ip/firewall/filter")
		if !ok {
			return Unknown, nil
		}
		if has(rows, chain) {
			return Pass, nil
		}
		return Fail, []string{missing}
	}
}

func ipSetting(id, title, key, bad, why, fix string) Check {
	return Check{
		ID: id, Category: "Firewall", Severity: Low, Title: title, Why: why, Fix: fix,
		Menus: []string{"/ip/settings"},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one("/ip/settings")
			if !ok {
				return Unknown, nil
			}
			if r[key] == bad {
				return Fail, []string{key + "=" + bad}
			}
			return Pass, nil
		},
	}
}

// onCheck fails when a settings menu's `enabled` is on.
func onCheck(id, category, title string, sev Severity, menu, why, fix, link string) Check {
	return Check{
		ID: id, Category: category, Severity: sev, Title: title, Why: why, Fix: fix, Link: link,
		Menus: []string{menu},
		Eval: func(in Inputs) (Status, []string) {
			r, ok := in.one(menu)
			if !ok {
				return Unknown, nil
			}
			if yes(r["enabled"]) {
				return Fail, []string{"enabled"}
			}
			return Pass, nil
		},
	}
}

// snmpCheck fails when SNMP is on and a community it answers matches `bad`.
func snmpCheck(id, title string, sev Severity, why, fix string, bad func(Row) bool) Check {
	return Check{
		ID: id, Category: "Exposed services", Severity: sev, Title: title, Why: why, Fix: fix, Link: "snmp",
		Menus: []string{"/snmp", "/snmp/community"},
		Eval: func(in Inputs) (Status, []string) {
			s, ok := in.one("/snmp")
			if !ok {
				return Unknown, nil
			}
			if !yes(s["enabled"]) {
				return Pass, nil
			}
			rows, ok := in.get("/snmp/community")
			if !ok {
				return Unknown, nil
			}
			var hit []string
			for _, r := range rows {
				if enabledRow(r) && bad(r) {
					hit = append(hit, r["name"])
				}
			}
			return verdict(hit)
		},
	}
}

// wifiCheck reads both wireless stacks: the wifi package's security profiles
// and the legacy wireless package's. Unknown only when the router has neither.
func wifiCheck(id, title string, sev Severity, why, fix string, bad func(auth, cipher string) bool) Check {
	return Check{
		ID: id, Category: "Wireless", Severity: sev, Title: title, Why: why, Fix: fix, Link: "wifi-networks",
		Menus: []string{"/interface/wifi/security", "/interface/wireless/security-profiles"},
		Eval: func(in Inputs) (Status, []string) {
			wifi, okW := in.get("/interface/wifi/security")
			legacy, okL := in.get("/interface/wireless/security-profiles")
			if !okW && !okL {
				return Unknown, nil
			}
			var hit []string
			for _, r := range wifi {
				if bad(r["authentication-types"], r["encryption"]) {
					hit = append(hit, r["name"])
				}
			}
			for _, r := range legacy {
				auth := r["authentication-types"]
				if r["mode"] == "none" {
					auth = ""
				}
				if bad(auth, r["unicast-ciphers"]) {
					hit = append(hit, r["name"])
				}
			}
			return verdict(hit)
		},
	}
}

// ── The overview's facts ────────────────────────────────────────────────────

// Facts are the numbers the overview's cards show beside the findings.
type Facts struct {
	Services        []ServiceFact `json:"services"`
	Installed       string        `json:"installed"`
	Latest          string        `json:"latest"`
	FirmwareCurrent string        `json:"firmwareCurrent"`
	FirmwareUpgrade string        `json:"firmwareUpgrade"`
	Users           int           `json:"users"`
	FullUsers       int           `json:"fullUsers"`
	MinPasswordLen  string        `json:"minPasswordLen"`
	AdminEnabled    bool          `json:"adminEnabled"`
}

// ServiceFact is one management service on the attack-surface card.
type ServiceFact struct {
	Name       string `json:"name"`
	Port       string `json:"port"`
	Enabled    bool   `json:"enabled"`
	Restricted bool   `json:"restricted"`
	Plaintext  bool   `json:"plaintext"`
}

func buildFacts(in Inputs) Facts {
	f := Facts{Services: []ServiceFact{}}
	if rows, ok := in.get("/ip/service"); ok {
		for _, r := range rows {
			if yes(r["dynamic"]) {
				continue
			}
			f.Services = append(f.Services, ServiceFact{Name: r["name"], Port: r["port"], Enabled: enabledRow(r),
				Restricted: restricted(r), Plaintext: plaintextServices[r["name"]]})
		}
	}
	if r, ok := in.one("/system/package/update"); ok {
		f.Installed, f.Latest = r["installed-version"], r["latest-version"]
	}
	if r, ok := in.one("/system/routerboard"); ok {
		f.FirmwareCurrent, f.FirmwareUpgrade = r["current-firmware"], r["upgrade-firmware"]
	}
	if users, ok := in.get("/user"); ok {
		full := map[string]bool{}
		if groups, ok := in.get("/user/group"); ok {
			full = fullGroups(groups)
		}
		for _, u := range users {
			if !enabledRow(u) {
				continue
			}
			f.Users++
			if full[u["group"]] {
				f.FullUsers++
			}
			if u["name"] == "admin" {
				f.AdminEnabled = true
			}
		}
	}
	if r, ok := in.one("/user/settings"); ok {
		f.MinPasswordLen = r["minimum-password-length"]
	}
	return f
}
