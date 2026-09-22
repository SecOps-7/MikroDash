// Command ztpprobe measures what zero-touch provisioning's bootstrap script
// depends on, on the lab CHR, before the bootstrap is written.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// The bootstrap is RouterOS script that runs on a router MikroDash cannot yet
// reach, so a wrong assumption fails silently on somebody's remote site. Each of
// these is measured rather than assumed:
//
//   - z1 how a device identifies itself: routerboard serial-number, and the
//     licence's system-id where there is no routerboard (a CHR);
//   - z2 whether `:rndstr` exists and what it returns (the API password is made
//     on the router);
//   - z3 whether `/tool/fetch … http-method=post output=user as-value` returns
//     the reply body to the script, what a refusal (HTTP 403) does to the
//     script, and whether `:deserialize from=json` reads the reply;
//   - z4 whether a WireGuard interface takes a private key MikroDash made, and
//     reports the public key Go derives from it; and that a script can read an
//     interface's own public key;
//   - z5 whether `/user add address=` really refuses a login from elsewhere;
//   - z6 whether a script can put an input accept rule FIRST;
//   - z7 whether a scheduler can remove itself from its own on-event;
//   - z10 the real thing: a bootstrap from ztp.RemoteScript, /imported, calls
//     home with its identity and a password it made, and MikroDash then signs
//     in through the tunnel with that password;
//   - z8 the premise itself: that RouterOS completes a WireGuard handshake with
//     internal/ztp's userspace engine, through the CHR's NAT, and that traffic
//     flows both ways (the router's fetch in, MikroDash's TCP to the API out).
//
// ── IT REFUSES ANY ROUTER THAT IS NOT A CHR ─────────────────────────────────
//
// It writes. Everything it creates is named `mdprobe-…` and removed on every
// exit path. It must run on the CHR's Docker network, because it also serves
// the HTTP endpoint the CHR's fetch calls.
//
//	docker run --rm --network mikrodash_default -v "$PWD":/src -w /src \
//	  -v mikrodash_data:/data:ro golang:1.27-alpine go run ./cmd/ztpprobe -data /data
package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
	"mikrodash/internal/ztp"
)

const prefix = "mdprobe-"

type step struct {
	Label string            `json:"label"`
	Path  string            `json:"path"`
	Args  []string          `json:"args"`
	Rows  []routeros.Reply  `json:"rows"`
	Done  map[string]string `json:"done"`
	Trap  string            `json:"trap,omitempty"`
	Err   string            `json:"err,omitempty"`
}

type probe struct {
	c        *routeros.Client
	steps    []step
	findings [][2]string
	srvAddr  string

	mu   sync.Mutex
	hits []string
}

func (p *probe) run(label, path string, args ...string) step {
	done := map[string]string{}
	rows, err := p.c.Do(routeros.Cmd{Path: path, Args: args, Timeout: 30 * time.Second, Done: &done})
	s := step{Label: label, Path: path, Args: args, Rows: rows, Done: done}
	if s.Rows == nil {
		s.Rows = []routeros.Reply{}
	}
	var trap *routeros.Trap
	if errors.As(err, &trap) {
		s.Trap = trap.Message
	} else if err != nil {
		s.Err = err.Error()
	}
	p.steps = append(p.steps, s)
	return s
}

func (p *probe) note(name, answer string) {
	p.findings = append(p.findings, [2]string{name, answer})
	fmt.Fprintf(os.Stderr, "  %-34s %s\n", name, answer)
}

