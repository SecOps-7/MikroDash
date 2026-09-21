// Command importprobe measures what RouterOS's `/import` actually does over the
// binary API, on the lab CHR, before any code that deploys configuration is
// written.
//
// ── WHY THIS EXISTS ─────────────────────────────────────────────────────────
//
// Config Management will be the first thing in MikroDash that executes a
// multi-line RouterOS file. Several things its design depends on cannot be
// settled from the documentation, and two of them the documentation contradicts
// itself on:
//
//   - whether `/import … dry-run=yes` returns ANY output over the API.
//     internal/backups/read.go records that `/export` over the API "returns an
//     empty array — it runs, and the text never comes back", measured on a hAP
//     AX3. /import may do the same, and then "show the operator the dry-run" is
//     not a feature that can exist as designed;
//   - what a runtime error does to the rest of the file, with `verbose=yes` and
//     without. MikroTik's page says both that verbose "executes each line
//     individually" and that it "will also stop the import process on a
//     problem";
//   - how large a `/file/set contents=` may be over the API (the documented
//     60 KB is the limit on READING contents, not on writing them);
//   - whether a dry-run sees an object created earlier in the same file;
//   - whether `/import` returns before it has finished, as `/export` does;
//   - whether cancelling the API tag stops an import already running;
//   - whether a one-shot `/system/scheduler` entry can serve as a dead-man
//     revert, and when it first fires;
//   - whether `/user/active` carries the address MikroDash arrives from;
//   - how long after an import a firewall rule it re-adds is in force.
//
// Each answer decides a branch of the design, so each is MEASURED here and the
// raw sentences are kept, rather than assumed and discovered wrong in
// production.
//
// ── IT REFUSES ANY ROUTER THAT IS NOT A CHR ─────────────────────────────────
//
// This tool writes: it creates files, DNS static entries, an interface list, a
// scheduler entry and a filter rule, and imports files. It is meant for the disposable lab CHR
// and nothing else. A label is only a name in a store, so the check that counts
// is what the router says it IS: `/system/resource` must report a CHR board
// before the first write. Everything it creates is named `mdprobe-…` and removed
// on every exit path.
//
//	go run ./cmd/importprobe -data /data -router "CHR Test" -out testdata/fixtures/import
package main

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
	"mikrodash/internal/store"
)

// prefix names everything this tool creates, so cleanup can find it and nothing
// it removes can belong to anyone else.
const prefix = "mdprobe-"

// step is one command and everything that came back, kept verbatim. This is the
// fixture: the parser for /import's answer is written against these, never
// against a guess about their shape.
type step struct {
	Label string            `json:"label"`
	Path  string            `json:"path"`
	Args  []string          `json:"args"`
	Rows  []routeros.Reply  `json:"rows"`
	Done  map[string]string `json:"done"`
	Trap  string            `json:"trap,omitempty"`
	Cat   string            `json:"trapCategory,omitempty"`
	Err   string            `json:"err,omitempty"`
	Ms    int64             `json:"ms"`
}

type finding struct {
	Name   string `json:"name"`
	Answer string `json:"answer"`
}

type probe struct {
	c        *routeros.Client
	steps    []step
	findings []finding
}

// argsForRecord keeps the file body out of the recorded args: a `contents=`
// word is replaced by its length, so a fixture holds what was asked, not a copy
// of the padding.
func argsForRecord(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if strings.HasPrefix(a, "=contents=") {
			out[i] = fmt.Sprintf("=contents=<%d bytes>", len(a)-len("=contents="))
			continue
		}
		out[i] = a
	}
	return out
}

func (p *probe) run(label, path string, timeout time.Duration, args ...string) step {
	done := map[string]string{}
	t0 := time.Now()
	rows, err := p.c.Do(routeros.Cmd{Path: path, Args: args, Timeout: timeout, Done: &done})
	s := step{Label: label, Path: path, Args: argsForRecord(args), Rows: rows, Done: done,
		Ms: time.Since(t0).Milliseconds()}
	if s.Rows == nil {
		s.Rows = []routeros.Reply{}
	}
	var trap *routeros.Trap
	if errors.As(err, &trap) {
		s.Trap, s.Cat = trap.Message, trap.Category
	} else if err != nil {
		s.Err = err.Error()
	}
	p.steps = append(p.steps, s)
	return s
}

func (p *probe) note(name, answer string) {
	p.findings = append(p.findings, finding{name, answer})
	fmt.Fprintf(os.Stderr, "  %-40s %s\n", name, answer)
}

// ── small router operations, all by .id, never by `numbers` ─────────────────
//
// `=numbers=` is the console's spelling and traps over the binary API with
// "unknown parameter numbers" (measured during the WireGuard work).

func (p *probe) fileID(name string) string {
	rows, _ := p.c.Do(routeros.Cmd{Path: "/file/print", Args: []string{"?name=" + name},
		Timeout: 10 * time.Second})
	if len(rows) == 0 {
		return ""
	}
	return rows[0][".id"]
}

func (p *probe) fileSize(name string) string {
	rows, _ := p.c.Do(routeros.Cmd{Path: "/file/print", Args: []string{"?name=" + name},
		Timeout: 10 * time.Second})
	if len(rows) == 0 {
		return "(absent)"
	}
	return rows[0]["size"]
}

// writeFile creates the file and sets its contents, returning what the router
// said at each step so a refusal is recorded rather than swallowed.
func (p *probe) writeFile(label, name, body string) bool {
	// NEVER SEND WHAT DROPS THE CONNECTION. 62464 bytes traps cleanly and M3
	// sends it on purpose; 102400 bytes closes the API connection with no trap.
	// Anything past what M3 needs is refused here, before it reaches the wire.
	if len(body) > 63*1024 {
		p.note(label, fmt.Sprintf("REFUSED to send %d bytes: past the size that drops the connection", len(body)))
		return false
	}
	if id := p.fileID(name); id != "" {
		p.run(label+": remove stale", "/file/remove", 10*time.Second, "=.id="+id)
	}
	add := p.run(label+": add", "/file/add", 15*time.Second, "=name="+name, "=type=file")
	if add.Trap != "" || add.Err != "" {
		// Retry without `type`, in case this build does not take it.
		add = p.run(label+": add (no type)", "/file/add", 15*time.Second, "=name="+name)
		if add.Trap != "" || add.Err != "" {
			return false
		}
	}
	id := p.fileID(name)
	if id == "" {
		p.note(label, "file did not appear after /file/add")
		return false
	}
	set := p.run(label+": set contents", "/file/set", 30*time.Second, "=.id="+id, "=contents="+body)
	return set.Trap == "" && set.Err == ""
}

