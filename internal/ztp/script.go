package ztp

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"mikrodash/internal/cfgtpl"
)

// The bootstrap scripts: what an operator runs on a router so it calls home.
//
// ── MIKRODASH'S OWN FIXED SCRIPT, NEVER USER TEXT ───────────────────────────
//
// Config Management refuses scripting in templates, because a template is text
// someone wrote. These are the other case, the one its reset bootstrap set the
// precedent for: RouterOS script MikroDash itself writes, with every value that
// came from anywhere else (a label, a token, a key, an endpoint) passed through
// cfgtpl.QuoteROS, so nothing can end a literal early. The generators are pure:
// the same inputs give the same text, which the golden tests pin.
//
// ── WHAT EACH STEP RESTS ON (cmd/ztpprobe, RouterOS 7.24.4) ─────────────────
//
//   - the device names itself by routerboard serial, else the licence's
//     system-id: a CHR has no routerboard at all (z1);
//   - the WireGuard interface is given NO listen port: a port another interface
//     holds makes RouterOS create it disabled and silent (z8);
//   - the enrolment body is `:serialize to=json`, not concatenation (z9), and
//     is POSTed with `/tool/fetch … output=user as-value`, which hands the reply
//     back and raises a catchable error on an HTTP refusal (z3);
//   - the enrolment runs from a stored script and a scheduler that remove
//     themselves once it succeeds (z7, z9), so a router that boots before the
//     tunnel is up simply tries again a minute later;
//   - the API user is limited with `address=` to where MikroDash connects from,
//     which really refuses a login from anywhere else (z5), and the input accept
//     rule goes first (z6).
//
// Every step tolerates having run before, so running a script twice is safe.

// Names on the router, fixed so a script and MikroDash agree on them.
const (
	IfaceName  = "mikrodash-ztp"       // the device's own tunnel interface
	EnrolIface = "mikrodash-ztp-enrol" // a generic script's shared-key interface
	// UserName is the API user MikroDash signs in as. NOT "mikrodash": that is
	// the name a router already managed by hand usually has, and the script
	// restricts this user's address and sets its password, which would lock the
	// existing connection out.
	UserName    = "mikrodash-ztp"
	EnrolScript = "mikrodash-ztp-enrol" // the stored enrolment script and its scheduler
	Comment     = "MikroDash ZTP"
	EnrolPath   = "/enrol" // the enrolment route, on the server's tunnel address
	LANPath     = "/api/ztp/enrol"
)

// passwordAlphabet has no look-alikes (0/O, 1/l/I), so a password read aloud
// from a router's log survives it.
const passwordAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// Instance is what every script needs to know about this MikroDash.
type Instance struct {
	ID        string     // the instance's random identifier
	PublicKey string     // this MikroDash's WireGuard public key
	Endpoint  string     // the host routers dial: an IP or a name
	Port      int        // the UDP port
	Server    netip.Addr // this MikroDash's tunnel address
}

// Remote is one pre-provisioned device reached through the tunnel.
type Remote struct {
	Label      string
	Token      string
	PrivateKey string     // the device's own, made by MikroDash
	Address    netip.Addr // its /32 on the tunnel
	Expires    time.Time
}

// Local is one pre-provisioned device reached on the LAN.
type Local struct {
	Label    string
	Token    string
	Password string     // made by MikroDash; the script is the only place it is written
	From     netip.Addr // where MikroDash connects from, as the router will see it
	URL      string     // MikroDash's own address on the LAN, e.g. http://192.0.2.10:3081
	Expires  time.Time
}

// Batch is a generic script for any number of devices.
type Batch struct {
	Token      string
	PrivateKey string // the shared enrolment key
	Enrolment  netip.Prefix
	Expires    time.Time
}

var q = cfgtpl.QuoteROS