// script runs RouterOS script through /execute into a file and returns what it
// printed (`:put`), the pattern importprobe m1b measured.
func (p *probe) script(label, body string) string {
	out := prefix + label
	p.run(label+": execute", "/execute", "=script="+body, "=file="+out)
	for i := 0; i < 15; i++ {
		time.Sleep(time.Second)
		rows, _ := p.c.Do(routeros.Cmd{Path: "/file/print", Timeout: 10 * time.Second,
			Args: []string{"=.proplist=name,contents"}})
		// THE FILE APPEARS BEFORE ITS CONTENTS (measured: a fetch's output was
		// read as empty), so wait for text, and give up only at the deadline.
		for _, r := range rows {
			if strings.HasPrefix(r["name"], out) && strings.TrimSpace(r["contents"]) != "" {
				return strings.TrimSpace(strings.ReplaceAll(r["contents"], "\r\n", "\n"))
			}
		}
	}
	return "(no output within 15s)"
}

func (p *probe) fileID(name string) string {
	rows, _ := p.c.Do(routeros.Cmd{Path: "/file/print", Args: []string{"?name=" + name, "=.proplist=.id"},
		Timeout: 10 * time.Second})
	if len(rows) == 0 {
		return ""
	}
	return rows[0][".id"]
}

func (p *probe) cleanup() {
	type menu struct{ path, key string }
	for _, m := range []menu{
		{"/system/scheduler", "name"}, {"/interface/wireguard", "name"},
		{"/user", "name"}, {"/file", "name"}, {"/ip/firewall/filter", "comment"},
		{"/ip/address", "comment"}, {"/system/script", "name"},
	} {
		rows, err := p.c.Do(routeros.Cmd{Path: m.path + "/print",
			Args: []string{"=.proplist=.id," + m.key}, Timeout: 15 * time.Second})
		if err != nil {
			continue
		}
		for _, r := range rows {
			if strings.HasPrefix(r[m.key], prefix) {
				_, _ = p.c.Do(routeros.Cmd{Path: m.path + "/remove",
					Args: []string{"=.id=" + r[".id"]}, Timeout: 15 * time.Second})
			}
		}
	}
}

