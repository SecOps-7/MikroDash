package cfgtpl

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"mikrodash/internal/guard"
	"mikrodash/internal/resource"
)

// A Finding is one thing the analyser has to say about a template.
//
// ── THREE LEVELS, AND WHAT EACH COSTS THE OPERATOR ──────────────────────────
//
//	refuse  it does not deploy, and nothing unlocks it
//	ack     it deploys only once the operator has acknowledged THIS finding
//	        for THIS router — the canary's acknowledgement is not the rest's
//	warn    it is shown and does not block
type Finding struct {
	Level   string `json:"level"`
	Code    string `json:"code"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// The levels, and the two profiles.
const (
	Refuse = "refuse"
	Ack    = "ack"
	Warn   = "warn"

	// Additions merge into a running router. The strict profile.
	Additions = "additions"
	// Full is a whole export replayed onto a reset router. A real export
	// legitimately carries users, scripts and settings menus that an addition
	// may not touch — so those move from refuse to acknowledge — but it is
	// still parsed by the same dialect, so scripting constructs never pass.
	Full = "full"
)

// Worst is the most serious level among findings, "" when there are none.
func Worst(fs []Finding) string {
	rank := map[string]int{Warn: 1, Ack: 2, Refuse: 3}
	worst := ""
	for _, f := range fs {
		if rank[f.Level] > rank[worst] {
			worst = f.Level
		}
	}
	return worst
}

// ── WHAT THE ANALYSER DOES NOT NEED TO REFUSE ───────────────────────────────
//
// Actions. `reboot`, `reset-configuration`, `fetch`, `import`, `execute`,
// `sign`, `upgrade`, `run` are not add/set/remove/enable/disable/unset, so the
// PARSER refuses them before this sees a line. What is left is configuration,
// and the question here is which configuration is itself dangerous.

// refusedMenus hold code or credentials, or reach below configuration. Adding
// to them is refused in both profiles except where noted.
var refusedMenus = []struct {
	prefix, why string
	fullAck     bool // a whole export legitimately carries it: acknowledge instead
}{
	{"/system/script", "a script is code that runs with the router's rights", true},
	{"/system/scheduler", "a scheduler entry runs code on a timer", true},
	// A full export carries /certificate SETTINGS (never a certificate: they
	// are not exported), a swap file under /disk, and container configuration
	// such as the registry. Measured on the lab CHR's own export (2026-09-21):
	// refusing them made a real router's export undeployable, so a full
	// replacement acknowledges them. An addition still may not touch them.
	{"/certificate", "certificates carry private keys", true},
	{"/file", "files are not configuration", false},
	{"/disk", "disks and swap files are storage, not network configuration", true},
	{"/container", "a container runs an image — code", true},
	{"/tool/netwatch", "netwatch entries run scripts on up and down", true},
	{"/user/ssh-keys", "SSH keys are credentials", false},
	{"/user/group", "user groups decide who may do what", true},
	{"/user/aaa", "RADIUS login decides whether anyone can sign in", true},
}

// userMenu is `/user` itself — the accounts, MikroDash's among them.
const userMenu = "/user"

// extraMenus are the SETTINGS menus an addition may configure though the
// resource registry has no page for them. The registry covers menus with
// rows and a page; these are single-row settings. Each is configuration only:
// no field in it runs code, and its effects are the ones its name says.
//
// A MENU NOT HERE AND NOT IN THE REGISTRY IS REFUSED for an addition, because
// the app cannot check a menu it does not know. Adding one is a decision, made
// here, with its reason.
var extraMenus = map[string]string{
	"/ip/dns":                         "resolver servers and whether it answers the LAN",
	"/ip/dns/static":                  "static DNS names",
	"/ip/settings":                    "IP stack settings such as TCP SYN cookies",
	"/ipv6/settings":                  "IPv6 stack settings",
	"/ipv6/firewall/address-list":     "IPv6 address lists for the firewall",
	"/system/identity":                "the router's name",
	"/system/ntp/server":              "whether the router serves time",
	"/queue/type":                     "queue disciplines such as CAKE and PCQ",
	"/ip/upnp":                        "UPnP on or off",
	"/ip/proxy":                       "the web proxy on or off",
	"/ip/socks":                       "the SOCKS proxy on or off",
	"/ip/cloud":                       "IP Cloud's DDNS on or off",
	"/ip/ssh":                         "SSH crypto settings (host-key regeneration is an action, refused by the parser)",
	"/ip/neighbor/discovery-settings": "which interfaces announce the router",
	"/tool/mac-server":                "MAC telnet access",
	"/tool/mac-server/mac-winbox":     "MAC WinBox access",
	"/tool/mac-server/ping":           "MAC ping",
	"/tool/bandwidth-server":          "the bandwidth-test server on or off",
	"/tool/romon":                     "RoMON on or off",
	"/user/settings":                  "the password policy",
}

// KnownMenu reports whether an addition may touch a menu: one the resource
// registry has, or one of the settings menus named in extraMenus.
func KnownMenu(menu string) bool {
	_, extra := extraMenus[menu]
	return menuIndex[menu] != nil || extra
}

// RefusedMenu reports whether an addition may never touch a menu: one of
// refusedMenus, or the accounts. A capture of one would be a template that can
// never be deployed.
func RefusedMenu(menu string) bool {
	if menu == userMenu {
		return true
	}
	for _, rm := range refusedMenus {
		if menu == rm.prefix || strings.HasPrefix(menu, rm.prefix+"/") {
			return true
		}
	}
	return false
}

// CaptureMenus is every menu a fragment may be captured from: known, and not
// one an addition may never touch. Sorted, for a picker.
func CaptureMenus() []string {
	out := []string{}
	for m := range menuIndex {
		if !RefusedMenu(m) {
			out = append(out, m)
		}
	}
	for m := range extraMenus {
		if !RefusedMenu(m) {
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

// menuIndex is the registry, by menu path.
var menuIndex = func() map[string]*resource.Resource {
	m := map[string]*resource.Resource{}
	for _, r := range resource.All() {
		m[r.Menu] = r
	}
	return m
}()

// Analyze judges a template's STRUCTURE — what it does, not yet to which rows.
// It needs no router, so it runs when a template is saved; AnalyzeLive runs
// again before it is sent, with the router's management path in hand.
func Analyze(t *Template, profile string) []Finding {
	var out []Finding
	add := func(l Line, level, code, f string, a ...any) {
		out = append(out, Finding{Level: level, Code: code, Line: l.Num, Message: fmt.Sprintf(f, a...)})
	}
	for _, l := range t.Lines {
		menu := l.Path()

		// Menus that are dangerous to configure at all.
		refused := false
		for _, rm := range refusedMenus {
			if menu == rm.prefix || strings.HasPrefix(menu, rm.prefix+"/") {
				level := Refuse
				if profile == Full && rm.fullAck {
					level = Ack
				}
				add(l, level, "menu", "%s: %s", menu, rm.why)
				refused = true
				break
			}
		}
		if menu == userMenu {
			if profile == Full {
				add(l, Ack, "users", "/user: an account in an export comes back with NO password — "+
					"RouterOS never exports one; keep-users keeps the target's own accounts")
			} else {
				add(l, Refuse, "users", "/user: accounts are not changed by a template — "+
					"MikroDash's own login is one of them")
			}
			refused = true
		}

		// THE FIELD CHECKS RUN EVEN ON A MENU ALREADY REFUSED OR ACKNOWLEDGED.
		// Skipping them once hid a literal `/user password=` in a full export —
		// a line that is only acknowledged there, so the password would have
		// been stored with the template on an acknowledgement about accounts.
		// TestEverySecretFieldWarnsWhenWrittenLiterally walks the registry, and
		// found it.
		res := menuIndex[menu]
		checkFields(l, res, profile, add)
		if refused {
			continue
		}

		if profile == Additions && !KnownMenu(menu) {
			add(l, Refuse, "unknown-menu", "%s is not a menu MikroDash knows, so it cannot check what "+
				"this line does", menu)
			continue
		}

		// Row numbers differ between routers; an export uses them only for
		// default rows.
		if len(l.Pos) > 0 && profile == Additions {
			add(l, Refuse, "positional", "%s %s: a row number means a different row on every router — "+
				"select with [ find name=… ]", menu, l.Verb)
		}

		// A command that must name a row but names none.
		if (l.Verb == "remove" || l.Verb == "enable" || l.Verb == "disable" || l.Verb == "unset") &&
			!l.HasFind && len(l.Pos) == 0 {
			add(l, Refuse, "no-selector", "%s %s names no row: add [ find name=… ]", menu, l.Verb)
		}

		// `[ find ]` with no terms is every row.
		if l.HasFind && len(l.Find) == 0 {
			add(l, Ack, "every-row", "%s %s [ find ] with nothing to match changes EVERY row of %s",
				menu, l.Verb, menu)
		}

	}
	return out
}

// checkFields flags the fields of a registry menu that are code, or credentials
// written as literals.
func checkFields(l Line, res *resource.Resource, profile string,
	add func(Line, string, string, string, ...any)) {
	if res == nil {
		return
	}
	menu := l.Path()
	for _, a := range l.Args {
		f := fieldByROS(res, a.Name)
		if f == nil {
			continue
		}
		if f.Code {
			level := Refuse
			if profile == Full {
				level = Ack
			}
			add(l, level, "code-field", "%s %s=: this field is code that the router runs", menu, a.Name)
		}
		if f.Type == resource.TypeSecret {
			if _, isVar := a.Value.Var(); !isVar {
				add(l, Warn, "literal-secret", "%s %s=: a credential written into the template is stored "+
					"with it — make it a secret variable", menu, a.Name)
			}
		}
	}
}

func fieldByROS(r *resource.Resource, ros string) *resource.Field {
	for i := range r.Fields {
		if r.Fields[i].ROS == ros {
			return &r.Fields[i]
		}
	}
	return nil
}

// LiveContext is what MikroDash knows about ONE router's way in.
type LiveContext struct {
	// FW is the resolved management path: MikroDash's addresses as the router
	// sees them, the interfaces they arrive on, the API port. The same context
	// the firewall guard uses on the Firewall page.
	FW guard.FWContext
	// APIService is the service MikroDash is connected through: api or api-ssl.
	APIService string
}

// firewallInput is each firewall menu and the chain that decides whether
// MikroDash's own traffic reaches the router.
var firewallInput = map[string]string{
	"/ip/firewall/filter":   "input",
	"/ip/firewall/raw":      "prerouting",
	"/ipv6/firewall/filter": "input",
	"/ipv6/firewall/raw":    "prerouting",
}

// recoveryServices are the ways back in when MikroDash is cut off.
var recoveryServices = map[string]bool{"winbox": true, "ssh": true, "www-ssl": true}

// AnalyzeLive judges a template against one router's management path.
//
// ── GIVE IT THE FILLED TEMPLATE ─────────────────────────────────────────────
//
// A deploy analyses Fill(t, values): what the router will actually receive.
// A placeholder still present is UNKNOWN, and unknown is read in the direction
// that flags, never the one that passes: an unknown service name may be
// MikroDash's own, an unknown `disabled=` may be yes, an unknown address is
// still a change. The Library, which has no values yet, relies on exactly that.
// Reading a placeholder as empty once let `set [ find name={{svc}} ]
// disabled=yes` pass for svc=api-ssl.
//
// ── IT DOES NOT MODEL RULE ORDER, AND SAYS SO ───────────────────────────────
//
// A drop that could match MikroDash is flagged even if an accept above it
// would let MikroDash through, because what is above it is the running
// router's rules plus this template's, in an order the template may not
// control. The firewall guard's own header refuses to model order for the same
// reason. What actually proves a deploy did not lock MikroDash out is a fresh
// login after it — which is what the deploy checks — and, for a lock-class
// template, the dead-man that undoes it if that login fails.
func AnalyzeLive(t *Template, ctx LiveContext) []Finding {
	var out []Finding
	add := func(l Line, level, code, f string, a ...any) {
		out = append(out, Finding{Level: level, Code: code, Line: l.Num, Message: fmt.Sprintf(f, a...)})
	}
	// arg is a setting's literal value, "" when absent or unknown. It feeds
	// the firewall guard, where "" means "any", which is the flagging reading.
	arg := func(l Line, name string) string {
		v, ok := l.Arg(name)
		if !ok {
			return ""
		}
		s, _ := v.Literal()
		return s
	}
	// may reports whether a setting is, or could be, want: present and either
	// that literal or not known yet.
	may := func(l Line, name, want string) bool {
		v, ok := l.Arg(name)
		if !ok {
			return false
		}
		s, lit := v.Literal()
		return !lit || s == want
	}
	// findName is the name a selector names. An unknown one is not named: it
	// could be any row, MikroDash's own included.
	findName := func(l Line) (string, bool) {
		for _, a := range l.Find {
			if a.Name == "name" {
				s, lit := a.Value.Literal()
				return s, lit
			}
		}
		return "", false
	}

	for _, l := range t.Lines {
		menu := l.Path()

		// ── A drop on the path MikroDash arrives by ──────────────────────────
		if chain, ok := firewallInput[menu]; ok && l.Verb == "add" && may(l, "chain", chain) {
			if may(l, "action", "drop") || may(l, "action", "reject") || may(l, "action", "tarpit") {
				rule := guard.FWRule{
					Chain: chain, Action: firstNonEmpty(arg(l, "action"), "drop"),
					SrcAddress: arg(l, "src-address"), DstAddress: arg(l, "dst-address"),
					Protocol: arg(l, "protocol"), DstPort: arg(l, "dst-port"),
					InInterface: arg(l, "in-interface"), Disabled: arg(l, "disabled") == "yes",
				}
				// A drop of only invalid packets cannot cut a management
				// session, which is established; MikroTik's own firewall drops
				// them first.
				state, _ := l.Arg("connection-state")
				onlyInvalid := false
				if s, lit := state.Literal(); lit && s == "invalid" {
					onlyInvalid = true
				}
				switch {
				case rule.Disabled, onlyInvalid:
				case !ctx.FW.Resolved:
					add(l, Ack, "lockout-unknown", "%s: a %s rule on %s, and MikroDash's own address on this "+
						"router is not known, so it cannot tell whether this cuts it off", menu, rule.Action, chain)
				case guard.MatchesUs(rule, ctx.FW):
					add(l, Ack, "lockout-firewall", "%s: this %s rule on %s can match MikroDash's own connection",
						menu, rule.Action, chain)
				}
			}
		}

		// ── The services MikroDash and a person use to get in ────────────────
		if menu == "/ip/service" && (l.Verb == "set" || l.Verb == "disable") {
			name, named := findName(l)
			disabling := l.Verb == "disable" || may(l, "disabled", "yes")
			ours := !named || name == ctx.APIService
			// Unknown before a router is chosen: named for what it is.
			svc := ctx.APIService
			if svc == "" {
				svc = "MikroDash's API service"
			}
			switch {
			case ours && disabling:
				add(l, Refuse, "own-service", "/ip/service: this disables %s, the service MikroDash is connected "+
					"through — it could never reconnect to see the result", svc)
			case ours && movesPort(l, ctx.FW.APIPort):
				add(l, Refuse, "own-service", "/ip/service: this moves %s to another port, and MikroDash would "+
					"reconnect to the old one", svc)
			case ours && has(l, "address"):
				add(l, Ack, "own-service-address", "/ip/service: this restricts where %s may be reached from — "+
					"it must include MikroDash's own address", svc)
			case named && recoveryServices[name] && disabling:
				add(l, Ack, "recovery-path", "/ip/service: this disables %s — if a later change locks MikroDash "+
					"out, that is one fewer way back in", name)
			}
		}

		// ── The classic instant lockout ──────────────────────────────────────
		if menu == "/interface/bridge" && l.Verb == "set" && may(l, "vlan-filtering", "yes") {
			add(l, Ack, "vlan-filtering", "/interface/bridge: turning on VLAN filtering drops every frame "+
				"not already allowed by the bridge's VLAN table, the management path's included")
		}

		// ── Interfaces and addresses MikroDash may arrive on ─────────────────
		if strings.HasPrefix(menu, "/interface") && (l.Verb == "disable" ||
			(l.Verb == "set" && may(l, "disabled", "yes"))) {
			name, named := findName(l)
			if !named || containsStr(ctx.FW.Interfaces, name) || !ctx.FW.Resolved {
				add(l, Ack, "lockout-interface", "%s: this disables an interface MikroDash may arrive on", menu)
			}
		}
		if menu == "/ip/address" && (l.Verb == "remove" || l.Verb == "disable") {
			add(l, Ack, "lockout-address", "/ip/address: removing or disabling an address can remove the one "+
				"MikroDash arrives on")
		}
		if (menu == "/ip/route" || menu == "/routing/rule") &&
			(l.Verb == "remove" || l.Verb == "disable" || l.Verb == "add") {
			add(l, Ack, "lockout-route", "%s: a routing change can take away the route MikroDash's replies use", menu)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

func has(l Line, name string) bool {
	_, ok := l.Arg(name)
	return ok
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// movesPort reports whether a /ip/service line sets a port other than the one
// MikroDash uses. The port it already uses is not a move: a real export writes
// `port=8729` on api-ssl, and refusing that refused every full export. A value
// that cannot be read as that number (a placeholder, a typo) is a move: not
// knowing is not a pass.
func movesPort(l Line, current int) bool {
	v, ok := l.Arg("port")
	if !ok {
		return false
	}
	lit, isLit := v.Literal()
	return !isLit || lit != strconv.Itoa(current) || current == 0
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
