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
//   - whether `/user/active` carries the address MikroDash arrives from.
//
// Each answer decides a branch of the design, so each is MEASURED here and the
// raw sentences are kept, rather than assumed and discovered wrong in
// production.
//
// ── IT REFUSES ANY ROUTER THAT IS NOT A CHR ─────────────────────────────────
//
// This tool writes: it creates files, DNS static entries, an interface list and
// a scheduler entry, and imports files. It is meant for the disposable lab CHR
// and nothing else. A label is only a name in a store, so the check that counts
// is what the router says it IS: `/system/resource` must report a CHR board
// before the first write. Everything it creates is named `mdprobe-…` and removed
// on every exit path.
//
//	go run ./cmd/importprobe -data /data -router "CHR Test" -out testdata/fixtures/import
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
		name := filepath.Join(*out, "probe-"+short+".json")
		if err := os.WriteFile(name, append(enc, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "wrote", name)
	} else {
		fmt.Println(string(enc))
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
