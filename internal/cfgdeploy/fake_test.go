package cfgdeploy

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/cfgtpl"
	"mikrodash/internal/routeros"
)

// fakeRouter is a scripted RouterOS: files, schedulers, backups, a few menus,
// and /import answering the way cmd/importprobe measured a real one answers.
// Every command is logged, so a test can say what was NEVER sent.
type fakeRouter struct {
	t     *testing.T
	clock *fakeClock

	id    Identity
	boot  time.Time
	files map[string]string // name -> contents
	fid   map[string]string // .id -> name
	sched map[string]routeros.Reply
	menus map[string][]map[string]string
	next  int

	// importDry and importReal answer /import; nil is success.
	importDry  func(file string) (trap string)
	importReal func(file string) (trap string)
	imported   []string // "dry:<body>" / "real:<body>", in order

	// lockedOut refuses a fresh login; the old session (Do) keeps answering,
	// as it does behind an input drop placed after "accept established".
	lockedOut bool
	// fire is what the dead-man does when its interval passes.
	fire func(f *fakeRouter, name string)

	log []string
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time        { return c.now }
func (c *fakeClock) Sleep(d time.Duration) { c.now = c.now.Add(d) }

func newFake(t *testing.T) *fakeRouter {
	clk := &fakeClock{now: time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)}
	f := &fakeRouter{t: t, clock: clk,
		id:    Identity{Board: "CHR", OSVersion: "7.24.4"},
		boot:  clk.now.Add(-24 * time.Hour),
		files: map[string]string{}, fid: map[string]string{},
		sched: map[string]routeros.Reply{},
		menus: map[string][]map[string]string{
			"/ip/dns/static":      {},
			"/interface/list":     {{".id": "*1", "name": "WAN"}},
			"/ip/firewall/filter": {},
		},
	}
	// By default a fired dead-man does what m11 measured: loads the backup,
	// which removes the scheduler, and reboots.
	f.fire = func(f *fakeRouter, name string) {
		delete(f.sched, name)
		f.boot = f.clock.now
		f.lockedOut = false
	}
	return f
}

// tick fires any dead-man whose interval has passed.
func (f *fakeRouter) tick() {
	for name, s := range f.sched {
		armed, _ := time.Parse(time.RFC3339Nano, s["armed"])
		if !f.clock.now.Before(armed.Add(DeadManAfter)) && s["run-count"] == "0" {
			s["run-count"] = "1"
			f.fire(f, name)
		}
	}
}

func (f *fakeRouter) arg(c routeros.Cmd, key string) string {
	for _, a := range c.Args {
		if strings.HasPrefix(a, "="+key+"=") {
			return strings.TrimPrefix(a, "="+key+"=")
		}
		if strings.HasPrefix(a, "?"+key+"=") {
			return strings.TrimPrefix(a, "?"+key+"=")
		}
	}
	return ""
}

func (f *fakeRouter) addFile(name, body string) string {
	f.next++
	id := "*F" + strconv.Itoa(f.next)
	f.files[name] = body
	f.fid[id] = name
	return id
}

func (f *fakeRouter) rmFile(name string) {
	delete(f.files, name)
	for id, n := range f.fid {
		if n == name {
			delete(f.fid, id)
		}
	}
}

