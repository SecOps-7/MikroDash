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
//   - z7 whether a scheduler can remove itself from its own on-event.
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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
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

func (p *probe) cleanup() {
	type menu struct{ path, key string }
	for _, m := range []menu{
		{"/system/scheduler", "name"}, {"/interface/wireguard", "name"},
		{"/user", "name"}, {"/file", "name"}, {"/ip/firewall/filter", "comment"},
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