// serve is the endpoint the CHR's fetch calls: /ok answers JSON, /deny 403.
func (p *probe) serve() (string, error) {
	l, err := net.Listen("tcp", ":8099")
	if err != nil {
		return "", err
	}
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		p.mu.Lock()
		p.hits = append(p.hits, fmt.Sprintf("%s %s ct=%q body=%s", r.Method, r.URL.Path,
			r.Header.Get("Content-Type"), b))
		p.mu.Unlock()
	}
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"address":"10.249.1.2/32","note":"from probe"}`))
	})
	mux.HandleFunc("/deny", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		http.Error(w, `{"ok":false,"error":"refused"}`, http.StatusForbidden)
	})
	go func() { _ = http.Serve(l, mux) }()
	// The address the CHR reaches us on: our own on the Docker network.
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			return n.IP.String() + ":8099", nil
		}
	}
	return "", errors.New("no address on the Docker network")
}

func main() {
	data := flag.String("data", "", "the /data directory holding the router's credentials")
	rname := flag.String("router", "CHR Test", "the router's label")
	out := flag.String("out", "", "write the recorded sentences here")
	onlyFlag := flag.String("only", "", "comma-separated measurements to run (default: all)")
	flag.Parse()
	cfg, err := fromStore(*data, *rname)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	c, err := routeros.Dial(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer c.Close()
	p := &probe{c: c}

	res, err := c.Do(routeros.Cmd{Path: "/system/resource/print", Timeout: 10 * time.Second})
	if err != nil || len(res) == 0 || !strings.Contains(strings.ToUpper(res[0]["board-name"]+" "+res[0]["platform"]), "CHR") {
		fmt.Fprintln(os.Stderr, "REFUSING: not a CHR, or unreadable. This tool writes.")
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "lab router: board=%q version=%q\n", res[0]["board-name"], res[0]["version"])
	if p.srvAddr, err = p.serve(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "probe endpoint:", p.srvAddr)

	p.cleanup()
	defer p.cleanup()

	only := map[string]bool{}
	for _, m := range strings.Split(*onlyFlag, ",") {
		if m = strings.TrimSpace(m); m != "" {
			only[m] = true
		}
	}
	for _, m := range []struct {
		name string
		fn   func()
	}{
		{"z1", func() { z1Identity(p) }}, {"z2", func() { z2Rndstr(p) }}, {"z3", func() { z3Fetch(p) }},
		{"z4", func() { z4WireGuard(p) }}, {"z5", func() { z5UserAddress(p, cfg) }},
		{"z6", func() { z6FirstRule(p) }}, {"z7", func() { z7SelfRemovingScheduler(p) }},
		{"z8", func() { z8Tunnel(p) }}, {"z9", func() { z9ScriptParts(p) }},
		{"z10", func() { z10Bootstrap(p, cfg) }},
	} {
		if len(only) == 0 || only[m.name] {
			m.fn()
		}
	}

	p.mu.Lock()
	for _, h := range p.hits {
		p.note("endpoint saw", h)
	}
	p.mu.Unlock()
	if *out != "" {
		b, _ := json.MarshalIndent(map[string]any{"router": map[string]string{
			"board": res[0]["board-name"], "version": res[0]["version"]},
			"steps": p.steps, "findings": p.findings}, "", "  ")
		_ = os.MkdirAll(*out, 0o755)
		_ = os.WriteFile(filepath.Join(*out, "ztpprobe.json"), b, 0o644)
	}
}

func z1Identity(p *probe) {
	fmt.Fprintln(os.Stderr, "Z1 identity")
	rb := p.run("z1 routerboard", "/system/routerboard/print")
	lic := p.run("z1 license", "/system/license/print")
	keys := func(s step) string {
		if len(s.Rows) == 0 {
			return fmt.Sprintf("no rows trap=%q", s.Trap)
		}
		var k []string
		for key := range s.Rows[0] {
			k = append(k, key)
		}
		return strings.Join(k, ",")
	}
	p.note("z1 routerboard keys", keys(rb))
	p.note("z1 license keys", keys(lic))
	p.note("z1 script identity", p.script("z1s", `:local s ""; :do { :set s [/system routerboard get serial-number] } on-error={}; `+
		`:if ([:len $s] = 0) do={ :do { :set s [/system license get system-id] } on-error={} }; `+
		`:if ([:len $s] = 0) do={ :do { :set s [/system license get software-id] } on-error={} }; `+
		`:put ("serial=" . $s . " len=" . [:len $s])`))
}

func z2Rndstr(p *probe) {
	fmt.Fprintln(os.Stderr, "Z2 rndstr")
	p.note("z2 rndstr", p.script("z2", `:local a [:rndstr length=24 from="abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"]; `+
		`:local b [:rndstr length=24 from="abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"]; `+
		`:put ("len=" . [:len $a] . " differ=" . ($a != $b))`))
}

func z3Fetch(p *probe) {
	fmt.Fprintln(os.Stderr, "Z3 fetch POST")
	ok := "http://" + p.srvAddr + "/ok"
	deny := "http://" + p.srvAddr + "/deny"
	p.note("z3 fetch ok", p.script("z3a", `:local r [/tool fetch url="`+ok+`" http-method=post `+
		`http-header-field="Content-Type: application/json" http-data="{\"serial\":\"x\",\"n\":1}" output=user as-value]; `+
		`:put ("status=" . ($r->"status") . " data=" . ($r->"data"))`))
	p.note("z3 deserialize", p.script("z3b", `:local r [/tool fetch url="`+ok+`" http-method=post http-data="{}" output=user as-value]; `+
		`:local j [:deserialize from=json value=($r->"data")]; :put ("address=" . ($j->"address") . " ok=" . ($j->"ok"))`))
	p.note("z3 fetch 403", p.script("z3c", `:onerror e in={ :local r [/tool fetch url="`+deny+`" http-method=post http-data="{}" output=user as-value]; `+
		`:put ("no error, status=" . ($r->"status")) } do={ :put ("error=" . $e) }`))
}

func z4WireGuard(p *probe) {
	fmt.Fprintln(os.Stderr, "Z4 wireguard")
	priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
	privB64 := base64.StdEncoding.EncodeToString(priv.Bytes())
	wantPub := base64.StdEncoding.EncodeToString(priv.PublicKey().Bytes())
	add := p.run("z4 add with key", "/interface/wireguard/add", "=name="+prefix+"wg", "=private-key="+privB64, "=listen-port=13299")
	p.note("z4 add with key", fmt.Sprintf("trap=%q err=%q", add.Trap, add.Err))
	rows := p.run("z4 print", "/interface/wireguard/print", "?name="+prefix+"wg", "=.proplist=public-key")
	got := ""
	if len(rows.Rows) > 0 {
		got = rows.Rows[0]["public-key"]
	}
	p.note("z4 public key matches Go", fmt.Sprintf("%v", got == wantPub))
	auto := p.run("z4 add without key", "/interface/wireguard/add", "=name="+prefix+"wg2", "=listen-port=13298")
	p.note("z4 add without key", fmt.Sprintf("trap=%q", auto.Trap))
	p.note("z4 script reads own key", p.script("z4s", `:put ("pub=" . [/interface wireguard get [find name="`+prefix+`wg2"] public-key])`))
	peer := p.run("z4 peer", "/interface/wireguard/peers/add", "=interface="+prefix+"wg", "=public-key="+wantPub,
		"=endpoint-address=198.51.100.1", "=endpoint-port=13231", "=allowed-address=10.249.0.1/32", "=persistent-keepalive=25s")
	p.note("z4 peer add", fmt.Sprintf("trap=%q", peer.Trap))
}

func z5UserAddress(p *probe, cfg routeros.Config) {
	fmt.Fprintln(os.Stderr, "Z5 user address restriction")
	pw := "Probe" + fmt.Sprint(time.Now().UnixNano()%1000000) + "x"
	add := p.run("z5 add restricted", "/user/add", "=name="+prefix+"u", "=group=read", "=password="+pw, "=address=10.249.0.1/32")
	p.note("z5 add", fmt.Sprintf("trap=%q", add.Trap))
	try := func() string {
		c2 := cfg
		c2.Username, c2.Password = prefix+"u", pw
		c, err := routeros.Dial(c2)
		if err != nil {
			return "refused: " + err.Error()
		}
		c.Close()
		return "logged in"
	}
	p.note("z5 login from elsewhere", try())
	// The control: widen it to everywhere, and the same login must work.
	rows := p.run("z5 find", "/user/print", "?name="+prefix+"u", "=.proplist=.id")
	if len(rows.Rows) > 0 {
		p.run("z5 widen", "/user/set", "=.id="+rows.Rows[0][".id"], "=address=0.0.0.0/0")
	}
	p.note("z5 control, unrestricted", try())
}

func z6FirstRule(p *probe) {
	fmt.Fprintln(os.Stderr, "Z6 input accept first")
	before := p.run("z6 before", "/ip/firewall/filter/print", "=.proplist=.id,chain,comment")
	p.note("z6 filter rows before", fmt.Sprint(len(before.Rows)))
	out := p.script("z6", `/ip firewall filter; :local first [:pick [find] 0]; `+
		`:if ([:len $first] > 0) do={ add chain=input action=accept in-interface="`+prefix+`wg" comment="`+prefix+`acc" place-before=$first } `+
		`else={ add chain=input action=accept in-interface="`+prefix+`wg" comment="`+prefix+`acc" }; :put "added"`)
	p.note("z6 script", out)
	after := p.run("z6 after", "/ip/firewall/filter/print", "=.proplist=.id,chain,comment")
	pos := -1
	for i, r := range after.Rows {
		if r["comment"] == prefix+"acc" {
			pos = i
		}
	}
	p.note("z6 position of the new rule", fmt.Sprintf("%d of %d", pos, len(after.Rows)))
}

func z7SelfRemovingScheduler(p *probe) {
	fmt.Fprintln(os.Stderr, "Z7 self-removing scheduler")
	add := p.run("z7 add", "/system/scheduler/add", "=name="+prefix+"sch", "=interval=5s",
		"=on-event=/system scheduler remove [find name=\""+prefix+"sch\"]")
	p.note("z7 add", fmt.Sprintf("trap=%q", add.Trap))
	time.Sleep(12 * time.Second)
	rows := p.run("z7 after 12s", "/system/scheduler/print", "?name="+prefix+"sch", "=.proplist=.id,run-count")
	p.note("z7 still there after 12s", fmt.Sprint(len(rows.Rows) > 0))
}

// cleanupZTP removes everything a bootstrap creates (all carry ztp.Comment, or
// ztp's fixed names), and puts the API services back as they were.
func (p *probe) cleanupZTP(services []routeros.Reply) {
	for _, m := range []string{"/system/scheduler", "/system/script", "/ip/firewall/filter", "/ip/address",
		"/interface/wireguard/peers", "/interface/wireguard", "/user"} {
		rows, _ := p.c.Do(routeros.Cmd{Path: m + "/print", Args: []string{"=.proplist=.id,comment,name"}, Timeout: 15 * time.Second})
		for _, r := range rows {
			if r["comment"] == ztp.Comment || r["name"] == ztp.UserName || r["name"] == ztp.IfaceName ||
				r["name"] == ztp.EnrolIface || r["name"] == ztp.EnrolScript {
				_, _ = p.c.Do(routeros.Cmd{Path: m + "/remove", Args: []string{"=.id=" + r[".id"]}, Timeout: 15 * time.Second})
			}
		}
	}
	for _, r := range services {
		addr := r["address"]
		_, _ = p.c.Do(routeros.Cmd{Path: "/ip/service/set", Timeout: 15 * time.Second,
			Args: []string{"=.id=" + r[".id"], "=disabled=" + r["disabled"], "=address=" + addr}})
	}
}

func z10Bootstrap(p *probe, cfg routeros.Config) {
	fmt.Fprintln(os.Stderr, "Z10 a generated remote bootstrap, end to end")
	svc := p.run("z10 services before", "/ip/service/print", "?name=api", "=.proplist=.id,name,disabled,address,port")
	svc2 := p.run("z10 services before", "/ip/service/print", "?name=api-ssl", "=.proplist=.id,name,disabled,address,port")
	services := append(svc.Rows, svc2.Rows...)
	for _, r := range services {
		p.note("z10 service before", fmt.Sprint(r))
	}
	p.cleanupZTP(nil)
	defer p.cleanupZTP(services)

	sPriv, sPub, _ := ztp.NewKeyPair()
	dPriv, dPub, _ := ztp.NewKeyPair()
	server := netip.MustParseAddr("10.249.0.1")
	dev := netip.MustParseAddr("10.249.1.2")
	eng, err := ztp.Start(ztp.Config{PrivateKey: sPriv, ListenPort: 13231, Address: server})
	if err != nil {
		p.note("z10 engine", err.Error())
		return
	}
	defer eng.Close()
	_ = eng.SetPeer(ztp.Peer{PublicKey: dPub, Allowed: []netip.Prefix{netip.PrefixFrom(dev, 32)}})

	var (
		mu       sync.Mutex
		password string
		enrolled = make(chan struct{}, 1)
	)
	l, err := eng.ListenTCP(80)
	if err != nil {
		p.note("z10 listen", err.Error())
		return
	}
	defer l.Close()
	go func() {
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			_ = json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body)
			mu.Lock()
			if pw, ok := body["password"].(string); ok {
				password = pw
				body["password"] = fmt.Sprintf("<%d chars>", len(pw))
			}
			// The serial identifies the lab CHR; it is not recorded.
			if sn, ok := body["serial"].(string); ok {
				body["serial"] = fmt.Sprintf("<%d chars>", len(sn))
			}
			mu.Unlock()
			p.note("z10 enrol request", fmt.Sprintf("%s %s from %s body=%v", r.Method, r.URL.Path, r.RemoteAddr, body))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"address":"` + dev.String() + `"}`))
			select {
			case enrolled <- struct{}{}:
			default:
			}
		}))
	}()

	host, _, _ := strings.Cut(p.srvAddr, ":")
	script := ztp.RemoteScript(ztp.Instance{ID: "probe-instance", PublicKey: sPub, Endpoint: host, Port: 13231, Server: server},
		ztp.Remote{Label: "lab CHR", Token: "probe-token", PrivateKey: dPriv, Address: dev, Expires: time.Now().Add(time.Hour)})
	p.note("z10 script size", fmt.Sprintf("%d bytes, %d lines", len(script), strings.Count(script, "\n")))
	const file = prefix + "ztp.rsc"
	if id := p.fileID(file); id != "" {
		p.run("z10 stale", "/file/remove", "=.id="+id)
	}
	p.run("z10 file add", "/file/add", "=name="+file, "=type=file")
	id := p.fileID(file)
	set := p.run("z10 file set", "/file/set", "=.id="+id, "=contents="+script)
	if set.Trap != "" {
		p.note("z10 file set", set.Trap)
		return
	}
	imp := p.run("z10 import", "/import", "=file-name="+file)
	p.note("z10 import", fmt.Sprintf("trap=%q done=%v", imp.Trap, imp.Done))

	select {
	case <-enrolled:
	case <-time.After(40 * time.Second):
		p.note("z10 enrolment", "none within 40s")
		lg := p.run("z10 log", "/log/print")
		for i, r := range lg.Rows {
			if i >= len(lg.Rows)-8 {
				p.note("z10 log", r["topics"]+" "+r["message"])
			}
		}
		return
	}
	time.Sleep(3 * time.Second)
	mu.Lock()
	pw := password
	mu.Unlock()
	for _, port := range []int{8728, 8729} {
		c, err := routeros.Dial(routeros.Config{Host: dev.String(), Port: port, TLS: port == 8729, InsecureTLS: true,
			Username: ztp.UserName, Password: pw, DialContext: eng.DialContext})
		if err != nil {
			p.note(fmt.Sprintf("z10 login through the tunnel :%d", port), "failed: "+err.Error())
			continue
		}
		rows, err := c.Do(routeros.Cmd{Path: "/system/resource/print", Args: []string{"=.proplist=board-name,version"}, Timeout: 10 * time.Second})
		c.Close()
		if err != nil || len(rows) == 0 {
			p.note(fmt.Sprintf("z10 read through the tunnel :%d", port), fmt.Sprint(err))
			continue
		}
		p.note(fmt.Sprintf("z10 login through the tunnel :%d", port), "ok: "+rows[0]["board-name"]+" "+rows[0]["version"])
	}
	sc := p.run("z10 enrol script left", "/system/script/print", "?name="+ztp.EnrolScript, "=.proplist=.id")
	sh := p.run("z10 enrol scheduler left", "/system/scheduler/print", "?name="+ztp.EnrolScript, "=.proplist=.id")
	p.note("z10 cleaned up after itself", fmt.Sprintf("script left=%v scheduler left=%v", len(sc.Rows) > 0, len(sh.Rows) > 0))
	// The control for the address limit: the same user and password from
	// MikroDash's own Docker address, outside the tunnel, must be refused.
	c2 := cfg
	c2.Username, c2.Password = ztp.UserName, pw
	if c, err := routeros.Dial(c2); err == nil {
		c.Close()
		p.note("z10 same login outside the tunnel", "LOGGED IN: the address limit did not hold")
	} else {
		p.note("z10 same login outside the tunnel", "refused: "+err.Error())
	}
}