func (f *fakeRouter) Do(c routeros.Cmd) ([]routeros.Reply, error) {
	f.tick()
	f.log = append(f.log, c.Path+" "+strings.Join(c.Args, " "))
	switch c.Path {
	case "/system/resource/print":
		return []routeros.Reply{{"board-name": f.id.Board, "version": f.id.OSVersion + " (stable)",
			"uptime": fmt.Sprintf("%ds", int(f.clock.now.Sub(f.boot)/time.Second))}}, nil
	case "/system/routerboard/print":
		return []routeros.Reply{{"serial-number": f.id.Serial}}, nil
	case "/file/print":
		var out []routeros.Reply
		want := f.arg(c, "name")
		names := make([]string, 0, len(f.files))
		for n := range f.files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if want != "" && n != want {
				continue
			}
			id := ""
			for i, x := range f.fid {
				if x == n {
					id = i
				}
			}
			out = append(out, routeros.Reply{".id": id, "name": n, "size": strconv.Itoa(len(f.files[n]))})
		}
		return out, nil
	case "/file/add":
		f.addFile(f.arg(c, "name"), "")
		return nil, nil
	case "/file/set":
		name, ok := f.fid[f.arg(c, ".id")]
		if !ok {
			return nil, &routeros.Trap{Message: "no such item"}
		}
		f.files[name] = f.arg(c, "contents")
		return nil, nil
	case "/file/remove":
		if n := f.arg(c, "numbers"); n != "" {
			f.rmFile(n)
		} else {
			f.rmFile(f.fid[f.arg(c, ".id")])
		}
		return nil, nil
	case "/file/read":
		body := f.files[f.arg(c, "file")]
		off, _ := strconv.Atoi(f.arg(c, "offset"))
		if off >= len(body) {
			return []routeros.Reply{{}}, nil
		}
		return []routeros.Reply{{"data": body[off:]}}, nil
	case "/system/backup/save":
		f.addFile(f.arg(c, "name")+".backup", "BINARY")
		return nil, nil
	case "/system/scheduler/print":
		var out []routeros.Reply
		want := f.arg(c, "name")
		for n, s := range f.sched {
			if want == "" || n == want {
				out = append(out, routeros.Reply{".id": s[".id"], "name": n, "run-count": s["run-count"]})
			}
		}
		return out, nil
	case "/system/scheduler/add":
		name := f.arg(c, "name")
		f.sched[name] = routeros.Reply{".id": "*S" + name, "on-event": f.arg(c, "on-event"),
			"interval": f.arg(c, "interval"), "run-count": "0", "armed": f.clock.now.Format(time.RFC3339Nano)}
		return nil, nil
	case "/system/scheduler/remove":
		for n, s := range f.sched {
			if s[".id"] == f.arg(c, ".id") {
				delete(f.sched, n)
			}
		}
		return nil, nil
	case "/import":
		file := f.arg(c, "file-name")
		body, ok := f.files[file]
		if !ok {
			return nil, &routeros.Trap{Message: "Script Error: Cannot open import file"}
		}
		dry := false
		for _, a := range c.Args {
			if strings.HasPrefix(a, "=dry-run") {
				dry = true
			}
		}
		answer, kind := f.importReal, "real:"
		if dry {
			answer, kind = f.importDry, "dry:"
		}
		f.imported = append(f.imported, kind+body)
		if answer != nil {
			if trap := answer(file); trap != "" {
				return nil, &routeros.Trap{Message: trap}
			}
		}
		if c.Done != nil {
			*c.Done = map[string]string{"ret": "true"}
		}
		return nil, nil
	case "/execute":
		f.addFile(f.arg(c, "file")+".txt", "#line 1\r\n/ip dns static\r\nexpected end of command (line 2 column 5)\r\n")
		return nil, nil
	}
	if strings.HasSuffix(c.Path, "/export") {
		menu := strings.TrimSuffix(c.Path, "/export")
		f.addFile(f.arg(c, "file")+".rsc", "# 2026-09-21 12:00:00 by RouterOS 7.24.4\n"+
			strings.ReplaceAll(strings.TrimPrefix(menu, "/"), "/", " ")+"\n")
		return nil, nil
	}
	if strings.HasSuffix(c.Path, "/print") {
		rows, ok := f.menus[strings.TrimSuffix(c.Path, "/print")]
		if !ok {
			return nil, &routeros.Trap{Message: "no such command prefix"}
		}
		out := make([]routeros.Reply, len(rows))
		for i, r := range rows {
			out[i] = r
		}
		return out, nil
	}
	return nil, errors.New("fake router: unexpected " + c.Path)
}

// sent reports whether any logged command starts with prefix.
func (f *fakeRouter) sent(prefix string) bool {
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			return true
		}
	}
	return false
}

// ours lists what Config Management left on the router.
func (f *fakeRouter) ours() []string {
	var out []string
	for n := range f.files {
		if cfgtpl.IsOurFile(n) {
			out = append(out, n)
		}
	}
	for n := range f.sched {
		out = append(out, "scheduler "+n)
	}
	return out
}

func (f *fakeRouter) env() (Env, *[]string) {
	steps := &[]string{}
	return Env{
		Do: f.Do,
		Fresh: func() (Identity, error) {
			f.tick()
			if f.lockedOut {
				return Identity{}, errors.New("i/o timeout")
			}
			id := f.id
			id.Uptime = f.clock.now.Sub(f.boot)
			return id, nil
		},
		Backup: func() (int64, error) { return 7, nil },
		Step:   func(s string) { *steps = append(*steps, s) },
		Now:    f.clock.Now,
		Sleep:  f.clock.Sleep,
	}, steps
}