func (p *probe) dnsCount() int {
	rows, err := p.c.Do(routeros.Cmd{Path: "/ip/dns/static/print",
		Args: []string{"=.proplist=.id,name"}, Timeout: 10 * time.Second})
	if err != nil {
		return -1
	}
	n := 0
	for _, r := range rows {
		if strings.HasPrefix(r["name"], prefix) {
			n++
		}
	}
	return n
}

// cleanup removes every mdprobe-* object this tool can create. It runs at start
// (a previous run that died) and on exit.
func (p *probe) cleanup() {
	type menu struct{ path, key string }
	for _, m := range []menu{
		{"/system/scheduler", "name"},
		{"/ip/dns/static", "name"},
		{"/interface/list/member", "list"},
		{"/interface/list", "name"},
		{"/file", "name"},
		{"/ip/firewall/filter", "comment"},
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

func dnsLines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		fmt.Fprintf(&b, "/ip/dns/static add name=%s%d.test address=192.0.2.%d\n", prefix, i, i%250+1)
	}
	return b.String()
}

func main() {
	var (
		data  = flag.String("data", "", "read the router's credentials from this /data directory")
		rname = flag.String("router", "CHR Test", "the router's label in that store")
		out   = flag.String("out", "", "write the recorded sentences here as JSON fixtures")
		only  = flag.String("only", "", "comma-separated measurements to run (default: all)")
		destr = flag.Bool("destructive", false, "allow m8, which WIPES AND REBOOTS the router")
	)
	flag.Parse()
	if *data == "" {
		fmt.Fprintln(os.Stderr, "usage: importprobe -data /data [-router LABEL] [-out DIR] [-only m1,m3,...]")
		os.Exit(2)
	}
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

	// ── THE GATE: the router must say it is a CHR, before any write ─────────
	res, err := c.Do(routeros.Cmd{Path: "/system/resource/print", Timeout: 10 * time.Second})
	if err != nil || len(res) == 0 {
		fmt.Fprintln(os.Stderr, "could not read /system/resource:", err)
		os.Exit(1)
	}
	board, ver := res[0]["board-name"], res[0]["version"]
	if !strings.Contains(strings.ToUpper(board+" "+res[0]["platform"]), "CHR") {
		fmt.Fprintf(os.Stderr, "REFUSING: %q reports board %q, not a CHR. This tool writes, "+
			"and is for the disposable lab router only.\n", *rname, board)
		os.Exit(3)
	}
	fmt.Fprintf(os.Stderr, "lab router: board=%q version=%q\n", board, ver)

	want := map[string]bool{}
	for _, m := range strings.Split(*only, ",") {
		if m = strings.TrimSpace(m); m != "" {
			want[m] = true
		}
	}
	runs := func(m string) bool { return len(want) == 0 || want[m] }

	p.cleanup()
	defer p.cleanup()

	if runs("m9") {
		m9UserActive(p)
	}
	if runs("m10") {
		m10RemoveByName(p)
	}
	if runs("m13") {
		m13CannedDryRun(p)
	}
	if runs("m14") {
		m14FirewallSettle(p, cfg)
	}
	if runs("m3") {
		m3FileCeiling(p)
	}
	if runs("m1") {
		m1DryRunOutput(p)
	}
	if runs("m1b") {
		m1bExecuteCapture(p)
	}
	if runs("m2") {
		m2RuntimeError(p)
	}
	if runs("m4") {
		m4DryRunDependency(p)
	}
	if runs("m5") {
		m5ReturnsWhenDone(p)
	}
	if runs("m6") {
		m6Cancel(p)
	}
	if runs("m7") {
		m7DeadMan(p)
	}
	// M8 WIPES AND REBOOTS THE ROUTER, so it never runs as part of "all": it
	// needs to be named, and -destructive given as well.
	if want["m8"] {
		if !*destr {
			fmt.Fprintln(os.Stderr, "m8 resets the router: add -destructive to run it")
			os.Exit(2)
		}
		m8ExportReset(p, cfg)
	}
	// M11 REBOOTS THE ROUTER too, by design: it is the revert under test.
	if want["m11"] {
		if !*destr {
			fmt.Fprintln(os.Stderr, "m11 reboots the router: add -destructive to run it")
			os.Exit(2)
		}
		m11BackupDeadMan(p, cfg)
	}
	if want["m12"] {
		if !*destr {
			fmt.Fprintln(os.Stderr, "m12 resets the router: add -destructive to run it")
			os.Exit(2)
		}
		m12Bootstrap(p, cfg)
	}

	report := struct {
		RouterOS string    `json:"routerOS"`
		Findings []finding `json:"findings"`
		Steps    []step    `json:"steps"`
	}{ver, p.findings, p.steps}
	enc, _ := json.MarshalIndent(report, "", "  ")
	if *out != "" {
		if err := os.MkdirAll(*out, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		// "7.24.4 (stable)" → "7.24.4": a fixture name holds no space or bracket.
		short := ver
		if f := strings.Fields(ver); len(f) > 0 {
			short = f[0]
		}
		// A PARTIAL RUN GETS ITS OWN FILE. With one name for every run, `-only m8`
		// would overwrite the full fixture with the one measurement it made.
		stem := "probe-" + short
		if len(want) > 0 {
			ms := make([]string, 0, len(want))
			for m := range want {
				ms = append(ms, m)
			}
			sort.Strings(ms)
			stem += "-" + strings.Join(ms, "-")
		}
		name := filepath.Join(*out, stem+".json")
		if err := os.WriteFile(name, append(enc, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "wrote", name)
	} else {
		fmt.Println(string(enc))
	}
}

// M10: does `/file/remove =numbers=<name>` remove a file over the API?
// backups.Sweep removes by name that way, and Config Management's sweep shares
// it; this tool's own helpers use `.id` because `numbers` trapped elsewhere.
func m10RemoveByName(p *probe) {
	fmt.Fprintln(os.Stderr, "M10 /file/remove by numbers=<name>")
	const name = prefix + "numbers.rsc"
	if !p.writeFile("m10", name, "# m10\n") {
		return
	}
	s := p.run("m10 remove by numbers=name", "/file/remove", 10*time.Second, "=numbers="+name)
	p.note("m10 trap", fmt.Sprintf("%q err=%q", s.Trap, s.Err))
	p.note("m10 file after", p.fileSize(name))
}

// M14: how long after an import does a filter rule it removed and re-added
// take effect? A deploy proves it did not lock MikroDash out with a fresh
// login straight after the import, so a window in which the old rule set is
// still in force would make that proof worthless.
//
// The rule drops only NEW connections to the API ports from this probe's own
// address, so the probe's session survives on a router with no "accept
// established" rule. The address reaches the router only inside the file,
// which is recorded by its length.
func m14FirewallSettle(p *probe, cfg routeros.Config) {
	fmt.Fprintln(os.Stderr, "M14 firewall settle after an import")
	u, err := net.Dial("udp", net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port)))
	if err != nil {
		p.note("m14", "ABORTED: no local address: "+err.Error())
		return
	}
	me := u.LocalAddr().(*net.UDPAddr).IP.String()
	_ = u.Close()
	const (
		name    = prefix + "settle.rsc"
		comment = prefix + "settle"
	)
	rule := "add chain=input action=drop protocol=tcp dst-port=8728,8729 connection-state=new src-address=" +
		me + " comment=" + comment + "\n"
	fresh := cfg
	fresh.DialTimeout = 3 * time.Second
	fresh.Label = "m14 fresh login"
	login := func() bool {
		c, err := routeros.Dial(fresh)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	// A plain add first: the rule the samples replace.
	if !p.writeFile("m14 seed", name, "/ip firewall filter\n"+rule) {
		return
	}
	p.run("m14 seed import", "/import", 30*time.Second, "=file-name="+name, "=verbose=no")
	time.Sleep(3 * time.Second)
	p.note("m14 control: a login 3 s after a plain add", fmt.Sprintf("got in=%v", login()))
	body := "/ip firewall filter\nremove [ find comment=" + comment + " ]\n" + rule
	for _, d := range []time.Duration{0, 0, 300 * time.Millisecond, 700 * time.Millisecond, time.Second,
		2 * time.Second, 5 * time.Second} {
		if !p.writeFile("m14", name, body) {
			return
		}
		s := p.run("m14 remove and re-add", "/import", 30*time.Second, "=file-name="+name, "=verbose=no")
		if s.Trap != "" || s.Err != "" {
			p.note("m14", "ABORTED: the import failed")
			return
		}
		time.Sleep(d)
		p.note(fmt.Sprintf("m14 login %v after remove+add", d), fmt.Sprintf("got in=%v", login()))
		time.Sleep(2 * time.Second)
	}
}

// M9: does /user/active carry the address MikroDash arrives from?
func m9UserActive(p *probe) {
	fmt.Fprintln(os.Stderr, "M9 /user/active")
	s := p.run("m9: /user/active", "/user/active/print", 10*time.Second)
	keys := map[string]bool{}
	for _, r := range s.Rows {
		for k := range r {
			keys[k] = true
		}
	}
	ks := make([]string, 0, len(keys))
	for k := range keys {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	p.note("m9 /user/active fields", strings.Join(ks, ","))
	p.note("m9 carries address", fmt.Sprint(keys["address"]))
	// The recorded rows hold a real address and user; keep only the shape.
	for i := range p.steps {
		if p.steps[i].Label == "m9: /user/active" {
			for _, r := range p.steps[i].Rows {
				for k := range r {
					r[k] = "<" + k + ">"
				}
			}
		}
	}
}

// M3: the /file/set contents ceiling over the API.
//
// ── IT STOPS AT 61 KiB, AND THE REASON IS A FINDING ─────────────────────────
//
// Measured on 7.24.4: 60416 bytes is accepted; 62464 bytes is refused with a
// clean trap, "failure: contents too long", and the connection survives. But
// 102400 bytes gets NO trap — the router closes the API connection outright
// (EOF), and every command after it fails. In MikroDash that connection is the
// shared session every collector uses for the router, so an oversized write
// would blind the whole dashboard for it. The cap therefore has to be enforced
// in Go before anything is sent, well inside the measured limit.
//
// The larger sizes are not re-run here, because doing so takes the rest of the
// probe down with them. The evidence is kept in the committed fixture.
func m3FileCeiling(p *probe) {
	fmt.Fprintln(os.Stderr, "M3 /file/set contents ceiling")
	for _, kib := range []int{30, 59, 60, 61} {
		name := fmt.Sprintf("%sceil-%d.rsc", prefix, kib)
		// Valid, inert RouterOS text: comment lines, padded to size.
		var b strings.Builder
		for b.Len() < kib*1024 {
			b.WriteString("# mdprobe padding line to measure the contents ceiling ........\n")
		}
		body := b.String()[:kib*1024]
		ok := p.writeFile(fmt.Sprintf("m3 %dKiB", kib), name, body)
		got := p.fileSize(name)
		p.note(fmt.Sprintf("m3 set %d KiB (%d bytes)", kib, len(body)),
			fmt.Sprintf("accepted=%v stored-size=%s", ok, got))
	}
}

// M1: does a dry-run return anything over the API?
func m1DryRunOutput(p *probe) {
	fmt.Fprintln(os.Stderr, "M1 dry-run output")
	const name = prefix + "valid.rsc"
	if !p.writeFile("m1", name, dnsLines(1, 3)) {
		p.note("m1", "could not write the file")
		return
	}
	for _, v := range [][]string{
		{"=dry-run="},
		{"=dry-run=yes"},
	} {
		args := append([]string{"=file-name=" + name, "=verbose=yes"}, v...)
		s := p.run("m1 dry-run "+v[0], "/import", 60*time.Second, args...)
		p.note("m1 dry-run "+v[0],
			fmt.Sprintf("rows=%d done=%v trap=%q err=%q", len(s.Rows), s.Done, s.Trap, s.Err))
	}
	p.note("m1 dry-run applied anything", fmt.Sprintf("mdprobe entries after dry-run = %d", p.dnsCount()))
}

// M1b: M1 found that a dry-run returns NO text over the API — only `ret:true`,
// or for a syntax error a trap reading "found N error(s)" with no line. Can the
// console's own words be had by running the import under `/execute` with its
// output sent to a file, then reading that file back?
//
// THE FLAG IS BARE HERE, and that is a finding. Over the API `=dry-run=yes` is
// accepted; in the CONSOLE syntax `/execute` runs, `dry-run` takes no value and
// `dry-run=yes` is "expected end of command". The first run of this measured
// exactly that and nothing else.
func m1bExecuteCapture(p *probe) {
	fmt.Fprintln(os.Stderr, "M1b capture a dry-run's console output through /execute")
	p.cleanup()
	const syn = prefix + "synerr.rsc"
	const outFile = prefix + "out"
	sbody := dnsLines(1, 2) + "/ip/dns/static add name=\"" + prefix + "unterminated\n" + dnsLines(4, 5)
	if !p.writeFile("m1b", syn, sbody) {
		return
	}
	s := p.run("m1b execute", "/execute", 20*time.Second,
		"=script=/import file-name="+syn+" verbose=yes dry-run", "=file="+outFile)
	p.note("m1b /execute", fmt.Sprintf("rows=%d done=%v trap=%q err=%q", len(s.Rows), s.Done, s.Trap, s.Err))
	// /execute runs as a job; give it a moment, then look for its output file.
	var got string
	for i := 0; i < 10; i++ {
		time.Sleep(time.Second)
		rows, _ := p.c.Do(routeros.Cmd{Path: "/file/print", Timeout: 10 * time.Second,
			Args: []string{"=.proplist=name,size,contents"}})
		for _, r := range rows {
			if strings.HasPrefix(r["name"], outFile) {
				got = fmt.Sprintf("file=%q size=%s contents=%q", r["name"], r["size"], r["contents"])
			}
		}
		if got != "" {
			break
		}
	}
	if got == "" {
		got = "(no output file appeared)"
	}
	p.note("m1b captured output", got)
}

// M2: what a runtime error on line 5 of 10 does, with and without verbose.
func m2RuntimeError(p *probe) {
	fmt.Fprintln(os.Stderr, "M2 runtime error on line 5")
	const name = prefix + "rterr.rsc"
	body := dnsLines(1, 4) +
		"/interface/list/member add list=" + prefix + "no-such-list interface=ether1\n" +
		dnsLines(6, 10)
	for _, mode := range []struct {
		label string
		args  []string
	}{
		{"verbose=no", nil},
		{"verbose=yes", []string{"=verbose=yes"}},
		{"dry-run", []string{"=verbose=yes", "=dry-run="}},
	} {
		p.cleanup()
		if !p.writeFile("m2 "+mode.label, name, body) {
			p.note("m2 "+mode.label, "could not write the file")
			continue
		}
		args := append([]string{"=file-name=" + name}, mode.args...)
		s := p.run("m2 import "+mode.label, "/import", 60*time.Second, args...)
		time.Sleep(time.Second)
		p.note("m2 "+mode.label,
			fmt.Sprintf("lines applied=%d of 9 dns adds; rows=%d done=%v trap=%q",
				p.dnsCount(), len(s.Rows), s.Done, s.Trap))
	}

	// A SYNTAX error, which is what dry-run claims to catch.
	p.cleanup()
	const syn = prefix + "synerr.rsc"
	sbody := dnsLines(1, 2) + "/ip/dns/static add name=\"" + prefix + "unterminated\n" + dnsLines(4, 5)
	for _, mode := range []struct {
		label string
		args  []string
	}{
		{"dry-run", []string{"=verbose=yes", "=dry-run="}},
		{"verbose=no", nil},
	} {
		p.cleanup()
		if !p.writeFile("m2 syntax "+mode.label, syn, sbody) {
			continue
		}
		args := append([]string{"=file-name=" + syn}, mode.args...)
		s := p.run("m2 syntax "+mode.label, "/import", 60*time.Second, args...)
		time.Sleep(time.Second)
		p.note("m2 syntax "+mode.label,
			fmt.Sprintf("applied=%d; rows=%d done=%v trap=%q", p.dnsCount(), len(s.Rows), s.Done, s.Trap))
	}

	// A file that does not exist.
	s := p.run("m2 missing file", "/import", 30*time.Second, "=file-name="+prefix+"nope.rsc")
	p.note("m2 missing file", fmt.Sprintf("trap=%q err=%q", s.Trap, s.Err))
}

// M4: does a dry-run see an object created earlier in the same file?
func m4DryRunDependency(p *probe) {
	fmt.Fprintln(os.Stderr, "M4 dry-run dependency within one file")
	p.cleanup()
	const name = prefix + "dep.rsc"
	body := "/interface/list add name=" + prefix + "list\n" +
		"/interface/list/member add list=" + prefix + "list interface=ether1\n"
	if !p.writeFile("m4", name, body) {
		return
	}
	s := p.run("m4 dry-run", "/import", 60*time.Second, "=file-name="+name, "=verbose=yes", "=dry-run=")
	p.note("m4 dry-run of add-then-use",
		fmt.Sprintf("rows=%d done=%v trap=%q", len(s.Rows), s.Done, s.Trap))
}

// M5: does /import return before it has finished applying?
func m5ReturnsWhenDone(p *probe) {
	fmt.Fprintln(os.Stderr, "M5 does /import return when done")
	p.cleanup()
	const name = prefix + "big.rsc"
	const n = 600
	if !p.writeFile("m5", name, dnsLines(1, n)) {
		return
	}
	s := p.run("m5 import 600 lines", "/import", 5*time.Minute, "=file-name="+name)
	at := p.dnsCount()
	time.Sleep(3 * time.Second)
	later := p.dnsCount()
	p.note("m5 returned after", fmt.Sprintf("%d ms; entries at return=%d, 3s later=%d (of %d); trap=%q",
		s.Ms, at, later, n, s.Trap))
}

// M6: does cancelling the API tag stop an import in flight?
func m6Cancel(p *probe) {
	fmt.Fprintln(os.Stderr, "M6 cancel an import in flight")
	p.cleanup()
	const name = prefix + "huge.rsc"
	// 950 lines is ~57 KB: as large as a file may be (M3) while still slow
	// enough to cancel mid-run — 600 lines took 266 ms (M5). The first version
	// of this used 3000 lines, which is ~159 KB, hit the connection-dropping
	// ceiling M3 had just found, and took M7 down with it.
	const n = 950
	body := dnsLines(1, n)
	if len(body) >= 60000 {
		p.note("m6", fmt.Sprintf("REFUSED to send %d bytes: over the measured ceiling", len(body)))
		return
	}
	if !p.writeFile("m6", name, body) {
		return
	}
	s := p.run("m6 import with 80ms timeout", "/import", 80*time.Millisecond, "=file-name="+name)
	var series []int
	for i := 0; i < 8; i++ {
		series = append(series, p.dnsCount())
		time.Sleep(time.Second)
	}
	p.note("m6 after cancel", fmt.Sprintf("err=%q; entries each second=%v (of %d)", s.Err, series, n))
}

// M7: can a one-shot scheduler entry act as a dead-man revert, and when does it
// first fire?
func m7DeadMan(p *probe) {
	fmt.Fprintln(os.Stderr, "M7 dead-man scheduler")
	p.cleanup()
	const undo = prefix + "undo.rsc"
	marker := prefix + "marker.test"
	if !p.writeFile("m7 undo", undo,
		"/ip/dns/static remove [find name=\""+marker+"\"]\n") {
		return
	}
	p.run("m7 marker", "/ip/dns/static/add", 10*time.Second,
		"=name="+marker, "=address=192.0.2.200")

	// Who are we, and may our group create a scheduler that imports?
	p.run("m7 our group", "/user/print", 10*time.Second, "=.proplist=name,group")

	const every = 15 * time.Second
	add := p.run("m7 add scheduler", "/system/scheduler/add", 10*time.Second,
		"=name="+prefix+"deadman", "=interval=15s",
		"=on-event=/import file-name="+undo)
	if add.Trap != "" || add.Err != "" {
		p.note("m7 scheduler add", fmt.Sprintf("REFUSED trap=%q err=%q", add.Trap, add.Err))
		return
	}
	t0 := time.Now()
	firstRun, markerGone := time.Duration(-1), time.Duration(-1)
	for time.Since(t0) < 2*every+10*time.Second {
		rows, _ := p.c.Do(routeros.Cmd{Path: "/system/scheduler/print",
			Args:    []string{"?name=" + prefix + "deadman", "=.proplist=run-count,next-run,start-time"},
			Timeout: 10 * time.Second})
		if firstRun < 0 && len(rows) > 0 && rows[0]["run-count"] != "" && rows[0]["run-count"] != "0" {
			firstRun = time.Since(t0)
		}
		mk, _ := p.c.Do(routeros.Cmd{Path: "/ip/dns/static/print",
			Args: []string{"?name=" + marker}, Timeout: 10 * time.Second})
		if markerGone < 0 && len(mk) == 0 {
			markerGone = time.Since(t0)
		}
		if firstRun >= 0 && markerGone >= 0 {
			break
		}
		time.Sleep(time.Second)
	}
	p.run("m7 scheduler row", "/system/scheduler/print", 10*time.Second, "?name="+prefix+"deadman")
	p.note("m7 first run after", fmt.Sprint(firstRun.Round(time.Second)))
	p.note("m7 undo removed the marker after", fmt.Sprint(markerGone.Round(time.Second)))
}

// M8: export + reset, the portable full-replacement route.
//
// ── WHAT IT MEASURES ─────────────────────────────────────────────────────────
//
//   - whether `keep-users=yes` keeps MikroDash's login, password and all. If it
//     does, the run-after-reset script never needs a credential written into it
//     — which the design would otherwise have to do, on the router's flash;
//   - whether the script runs, and whether it can DELETE ITSELF ON ITS FIRST
//     LINE. M2 showed a file is parsed whole before any of it runs, so removing
//     the file first should cost nothing and leaves nothing on disk however the
//     rest goes;
//   - whether a self-signed certificate can be made and signed inside the
//     script, so `api-ssl` answers again. A reset destroys certificates and
//     `/export` never carries them;
//   - how long the router is gone;
//   - where the script's outcome can be read afterwards, since no API session
//     is open to receive a trap while it runs.
//
// ── IT PUTS THE LAB BACK AS IT FOUND IT ──────────────────────────────────────
//
// A binary backup is taken first and loaded afterwards, which restores the
// original certificate and everything else, and reboots once more. If the
// router does not come back over api-ssl, a plain-API dial is tried so the
// restore can still be issued; if neither answers, the serial console on
// 127.0.0.1:15000 is the way in, and this says so.
func m8ExportReset(p *probe, cfg routeros.Config) {
	fmt.Fprintln(os.Stderr, "M8 export + reset (DESTRUCTIVE: wipes and reboots the router)")
	p.cleanup()
	const backup = prefix + "prereset"
	const script = prefix + "reset.rsc"
	const cert = prefix + "api"
	marker := prefix + "reset-ran.test"

	// What the router looks like before, so the restore can be checked.
	before := func(label string) string {
		rows, _ := p.c.Do(routeros.Cmd{Path: "/ip/service/print", Timeout: 10 * time.Second,
			Args: []string{"?name=api-ssl", "=.proplist=name,certificate,disabled,port"}})
		if len(rows) == 0 {
			return "(no api-ssl row)"
		}
		return fmt.Sprintf("api-ssl certificate=%q disabled=%s port=%s",
			rows[0]["certificate"], rows[0]["disabled"], rows[0]["port"])
	}
	origCert := before("before")
	p.note("m8 before", origCert)

	// 1. The way back.
	bk := p.run("m8 backup save", "/system/backup/save", 60*time.Second,
		"=name="+backup, "=dont-encrypt=yes")
	if bk.Trap != "" || bk.Err != "" {
		p.note("m8", fmt.Sprintf("ABORTED: could not take the pre-reset backup: trap=%q err=%q", bk.Trap, bk.Err))
		return
	}
	for i := 0; i < 30 && p.fileSize(backup+".backup") == "(absent)"; i++ {
		time.Sleep(time.Second)
	}
	if p.fileSize(backup+".backup") == "(absent)" {
		p.note("m8", "ABORTED: the pre-reset backup never appeared")
		return
	}
	p.note("m8 pre-reset backup", "size="+p.fileSize(backup+".backup"))

	// 2. The run-after-reset script. The FIRST line removes the file itself.
	// ether1's DHCP client is this lab's reachability (QEMU slirp); a real
	// deploy's captured export carries the router's own addressing instead.
	body := "/file remove [find name=\"" + script + "\"]\n" +
		"/ip/dhcp-client add interface=ether1\n" +
		"/certificate add name=" + cert + " common-name=" + cert + " days-valid=30\n" +
		"/certificate sign " + cert + "\n" +
		"/ip/service set api-ssl certificate=" + cert + "\n" +
		"/ip/dns/static add name=" + marker + " address=192.0.2.99\n"
	if !p.writeFile("m8 script", script, body) {
		p.note("m8", "ABORTED: could not write the run-after-reset script")
		return
	}

	// 3. The reset. The connection is expected to drop.
	t0 := time.Now()
	rs := p.run("m8 reset-configuration", "/system/reset-configuration", 20*time.Second,
		"=no-defaults=yes", "=skip-backup=yes", "=keep-users=yes", "=run-after-reset="+script)
	p.note("m8 reset answer", fmt.Sprintf("trap=%q err=%q done=%v", rs.Trap, rs.Err, rs.Done))
	p.c.Close()

	// 4. Wait for it. api-ssl first — what MikroDash itself would use — then
	// plain api as the lab's way to put things back.
	redial := func(limit time.Duration) (*routeros.Client, string, time.Duration) {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			time.Sleep(5 * time.Second)
			c := cfg
			c.DialTimeout = 5 * time.Second
			if cl, err := routeros.Dial(c); err == nil {
				return cl, "api-ssl", time.Since(t0)
			}
			plain := cfg
			plain.TLS, plain.Port, plain.DialTimeout = false, 8728, 5*time.Second
			if cl, err := routeros.Dial(plain); err == nil {
				return cl, "api (plain)", time.Since(t0)
			}
		}
		return nil, "", 0
	}
	cl, via, after := redial(6 * time.Minute)
	if cl == nil {
		p.note("m8 came back", "NO — neither api-ssl nor plain api answered in 6 minutes. "+
			"Recover through the serial console (127.0.0.1:15000): log in as admin, then "+
			"/system/backup/load name="+backup)
		return
	}
	p.c = cl
	p.note("m8 came back", fmt.Sprintf("after %s, over %s", after.Round(time.Second), via))

	// 5. What the script did.
	p.note("m8 our login survived (keep-users)", "yes — this dial used it")
	p.note("m8 script deleted itself", fmt.Sprint(p.fileSize(script) == "(absent)"))
	mk, _ := p.c.Do(routeros.Cmd{Path: "/ip/dns/static/print",
		Args: []string{"?name=" + marker}, Timeout: 10 * time.Second})
	p.note("m8 script ran to its last line", fmt.Sprint(len(mk) > 0))
	p.note("m8 api-ssl after", before("after"))
	// THE LOG IS KEPT ONLY WHERE IT CONCERNS THE SCRIPT, with addresses masked:
	// a router's log carries addresses and MACs, and this goes into a committed
	// fixture.
	lg := p.run("m8 log after", "/log/print", 15*time.Second, "=.proplist=topics,message")
	kept := []routeros.Reply{}
	for _, r := range lg.Rows {
		m := strings.ToLower(r["message"] + " " + r["topics"])
		if strings.Contains(m, "script") || strings.Contains(m, "error") ||
			strings.Contains(m, "fail") || strings.Contains(m, "mdprobe") ||
			strings.Contains(m, "certificate") || strings.Contains(m, "reset") {
			kept = append(kept, routeros.Reply{"topics": r["topics"], "message": maskAddrs(r["message"])})
		}
	}
	p.steps[len(p.steps)-1].Rows = kept
	for _, r := range kept {
		p.note("m8 log", r["topics"]+": "+r["message"])
	}

	// 6. Put the lab back.
	ld := p.run("m8 backup load", "/system/backup/load", 20*time.Second,
		"=name="+backup+".backup", "=password=")
	p.note("m8 restore issued", fmt.Sprintf("trap=%q err=%q", ld.Trap, ld.Err))
	p.c.Close()
	t0 = time.Now()
	cl, via, after = redial(6 * time.Minute)
	if cl == nil {
		p.note("m8 restored", "NO — the router did not return after the restore. "+
			"Recover through the serial console (127.0.0.1:15000).")
		return
	}
	p.c = cl
	p.note("m8 restored", fmt.Sprintf("back after %s over %s; %s", after.Round(time.Second), via, before("restored")))
	p.note("m8 original certificate back", fmt.Sprint(before("restored") == origCert))
	if id := p.fileID(backup + ".backup"); id != "" {
		p.run("m8 remove backup", "/file/remove", 10*time.Second, "=.id="+id)
	}
}

// M13: every canned template, rendered with sample values, through RouterOS's
// own dry-run. The resource registry holds only the fields MikroDash's pages
// edit, so it cannot say whether a canned template names a real property; the
// router's parser can. A dry-run checks every argument name and value and
// applies nothing (measured, m1). A failure's own words are captured through
// /execute, as the deploy does.
func m13CannedDryRun(p *probe) {
	fmt.Fprintln(os.Stderr, "M13 canned templates through the router's dry-run")
	sample := map[string]string{"iface": "ether1", "cidr": "192.0.2.0/24", "rate": "50M", "ip": "192.0.2.10",
		"secret": "example-pass-123", "ifaddr": "192.0.2.1/24", "hostname": "pool.ntp.org", "port": "13231",
		"int": "12", "ipv4-list": "192.0.2.53", "text": "lab"}
	server := map[string]string{"mgmt_src": "192.0.2.9", "api_service": "api-ssl", "api_user": "mikrodash"}

	// THE CONTROL. A dry-run is only evidence about property names if it
	// rejects one that does not exist, and a value outside an enum; both must
	// FAIL here, or every "ok" below means nothing.
	for label, body := range map[string]string{
		"unknown property": "/ip dns\nset no-such-property=1\n",
		"bad enum value":   "/ip settings\nset rp-filter=sideways\n",
	} {
		name := prefix + "canned-control.rsc"
		if !p.writeFile("m13 control", name, body) {
			continue
		}
		s := p.run("m13 control "+label, "/import", 30*time.Second, "=file-name="+name, "=verbose=yes", "=dry-run=")
		p.note("m13 control: "+label+" rejected", fmt.Sprint(s.Trap != "" || s.Done["ret"] != "true"))
	}

	for _, c := range cfgtpl.CannedTemplates() {
		given := map[string]string{}
		for _, d := range c.Variables {
			if d.Default == "" {
				given[d.Name] = sample[d.Type]
			}
		}
		tp, err := cfgtpl.Parse(c.Body)
		if err != nil {
			p.note("m13 "+c.ID, "does not parse: "+err.Error())
			continue
		}
		vals, err := cfgtpl.Resolve(c.Variables, given, server)
		if err != nil {
			p.note("m13 "+c.ID, "values: "+err.Error())
			continue
		}
		empty := map[string][]map[string]string{}
		for _, m := range cfgtpl.EnsureMenus(tp) {
			empty[m] = nil
		}
		resolved, _, err := cfgtpl.ResolveEnsure(tp, vals, empty)
		if err == nil {
			var text string
			if text, err = cfgtpl.Render(resolved, vals); err == nil {
				name := prefix + "canned-" + c.ID + ".rsc"
				if !p.writeFile("m13 "+c.ID, name, text) {
					continue
				}
				s := p.run("m13 dry-run "+c.ID, "/import", 30*time.Second,
					"=file-name="+name, "=verbose=yes", "=dry-run=")
				verdict := "ok"
				if s.Trap != "" || s.Err != "" || s.Done["ret"] != "true" {
					verdict = fmt.Sprintf("FAILED trap=%q err=%q", s.Trap, s.Err)
					out := prefix + "canned-out"
					p.run("m13 capture "+c.ID, "/execute", 20*time.Second,
						"=script=/import file-name="+name+" verbose=yes dry-run", "=file="+out)
					time.Sleep(2 * time.Second)
					rows, _ := p.c.Do(routeros.Cmd{Path: "/file/print", Timeout: 10 * time.Second,
						Args: []string{"?name=" + out + ".txt", "=.proplist=contents"}})
					if len(rows) > 0 {
						verdict += " report=" + fmt.Sprintf("%q", rows[0]["contents"])
					}
					if id := p.fileID(out + ".txt"); id != "" {
						p.run("m13 remove report", "/file/remove", 10*time.Second, "=.id="+id)
					}
				}
				p.note("m13 "+c.ID, verdict)
				continue
			}
		}
		p.note("m13 "+c.ID, "render: "+err.Error())
	}
}

// M12: the export + reset BOOTSTRAP, as Config Management would write it.
//
// M8 showed that the first runtime error in a run-after-reset script aborts
// the rest silently, and stopped at its line 2, so its certificate steps never
// ran. This measures what the design depends on:
//
//   - that each line wrapped in `:do { … } on-error={ :log … }` survives a
//     failing line (the DHCP client a CHR keeps through a reset makes line 2
//     fail on purpose) and logs which one;
//   - that a certificate can be added, SIGNED and assigned to api-ssl inside
//     the script, so MikroDash comes back over TLS;
//   - that a final `:log` line marks the script as having reached its end.
//
// The lab is put back from a backup afterwards, as M8 does.
func m12Bootstrap(p *probe, cfg routeros.Config) {
	fmt.Fprintln(os.Stderr, "M12 export+reset bootstrap (DESTRUCTIVE: wipes and reboots the router)")
	p.cleanup()
	const backup = prefix + "preboot"
	const script = prefix + "boot.rsc"
	const cert = prefix + "api"
	marker := prefix + "boot-ran.test"

	bk := p.run("m12 backup save", "/system/backup/save", 60*time.Second, "=name="+backup, "=dont-encrypt=yes")
	if bk.Trap != "" || bk.Err != "" {
		p.note("m12", fmt.Sprintf("ABORTED: no pre-reset backup: trap=%q err=%q", bk.Trap, bk.Err))
		return
	}
	for i := 0; i < 30 && p.fileSize(backup+".backup") == "(absent)"; i++ {
		time.Sleep(time.Second)
	}
	if p.fileSize(backup+".backup") == "(absent)" {
		p.note("m12", "ABORTED: the pre-reset backup never appeared")
		return
	}

	guard := func(n int, cmd string) string {
		return fmt.Sprintf(":do { %s } on-error={ :log warning \"%sbootstrap: line %d failed\" }\n", cmd, prefix, n)
	}
	body := "/file remove [find name=\"" + script + "\"]\n" +
		guard(2, "/ip dhcp-client add interface=ether1") +
		guard(3, "/certificate add name="+cert+" common-name="+cert+" days-valid=30") +
		guard(4, "/certificate sign "+cert) +
		guard(5, "/ip service set api-ssl certificate="+cert) +
		guard(6, "/ip dns static add name="+marker+" address=192.0.2.98") +
		":log info \"" + prefix + "bootstrap: done\"\n"
	if !p.writeFile("m12 script", script, body) {
		p.note("m12", "ABORTED: could not write the bootstrap")
		return
	}

	t0 := time.Now()
	rs := p.run("m12 reset-configuration", "/system/reset-configuration", 20*time.Second,
		"=no-defaults=yes", "=skip-backup=yes", "=keep-users=yes", "=run-after-reset="+script)
	p.note("m12 reset answer", fmt.Sprintf("trap=%q err=%q", rs.Trap, rs.Err))
	p.c.Close()

	// api-ssl ONLY first: coming back over TLS is the measurement.
	var cl *routeros.Client
	via := ""
	for time.Since(t0) < 4*time.Minute && cl == nil {
		time.Sleep(5 * time.Second)
		c := cfg
		c.DialTimeout = 5 * time.Second
		if x, err := routeros.Dial(c); err == nil {
			cl, via = x, "api-ssl"
		}
	}
	if cl == nil {
		plain := cfg
		plain.TLS, plain.Port, plain.DialTimeout = false, 8728, 5*time.Second
		for time.Since(t0) < 6*time.Minute && cl == nil {
			time.Sleep(5 * time.Second)
			if x, err := routeros.Dial(plain); err == nil {
				cl, via = x, "api (plain)"
			}
		}
	}
	if cl == nil {
		p.note("m12 came back", "NO. Recover through the serial console (127.0.0.1:15000): "+
			"/system/backup/load name="+backup+".backup")
		return
	}
	p.c = cl
	p.note("m12 came back", fmt.Sprintf("after %s, over %s", time.Since(t0).Round(time.Second), via))
	p.note("m12 script deleted itself", fmt.Sprint(p.fileSize(script) == "(absent)"))
	mk, _ := p.c.Do(routeros.Cmd{Path: "/ip/dns/static/print", Args: []string{"?name=" + marker},
		Timeout: 10 * time.Second})
	p.note("m12 lines after the failing one ran", fmt.Sprint(len(mk) > 0))
	sv, _ := p.c.Do(routeros.Cmd{Path: "/ip/service/print", Timeout: 10 * time.Second,
		Args: []string{"?name=api-ssl", "=.proplist=certificate"}})
	if len(sv) > 0 {
		p.note("m12 api-ssl certificate", sv[0]["certificate"])
	}
	lg := p.run("m12 log after", "/log/print", 15*time.Second, "=.proplist=topics,message")
	kept := []routeros.Reply{}
	for _, r := range lg.Rows {
		m := strings.ToLower(r["message"] + " " + r["topics"])
		if strings.Contains(m, "bootstrap") || strings.Contains(m, "script") || strings.Contains(m, "certificate") {
			kept = append(kept, routeros.Reply{"topics": r["topics"], "message": maskAddrs(r["message"])})
		}
	}
	p.steps[len(p.steps)-1].Rows = kept
	for _, r := range kept {
		p.note("m12 log", r["topics"]+": "+r["message"])
	}

	ld := p.run("m12 backup load", "/system/backup/load", 20*time.Second,
		"=name="+backup+".backup", "=password=")
	p.note("m12 restore issued", fmt.Sprintf("trap=%q err=%q", ld.Trap, ld.Err))
	p.c.Close()
	t1 := time.Now()
	cl = nil
	for time.Since(t1) < 6*time.Minute && cl == nil {
		time.Sleep(5 * time.Second)
		c := cfg
		c.DialTimeout = 5 * time.Second
		cl, _ = routeros.Dial(c)
	}
	if cl == nil {
		p.note("m12 restored", "NO. Recover through the serial console (127.0.0.1:15000).")
		return
	}
	p.c = cl
	p.note("m12 restored", "back over api-ssl after "+fmt.Sprint(time.Since(t1).Round(time.Second)))
	if id := p.fileID(backup + ".backup"); id != "" {
		p.run("m12 remove backup", "/file/remove", 10*time.Second, "=.id="+id)
	}
}

// M11: a dead-man that reverts by LOADING A BACKUP rather than importing an
// undo file.
//
// An undo file has to be written from the template: every `set` needs the value
// it replaced, every `remove` the whole row it removed. A backup taken just
// before the change needs neither, and because the scheduler is added AFTER the
// backup, loading it also removes the scheduler, so it fires once. What this
// measures:
//
//   - whether `/system backup load` runs from a scheduler at all, with no
//     console to answer its "Restore and reboot?" prompt;
//   - whether the router comes back holding the pre-change state (a marker
//     made before the backup is there, one made after is not), and without the
//     scheduler;
//   - how long MikroDash is blind.
//
// The password is minted here, used once, and never recorded: the fixture
// shows `<one-time>`. It guards a file that lives on the router it restores,
// so it exposes nothing a reader of the router could not already see.
func m11BackupDeadMan(p *probe, cfg routeros.Config) {
	fmt.Fprintln(os.Stderr, "M11 dead-man by backup load (DESTRUCTIVE: reboots the router)")
	p.cleanup()
	const backup = prefix + "deadman"
	const sched = prefix + "deadman-load"
	before, after := prefix+"m11-before.test", prefix+"m11-after.test"

	pwb := make([]byte, 18)
	if _, err := crand.Read(pwb); err != nil {
		p.note("m11", "ABORTED: no randomness for the one-time password")
		return
	}
	pw := hex.EncodeToString(pwb)
	mask := func() {
		s := &p.steps[len(p.steps)-1]
		for i, a := range s.Args {
			s.Args[i] = strings.ReplaceAll(a, pw, "<one-time>")
		}
	}

	p.run("m11 marker before", "/ip/dns/static/add", 10*time.Second, "=name="+before, "=address=192.0.2.111")
	bk := p.run("m11 backup save", "/system/backup/save", 60*time.Second,
		"=name="+backup, "=password="+pw, "=encryption=aes-sha256")
	mask()
	if bk.Trap != "" || bk.Err != "" {
		p.note("m11", fmt.Sprintf("ABORTED: backup save trap=%q err=%q", bk.Trap, bk.Err))
		return
	}
	for i := 0; i < 30 && p.fileSize(backup+".backup") == "(absent)"; i++ {
		time.Sleep(time.Second)
	}
	p.run("m11 marker after", "/ip/dns/static/add", 10*time.Second, "=name="+after, "=address=192.0.2.112")

	const every = 20 * time.Second
	add := p.run("m11 add scheduler", "/system/scheduler/add", 10*time.Second,
		"=name="+sched, "=interval=20s",
		"=on-event=/system backup load name="+backup+".backup password="+pw)
	mask()
	if add.Trap != "" || add.Err != "" {
		p.note("m11 scheduler add", fmt.Sprintf("REFUSED trap=%q err=%q", add.Trap, add.Err))
		return
	}
	t0 := time.Now()

	// Wait for the router to go away: the old session starts failing.
	dropped := time.Duration(-1)
	for time.Since(t0) < every+60*time.Second {
		time.Sleep(2 * time.Second)
		if _, err := p.c.Do(routeros.Cmd{Path: "/system/identity/print", Timeout: 3 * time.Second}); err != nil {
			dropped = time.Since(t0)
			break
		}
	}
	if dropped < 0 {
		rows, _ := p.c.Do(routeros.Cmd{Path: "/system/scheduler/print",
			Args: []string{"?name=" + sched, "=.proplist=run-count"}, Timeout: 10 * time.Second})
		rc := "(no row)"
		if len(rows) > 0 {
			rc = rows[0]["run-count"]
		}
		p.note("m11 router rebooted", "NO — still answering after "+fmt.Sprint(every+60*time.Second)+
			"; scheduler run-count="+rc)
		return
	}
	p.note("m11 router went away after", fmt.Sprint(dropped.Round(time.Second)))
	p.c.Close()

	var cl *routeros.Client
	for time.Since(t0) < 6*time.Minute && cl == nil {
		time.Sleep(5 * time.Second)
		c := cfg
		c.DialTimeout = 5 * time.Second
		cl, _ = routeros.Dial(c)
	}
	if cl == nil {
		p.note("m11 came back", "NO — recover through the serial console (127.0.0.1:15000)")
		return
	}
	p.c = cl
	p.note("m11 came back after", fmt.Sprint(time.Since(t0).Round(time.Second)))
	has := func(path, key, val string) bool {
		rows, _ := p.c.Do(routeros.Cmd{Path: path + "/print", Args: []string{"?" + key + "=" + val},
			Timeout: 10 * time.Second})
		return len(rows) > 0
	}
	p.note("m11 marker made before the backup is back", fmt.Sprint(has("/ip/dns/static", "name", before)))
	p.note("m11 marker made after the backup is gone", fmt.Sprint(!has("/ip/dns/static", "name", after)))
	p.note("m11 scheduler is gone", fmt.Sprint(!has("/system/scheduler", "name", sched)))
	p.note("m11 backup file remains", fmt.Sprint(p.fileSize(backup+".backup") != "(absent)"))
}

// maskAddrs replaces IPv4 addresses and MACs, keeping TEST-NET-1 (192.0.2.0/24),
// which is this tool's own and identifies nothing.
func maskAddrs(s string) string {
	s = ipv4.ReplaceAllStringFunc(s, func(a string) string {
		if strings.HasPrefix(a, "192.0.2.") {
			return a
		}
		return "<ip>"
	})
	return mac.ReplaceAllString(s, "<mac>")
}

var (
	ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	mac  = regexp.MustCompile(`\b(?:[0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}\b`)
)

// fromStore is cmd/conformance's and cmd/streamcost's, deliberately a copy: each
// lab tool is a single file that keeps working if another is deleted.
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
		return routeros.Config{
			Host: r.Host, Port: r.Port, Username: r.Username, Password: pw,
			TLS: r.TLS, InsecureTLS: r.TLSInsecure,
		}, nil
	}
	return routeros.Config{}, fmt.Errorf("no router in %s is called %q", dir, name)
}