func z9ScriptParts(p *probe) {
	fmt.Fprintln(os.Stderr, "Z9 script parts: serialize, a stored script, self-removal")
	p.note("z9 serialize", p.script("z9a", `:local id "AB12\"x"; :local m {"token"="t0k";"serial"=$id;"n"=3}; `+
		`:put [:serialize to=json value=$m]`))
	// A script stored over the API with a multi-line source, run by a scheduler
	// by name, that removes its scheduler and then itself.
	src := ":global mdprobeRan \"yes\"\n" +
		"/system scheduler remove [find name=\"" + prefix + "sch9\"]\n" +
		"/system script remove [find name=\"" + prefix + "s9\"]"
	add := p.run("z9 script add", "/system/script/add", "=name="+prefix+"s9", "=source="+src)
	p.note("z9 script add", fmt.Sprintf("trap=%q", add.Trap))
	sch := p.run("z9 scheduler", "/system/scheduler/add", "=name="+prefix+"sch9", "=interval=5s", "=on-event="+prefix+"s9")
	p.note("z9 scheduler runs a script by name", fmt.Sprintf("trap=%q", sch.Trap))
	time.Sleep(12 * time.Second)
	s1 := p.run("z9 script left", "/system/script/print", "?name="+prefix+"s9", "=.proplist=.id")
	s2 := p.run("z9 scheduler left", "/system/scheduler/print", "?name="+prefix+"sch9", "=.proplist=.id")
	p.note("z9 after 12s", fmt.Sprintf("script left=%v scheduler left=%v", len(s1.Rows) > 0, len(s2.Rows) > 0))
	p.note("z9 the script ran", p.script("z9b", `:global mdprobeRan; :put ("ran=" . $mdprobeRan)`))
	// And a script added by a line of RouterOS TEXT, its source quoted as
	// cfgtpl.QuoteROS quotes it (newline as \0A, quotes and $ escaped), then run.
	quoted := cfgtpl.QuoteROS(":put \"hi \\$x\"\n:put [:len \"four\"]")
	line := "/system script add name=\"" + prefix + "s9b\" source=" + quoted + "; :put [/system script get [find name=\"" + prefix + "s9b\"] source]; /system script run [find name=\"" + prefix + "s9b\"]"
	p.note("z9 quoted source line", line)
	p.note("z9 quoted source result", p.script("z9c", line))
}