func header(b *strings.Builder, title, instance string, expires time.Time, secret string) {
	fmt.Fprintf(b, "# MikroDash zero-touch provisioning: %s\n", oneLine(title))
	fmt.Fprintf(b, "# Instance %s. Valid until %s.\n", oneLine(instance), expires.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintf(b, "# THIS FILE HOLDS A SECRET (%s). Keep it like a password.\n", secret)
	// No command spelled out here: the Devices wizard shows the exact one. A
	// literal naming it would read as a call site to TestImportHasOneCallSite,
	// and this text is for a person; MikroDash never runs it.
	b.WriteString("# Run it on the router: upload the file and import it, or paste it into a terminal.\n")
	b.WriteString("# It is safe to run twice.\n\n")
}

// oneLine keeps a value on its comment line: a newline in a label would end the
// comment and start a command.
func oneLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// identify is the device's identity, as enrolment script lines.
func identify(b *strings.Builder) {
	b.WriteString(":local id \"\"\n")
	b.WriteString(":do { :set id [/system routerboard get serial-number] } on-error={}\n")
	b.WriteString(":if ([:len $id] = 0) do={ :do { :set id [/system license get system-id] } on-error={} }\n")
	b.WriteString(":local model [/system resource get board-name]\n")
	b.WriteString(":local version [/system resource get version]\n")
	b.WriteString(":local identity [/system identity get name]\n")
}

// finish is the enrolment's success path: log, then remove the scheduler and
// the script itself.
func finish(b *strings.Builder, extra string) {
	fmt.Fprintf(b, "  :local r [/tool fetch url=$url http-method=post http-header-field=%s http-data=$body output=user as-value]\n",
		q("Content-Type: application/json"))
	b.WriteString(extra)
	fmt.Fprintf(b, "  :log info %s\n", q("MikroDash: enrolled"))
	fmt.Fprintf(b, "  /system scheduler remove [find name=%s]\n", q(EnrolScript))
	fmt.Fprintf(b, "  /system script remove [find name=%s]\n", q(EnrolScript))
	b.WriteString("} do={\n")
	// A REFUSAL IS FINAL: a wrong token or serial will not become right by
	// asking again every minute. Anything else (the tunnel not up yet) retries.
	b.WriteString("  :if ([:typeof [:find $e \"403\"]] != \"nil\") do={\n")
	fmt.Fprintf(b, "    :log error (%s . $e)\n", q("MikroDash refused this device: "))
	fmt.Fprintf(b, "    /system scheduler remove [find name=%s]\n", q(EnrolScript))
	b.WriteString("  } else={\n")
	fmt.Fprintf(b, "    :log warning (%s . $e)\n", q("MikroDash: not enrolled yet, trying again in a minute: "))
	b.WriteString("  }\n}\n")
}

// installEnrol stores the enrolment script, schedules it every minute and runs
// it once now.
func installEnrol(b *strings.Builder, src string) {
	const policy = "read,write,policy,test,sensitive"
	b.WriteString("/system script\n")
	fmt.Fprintf(b, "remove [find name=%s]\n", q(EnrolScript))
	fmt.Fprintf(b, "add name=%s policy=%s comment=%s source=%s\n", q(EnrolScript), policy, q(Comment), q(src))
	b.WriteString("/system scheduler\n")
	fmt.Fprintf(b, "remove [find name=%s]\n", q(EnrolScript))
	fmt.Fprintf(b, "add name=%s interval=1m on-event=%s policy=%s comment=%s\n", q(EnrolScript), q(EnrolScript), policy, q(Comment))
	fmt.Fprintf(b, "/system script run [find name=%s]\n", q(EnrolScript))
}

// firstAccept adds the input accept rule, first, once. `match` is the rule's
// matcher, e.g. `in-interface="mikrodash-ztp"`.
func firstAccept(b *strings.Builder, match string) {
	b.WriteString("/ip firewall filter\n")
	fmt.Fprintf(b, ":if ([:len [find comment=%s]] = 0) do={ :local first [:pick [find] 0]; "+
		":if ([:len $first] > 0) do={ add chain=input action=accept %s comment=%s place-before=$first } "+
		"else={ add chain=input action=accept %s comment=%s } }\n", q(Comment), match, q(Comment), match, q(Comment))
}

// apiUser creates the API user, limited to `from`, with `password` (a literal
// or a script expression).
func apiUser(b *strings.Builder, from netip.Addr, password string) {
	b.WriteString("/user\n")
	fmt.Fprintf(b, ":if ([:len [find name=%s]] = 0) do={ add name=%s group=full address=%s password=%s comment=%s }\n",
		q(UserName), q(UserName), q(from.String()+"/32"), password, q(Comment))
	fmt.Fprintf(b, "set [find name=%s] address=%s\n", q(UserName), q(from.String()+"/32"))
}

// apiService makes sure MikroDash can reach the API from `from`: an address
// list that excludes it gains it, and if every API service is off, the plain
// one is turned on for that address alone.
//
// EVERY ENTRY, NOT ONE: a router can hold several entries of one service (per
// VRF), and `get [find name=…]` on several ids fails with "invalid internal
// item number" (measured on the lab CHR, which had three api-ssl entries), so
// the script walks each id.
func apiService(b *strings.Builder, from netip.Addr) {
	a := q(from.String() + "/32")
	b.WriteString("/ip service\n")
	fmt.Fprintf(b, ":foreach i in=[find where (name=\"api\" || name=\"api-ssl\")] do={ :local l [get $i address]; "+
		":if ([:len $l] > 0 && [:typeof [:find $l %s]] = \"nil\") do={ set $i address=($l, %s) } }\n", a, a)
	fmt.Fprintf(b, ":if ([:len [find where (name=\"api\" || name=\"api-ssl\") && disabled=no]] = 0) do={ "+
		":foreach i in=[find where name=\"api\"] do={ set $i disabled=no address=%s } }\n", a)
}

// tunnel creates the device's tunnel interface, its peer (this MikroDash) and
// its address. privateKey is a quoted literal, or "" to let RouterOS make one.
func tunnel(b *strings.Builder, inst Instance, iface, privateKey string, addr netip.Addr) {
	b.WriteString("/interface wireguard\n")
	key := ""
	if privateKey != "" {
		key = " private-key=" + q(privateKey)
	}
	fmt.Fprintf(b, ":if ([:len [find name=%s]] = 0) do={ add name=%s%s comment=%s }\n", q(iface), q(iface), key, q(Comment))
	fmt.Fprintf(b, "set [find name=%s] disabled=no\n", q(iface))
	b.WriteString("/interface wireguard peers\n")
	fmt.Fprintf(b, ":if ([:len [find interface=%s]] = 0) do={ add interface=%s public-key=%s endpoint-address=%s "+
		"endpoint-port=%d allowed-address=%s persistent-keepalive=25s comment=%s }\n",
		q(iface), q(iface), q(inst.PublicKey), q(inst.Endpoint), inst.Port, q(inst.Server.String()+"/32"), q(Comment))
	if addr.IsValid() {
		b.WriteString("/ip address\n")
		fmt.Fprintf(b, ":if ([:len [find interface=%s]] = 0) do={ add address=%s network=%s interface=%s comment=%s }\n",
			q(iface), q(addr.String()+"/32"), q(inst.Server.String()), q(iface), q(Comment))
	}
}

// RemoteScript is a pre-provisioned device's script, reached through the
// tunnel. It carries the device's private key and a token: a secret.
func RemoteScript(inst Instance, d Remote) string {
	var b strings.Builder
	header(&b, d.Label+" (remote, through the tunnel)", inst.ID, d.Expires, "this device's tunnel key and a one-time token")
	tunnel(&b, inst, IfaceName, d.PrivateKey, d.Address)
	firstAccept(&b, "in-interface="+q(IfaceName))
	// The password is made on the router, by the enrolment script, and sent
	// only inside the tunnel. Until then the user has a random one nobody knows.
	apiUser(&b, inst.Server, "[:rndstr length=32 from="+q(passwordAlphabet)+"]")
	apiService(&b, inst.Server)
	installEnrol(&b, remoteEnrol(inst, d))
	return b.String()
}

// remoteEnrol is the remote script's stored enrolment script, as the router
// will run it. It is quoted AGAIN to embed it in the file, so a value it leaves
// unquoted is escaped in the file and live code once stored: its own quoting is
// tested on this text, not only on the file's.
func remoteEnrol(inst Instance, d Remote) string {
	var s strings.Builder
	fmt.Fprintf(&s, ":local url %s\n", q("http://"+inst.Server.String()+EnrolPath))
	fmt.Fprintf(&s, ":local pw [:rndstr length=32 from=%s]\n", q(passwordAlphabet))
	identify(&s)
	fmt.Fprintf(&s, ":local body [:serialize to=json value={\"instance\"=%s;\"token\"=%s;\"serial\"=$id;\"model\"=$model;"+
		"\"version\"=$version;\"identity\"=$identity;\"password\"=$pw}]\n", q(inst.ID), q(d.Token))
	s.WriteString(":onerror e in={\n")
	// THE PASSWORD IS SET ONLY ONCE THE ENROLMENT IS ACCEPTED, so a refused
	// attempt never leaves the router with a password MikroDash does not have.
	finish(&s, fmt.Sprintf("  /user set [find name=%s] password=$pw\n", q(UserName)))
	return s.String()
}

// LocalScript is a pre-provisioned device's script, reached on the LAN. No
// tunnel: the router calls MikroDash's own address, and MikroDash dials it
// back. The password is MikroDash's, written here, so nothing credential-shaped
// crosses the LAN.
func LocalScript(inst Instance, d Local) string {
	var b strings.Builder
	header(&b, d.Label+" (local network)", inst.ID, d.Expires, "this device's API password and a one-time token")
	firstAccept(&b, "src-address="+q(d.From.String()))
	apiUser(&b, d.From, q(d.Password))
	fmt.Fprintf(&b, "set [find name=%s] password=%s\n", q(UserName), q(d.Password))
	apiService(&b, d.From)
	installEnrol(&b, localEnrol(inst, d))
	return b.String()
}

// localEnrol is the local script's stored enrolment script (see remoteEnrol).
func localEnrol(inst Instance, d Local) string {
	var s strings.Builder
	fmt.Fprintf(&s, ":local url %s\n", q(strings.TrimRight(d.URL, "/")+LANPath))
	identify(&s)
	fmt.Fprintf(&s, ":local body [:serialize to=json value={\"instance\"=%s;\"token\"=%s;\"serial\"=$id;\"model\"=$model;"+
		"\"version\"=$version;\"identity\"=$identity}]\n", q(inst.ID), q(d.Token))
	s.WriteString(":onerror e in={\n")
	finish(&s, "")
	return s.String()
}

// GenericScript is one script for many devices. Each comes up on the shared
// enrolment key at a random address in the enrolment range, which reaches only
// the enrolment endpoint, and sends its OWN interface's public key (RouterOS
// makes the key). MikroDash records it as waiting for an operator, gives its
// key its own /32, and answers with that address; the script moves to its own
// interface and removes the shared one.
func GenericScript(inst Instance, bt Batch) string {
	var b strings.Builder
	header(&b, "any device (mass deployment)", inst.ID, bt.Expires, "the shared enrolment key and a batch token")
	// THE DEVICE'S OWN INTERFACE, key made by the router, no address yet.
	tunnel(&b, inst, IfaceName, "", netip.Addr{})
	// THE SHARED ENROLMENT INTERFACE, at a random address in the enrolment /24.
	e := bt.Enrolment.Addr().As4()
	b.WriteString("/interface wireguard\n")
	fmt.Fprintf(&b, ":if ([:len [find name=%s]] = 0) do={ add name=%s private-key=%s comment=%s }\n",
		q(EnrolIface), q(EnrolIface), q(bt.PrivateKey), q(Comment))
	fmt.Fprintf(&b, "set [find name=%s] disabled=no\n", q(EnrolIface))
	b.WriteString("/interface wireguard peers\n")
	fmt.Fprintf(&b, ":if ([:len [find interface=%s]] = 0) do={ add interface=%s public-key=%s endpoint-address=%s "+
		"endpoint-port=%d allowed-address=%s persistent-keepalive=25s comment=%s }\n",
		q(EnrolIface), q(EnrolIface), q(inst.PublicKey), q(inst.Endpoint), inst.Port, q(inst.Server.String()+"/32"), q(Comment))
	b.WriteString("/ip address\n")
	fmt.Fprintf(&b, ":if ([:len [find interface=%s]] = 0) do={ add address=(%s . [:rndnum from=2 to=254] . \"/32\") "+
		"network=%s interface=%s comment=%s }\n",
		q(EnrolIface), q(fmt.Sprintf("%d.%d.%d.", e[0], e[1], e[2])), q(inst.Server.String()), q(EnrolIface), q(Comment))
	firstAccept(&b, "in-interface="+q(IfaceName))
	apiUser(&b, inst.Server, "[:rndstr length=32 from="+q(passwordAlphabet)+"]")
	apiService(&b, inst.Server)
	installEnrol(&b, genericEnrol(inst, bt))
	return b.String()
}

// genericEnrol is the generic script's stored enrolment script (see
// remoteEnrol).
func genericEnrol(inst Instance, bt Batch) string {
	var s strings.Builder
	fmt.Fprintf(&s, ":local url %s\n", q("http://"+inst.Server.String()+EnrolPath))
	fmt.Fprintf(&s, ":local pw [:rndstr length=32 from=%s]\n", q(passwordAlphabet))
	identify(&s)
	fmt.Fprintf(&s, ":local key [/interface wireguard get [find name=%s] public-key]\n", q(IfaceName))
	fmt.Fprintf(&s, ":local body [:serialize to=json value={\"instance\"=%s;\"token\"=%s;\"serial\"=$id;\"model\"=$model;"+
		"\"version\"=$version;\"identity\"=$identity;\"password\"=$pw;\"publicKey\"=$key}]\n", q(inst.ID), q(bt.Token))
	s.WriteString(":onerror e in={\n")
	// ON ACCEPTANCE: the password, then the address MikroDash gave this key,
	// then the shared interface goes (its address and peer with it).
	var x strings.Builder
	fmt.Fprintf(&x, "  /user set [find name=%s] password=$pw\n", q(UserName))
	x.WriteString("  :local j [:deserialize from=json value=($r->\"data\")]\n")
	fmt.Fprintf(&x, "  /ip address add address=(($j->\"address\") . \"/32\") network=%s interface=%s comment=%s\n",
		q(inst.Server.String()), q(IfaceName), q(Comment))
	fmt.Fprintf(&x, "  /ip address remove [find interface=%s]\n", q(EnrolIface))
	fmt.Fprintf(&x, "  /interface wireguard peers remove [find interface=%s]\n", q(EnrolIface))
	fmt.Fprintf(&x, "  /interface wireguard remove [find name=%s]\n", q(EnrolIface))
	finish(&s, x.String())
	return s.String()
}