func z8Tunnel(p *probe) {
	fmt.Fprintln(os.Stderr, "Z8 RouterOS against the userspace engine")
	sPriv, sPub, _ := ztp.NewKeyPair()
	rPriv, rPub, _ := ztp.NewKeyPair()
	eng, err := ztp.Start(ztp.Config{PrivateKey: sPriv, ListenPort: 13231, Address: netip.MustParseAddr("10.249.0.1")})
	if err != nil {
		p.note("z8 engine", "did not start: "+err.Error())
		return
	}
	defer eng.Close()
	_ = eng.SetPeer(ztp.Peer{PublicKey: rPub, Allowed: []netip.Prefix{netip.MustParsePrefix("10.249.1.2/32")}})
	l, err := eng.ListenTCP(80)
	if err != nil {
		p.note("z8 listen", err.Error())
		return
	}
	defer l.Close()
	go func() {
		_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			p.hits = append(p.hits, "via tunnel from "+r.RemoteAddr)
			p.mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
	}()
	host, _, _ := strings.Cut(p.srvAddr, ":")
	for _, s := range []struct {
		path string
		args []string
	}{
		// MEASURED: a listen port another WireGuard interface already holds makes
		// RouterOS create the interface DISABLED, with `.about: Listen port
		// already used`, and it sends nothing. So no port is given here, to see
		// what RouterOS picks for itself.
		{"/interface/wireguard/add", []string{"=name=" + prefix + "ztp", "=private-key=" + rPriv}},
		{"/interface/wireguard/peers/add", []string{"=interface=" + prefix + "ztp", "=public-key=" + sPub,
			"=endpoint-address=" + host, "=endpoint-port=13231", "=allowed-address=10.249.0.1/32", "=persistent-keepalive=5s"}},
		{"/ip/address/add", []string{"=address=10.249.1.2/16", "=interface=" + prefix + "ztp", "=comment=" + prefix + "ztp"}},
	} {
		st := p.run("z8 "+s.path, s.path, s.args...)
		if st.Trap != "" || st.Err != "" {
			p.note("z8 "+s.path, fmt.Sprintf("trap=%q err=%q", st.Trap, st.Err))
			return
		}
	}
	// Let the input chain admit the tunnel, first, as the bootstrap will.
	p.script("z8fw", `/ip firewall filter; :local first [:pick [find] 0]; `+
		`:if ([:len $first] > 0) do={ add chain=input action=accept in-interface="`+prefix+`ztp" comment="`+prefix+`ztp" place-before=$first } `+
		`else={ add chain=input action=accept in-interface="`+prefix+`ztp" comment="`+prefix+`ztp" }; :put ok`)

	// IN: the router calls home over the tunnel.
	p.note("z8 router calls home", p.script("z8a", `:onerror e in={ :local r [/tool fetch url="http://10.249.0.1/enrol" `+
		`http-method=post http-data="{}" output=user as-value]; :put ("status=" . ($r->"status") . " data=" . ($r->"data")) } `+
		`do={ :put ("error=" . $e) }`))
	rp := p.run("z8 router peer", "/interface/wireguard/peers/print", "?interface="+prefix+"ztp")
	for _, r := range rp.Rows {
		p.note("z8 router's peer", fmt.Sprintf("tx=%s rx=%s last-handshake=%q current-endpoint=%s:%s running=%s",
			r["tx"], r["rx"], r["last-handshake"], r["current-endpoint-address"], r["current-endpoint-port"], r["running"]))
	}
	wi := p.run("z8 router iface", "/interface/wireguard/print", "?name="+prefix+"ztp")
	for _, r := range wi.Rows {
		delete(r, "private-key")
		p.note("z8 router's interface", fmt.Sprint(r))
	}
	lg := p.run("z8 log", "/log/print", "?topics=wireguard")
	for i, r := range lg.Rows {
		if i >= len(lg.Rows)-6 {
			p.note("z8 log", r["time"]+" "+r["topics"]+" "+r["message"])
		}
	}
	all := p.run("z8 all wg", "/interface/wireguard/print", "=.proplist=name,listen-port,disabled,running")
	for _, r := range all.Rows {
		p.note("z8 wg on router", fmt.Sprint(r))
	}
	st, _ := eng.Status()
	for _, s := range st {
		p.note("z8 engine sees peer", fmt.Sprintf("endpoint=%s handshake=%v rx=%d tx=%d", s.Endpoint,
			!s.LastHandshake.IsZero(), s.RxBytes, s.TxBytes))
	}
	// OUT: MikroDash reaches the router's API port through the tunnel.
	for _, port := range []string{"8728", "8729"} {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		c, err := eng.DialContext(ctx, "tcp", "10.249.1.2:"+port)
		cancel()
		if err != nil {
			p.note("z8 TCP to API "+port, "failed: "+err.Error())
			continue
		}
		c.Close()
		p.note("z8 TCP to API "+port, "connected")
	}
}

// fromStore is importprobe's, deliberately a copy: each lab tool is one file.
func fromStore(dir, name string) (routeros.Config, error) {
	st, err := store.Open(dir)
	if err != nil {
		return routeros.Config{}, fmt.Errorf("open %s: %w", dir, err)
	}
	routers, errs := st.Routers()
	for _, e := range errs {
		fmt.Fprintln(os.Stderr, "warning:", e)
	}
	for _, r := range routers {
		if !strings.EqualFold(r.Label, name) && r.Host != name {
			continue
		}
		pw, err := st.Decrypt(r.Encrypted)
		if err != nil {
			return routeros.Config{}, fmt.Errorf("decrypt %s: %w", r.Label, err)
		}
		return routeros.Config{Host: r.Host, Port: r.Port, Username: r.Username, Password: pw,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure}, nil
	}
	return routeros.Config{}, fmt.Errorf("no router in %s is called %q", dir, name)
}
