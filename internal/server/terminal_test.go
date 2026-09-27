package server

import (
	"errors"
	"strings"
	"testing"

	"mikrodash/internal/hub"
	"mikrodash/internal/rbac"
	"mikrodash/internal/routeros"
)

// fakeExec is a router that answers however a test says.
type fakeExec struct {
	reply func(routeros.Cmd) ([]routeros.Reply, error)
	got   []routeros.Cmd
}

func (f *fakeExec) Exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	f.got = append(f.got, cmd)
	if f.reply == nil {
		return nil, nil
	}
	return f.reply(cmd)
}

// termGrantedConn is a connection whose viewer HAS terminal write access on the
// selected device. It reuses ai_raw_test.go's grant graph deliberately rather
// than restating one, so the two paths that leave the resource registry are
// proved against the same fixture and cannot drift apart.
func termGrantedConn(t *testing.T) *conn {
	t.Helper()
	cn := rawAdminConn(t, false)

	// A DEVICE THE RESOLVER KNOWS. `rbac.CanPage` refuses outright when the
	// router id is empty, because a page permission in this app is per device -
	// "may they use the Terminal" has no answer until you say where. The
	// fixture next door has no router because the raw-command gate never needed
	// one, so this adds it rather than weakening the question.
	const routerID = "r1"
	cn.srv.rbac = rbac.New(cn.srv.auditDB, func() []rbac.Router { return []rbac.Router{{ID: routerID}} })

	// Everything else canPageIn actually checks. A control refused for an
	// unrelated reason would let the refusals above pass against a build that
	// refuses everybody, which is the one failure this page must not have.
	//
	//	Readable   CanReadRouter, the coarse session's device list
	//	Pages      the coarse union, ANDed with the resolver and never replaced
	//	userID     the grant graph's key, which the resolver is asked about
	cn.routerID = routerID
	cn.sess.Readable = []string{routerID}
	cn.sess.Pages = map[string]string{"terminal": "write"}
	cn.userID = cn.srv.userIDFor(cn.sess.Username)
	if cn.userID == "" {
		t.Fatal("the fixture's user has no grant-graph id")
	}
	return cn
}

// TestTheTerminalIsGatedByThePermissionMatrixAndNothingElse.
//
// The gate used to be three checks - an install switch, a global-administrator
// clause, and the page grant. It is now the grant alone, so what this pins is
// that the grant is genuinely load-bearing and that the refusal says the same
// thing however it was reached.
//
// The properties below are not this file's to implement: they belong to
// canPageIn, and they are asserted here because the Terminal is the page where
// getting them wrong costs the most.
func TestTheTerminalIsGatedByThePermissionMatrixAndNothingElse(t *testing.T) {
	cases := map[string]*conn{
		// No session at all.
		"nobody": {srv: &Server{hub: hub.New()}},
		// SIGN-IN SWITCHED OFF. canPageIn refuses `write` outright in this
		// mode, which is what stops a command nobody can be held to. The old
		// gate spelled this out by hand; it is the matrix's property now, and
		// pinned here so a change to that path is noticed on this page.
		"sign-in off": {srv: &Server{hub: hub.New()}, sess: &Session{Username: "x", AuthMode: "none"}},
		// SIGNED IN, NO GRANT, and no RBAC resolver behind it: fails closed.
		"no grant": {srv: &Server{hub: hub.New()}, sess: &Session{Username: "x", AuthMode: "modern"}},
	}
	for name, cn := range cases {
		t.Run(name, func(t *testing.T) {
			if note := cn.terminalGateNote(cn.scope()); note == "" {
				t.Fatal("the gate let this viewer through")
			}
			if got := cn.terminalGate(cn.recorder(), cn.scope()); got != terminalRefusal {
				t.Errorf("refusal = %q, want the shared sentence %q", got, terminalRefusal)
			}
		})
	}

	// THE CONTROL. Without it, every assertion above would pass against a build
	// that refused everybody, which is the failure this whole page must not have.
	granted := termGrantedConn(t)
	if note := granted.terminalGateNote(granted.scope()); note != "" {
		t.Errorf("a viewer WITH the grant was refused (%q); the assertions above "+
			"would then be measuring a build that refuses everyone", note)
	}

	// AND IT IS PER DEVICE, which is the whole reason the permission is a page
	// key rather than a switch. Somebody trusted with a console on the lab CHR
	// is not thereby trusted with one on the edge router, and `rbac.CanPage`
	// refuses a device its resolver has never heard of.
	elsewhere := granted.scope()
	elsewhere.routerID = "some-other-device"
	if note := granted.terminalGateNote(elsewhere); note == "" {
		t.Error("a grant on one device let the terminal run on another")
	}
}

// TestTheRefusalDoesNotSayWhyItRefused.
//
// A viewer who may not use the page learns that and nothing else. The audit row
// carries the reason, which is the half nobody outside can read.
func TestTheRefusalDoesNotSayWhyItRefused(t *testing.T) {
	cn := &conn{srv: &Server{hub: hub.New()}, sess: &Session{Username: "x", AuthMode: "none"}}
	note := cn.terminalGateNote(cn.scope())
	if note == "" {
		t.Fatal("this viewer was not refused, so there is nothing to compare")
	}
	if strings.Contains(terminalRefusal, note) {
		t.Errorf("the refusal %q repeats the audit note %q", terminalRefusal, note)
	}
}

// TestASecretNeverReachesTheAuditRowButAlwaysReachesTheRouter.
//
// Both halves, because either alone would be satisfied by a broken build: a
// mask that dropped the value would keep it out of the trail AND out of the
// command, and the command would silently stop working.
func TestASecretNeverReachesTheAuditRowButAlwaysReachesTheRouter(t *testing.T) {
	const line = `/user/add name=bob password=hunter2 comment="ok"`

	shown := maskTypedLine(line)
	if strings.Contains(shown, "hunter2") {
		t.Errorf("the masked line still carries the secret: %q", shown)
	}
	if !strings.Contains(shown, "name=bob") {
		t.Errorf("the masked line lost an ordinary field: %q", shown)
	}

	f := &fakeExec{reply: func(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }}
	if _, _, err := runTerminalLine(f, line); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 {
		t.Fatalf("sent %d commands, want 1", len(f.got))
	}
	// WHAT IS SENT IS BYTE-IDENTICAL TO WHAT WAS TYPED. A terminal that quietly
	// sent the masked form would mask the password out of the command itself.
	if want := "=script=" + line; f.got[0].Args[0] != want {
		t.Errorf("sent %q, want the typed line verbatim %q", f.got[0].Args[0], want)
	}
	if f.got[0].Path != "/execute" {
		t.Errorf("path = %q, want /execute", f.got[0].Path)
	}
}

// TestTheReplyIsReadTheWayTheRouterActuallySendsIt.
//
// Measured against a RouterOS 7.24.4 CHR: `as-string` puts the console text in
// the `!done` `ret` word and sends ZERO `!re` sentences. Rows are read as well,
// so a router that answers some other way is visible rather than silently
// rendered as nothing - and that fallback is pinned here so it is not tidied
// away as dead code.
func TestTheReplyIsReadTheWayTheRouterActuallySendsIt(t *testing.T) {
	t.Run("the ret word, which is what a real router sends", func(t *testing.T) {
		f := &fakeExec{reply: func(c routeros.Cmd) ([]routeros.Reply, error) {
			*c.Ret = "line one\r\nline two"
			return nil, nil
		}}
		out, _, err := runTerminalLine(f, "/ip/address/print")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 || out[0] != "line one" || out[1] != "line two" {
			t.Errorf("out = %q, want the two CRLF-separated lines", out)
		}
	})
	t.Run("rows, the shape no measured router used", func(t *testing.T) {
		f := &fakeExec{reply: func(routeros.Cmd) ([]routeros.Reply, error) {
			return []routeros.Reply{{"ret": "a\r\nb"}}, nil
		}}
		out, _, err := runTerminalLine(f, "/ip/address/print")
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 2 {
			t.Errorf("out = %q, want the row's text read too", out)
		}
	})
	t.Run("a silent success is not an error", func(t *testing.T) {
		f := &fakeExec{reply: func(routeros.Cmd) ([]routeros.Reply, error) { return nil, nil }}
		out, trunc, err := runTerminalLine(f, `/file/remove [find name="nope"]`)
		if err != nil || trunc || len(out) != 0 {
			t.Errorf("out=%q trunc=%v err=%v, want an empty answer and no error", out, trunc, err)
		}
	})
	t.Run("a null slice never reaches the browser", func(t *testing.T) {
		f := &fakeExec{reply: func(routeros.Cmd) ([]routeros.Reply, error) {
			return nil, errors.New("boom")
		}}
		out, _, err := runTerminalLine(f, "x")
		if err == nil {
			t.Fatal("want the error through")
		}
		if out == nil {
			t.Error("out is nil; TestNoServerPayloadSendsANullArray's rule holds here too")
		}
	})
}

// TestTerminalOutputIsCapped, both ways round: by lines and by bytes.
func TestTerminalOutputIsCapped(t *testing.T) {
	many := strings.Repeat("x\r\n", terminalMaxLines+500)
	out, trunc := terminalLines(many)
	if !trunc || len(out) != terminalMaxLines {
		t.Errorf("lines = %d trunc = %v, want %d and true", len(out), trunc, terminalMaxLines)
	}
	wide := strings.Repeat("y", terminalMaxBytes+10)
	out, trunc = terminalLines(wide)
	if !trunc || len(out) != 0 {
		t.Errorf("one over-long line: got %d lines trunc=%v, want it cut by bytes", len(out), trunc)
	}
}

// TestTheScrollbackSurvivesAPageSwitchAndIsBoundedBothWays.
//
// The pane is kept on the server because the hub drops frames and a terminal
// answer is not a snapshot that re-emits. That is what this asserts: what was
// appended comes back, and it stays inside both caps.
func TestTheScrollbackSurvivesAPageSwitchAndIsBoundedBothWays(t *testing.T) {
	var s termState
	for i := 0; i < 3; i++ {
		s.append("r1", TermEntry{Seq: i, Command: "cmd", Lines: []string{"out"}})
	}
	id, got, trimmed, _ := s.snapshot()
	if id != "r1" || len(got) != 3 || trimmed {
		t.Fatalf("router=%q entries=%d trimmed=%v, want r1/3/false", id, len(got), trimmed)
	}

	for i := 0; i < terminalScrollbackEntries+10; i++ {
		s.append("r1", TermEntry{Seq: i, Lines: []string{"x"}})
	}
	_, got, trimmed, _ = s.snapshot()
	if len(got) != terminalScrollbackEntries || !trimmed {
		t.Errorf("entries=%d trimmed=%v, want %d and true", len(got), trimmed, terminalScrollbackEntries)
	}

	// The byte cap bites before the entry cap when the output is large.
	var big termState
	huge := strings.Repeat("z", 64*1024)
	for i := 0; i < 10; i++ {
		big.append("r1", TermEntry{Seq: i, Lines: []string{huge}})
	}
	_, got, trimmed, _ = big.snapshot()
	if !trimmed || len(got) >= 10 {
		t.Errorf("entries=%d trimmed=%v, want the byte cap to have evicted some", len(got), trimmed)
	}

	// A snapshot is a copy, not the live slice.
	_, a, _, _ := big.snapshot()
	if len(a) > 0 {
		a[0].Command = "mutated"
		_, b, _, _ := big.snapshot()
		if b[0].Command == "mutated" {
			t.Error("snapshot handed out the live slice; a caller can rewrite the pane")
		}
	}
}

// TestAResultThatLandsAfterARouterSwitchIsDiscarded.
//
// A result belongs to the device it was asked of. Appending one device's output
// to another's pane is a wrong answer that looks entirely right.
func TestAResultThatLandsAfterARouterSwitchIsDiscarded(t *testing.T) {
	var s termState
	s.append("r1", TermEntry{Seq: 1, Lines: []string{"from r1"}})
	s.reset()
	s.append("r2", TermEntry{Seq: 2, Lines: []string{"from r2"}})
	// The late frame from the router we have left.
	s.append("r1", TermEntry{Seq: 1, Lines: []string{"stale"}})

	id, got, _, _ := s.snapshot()
	if id != "r2" || len(got) != 1 || got[0].Lines[0] != "from r2" {
		t.Errorf("router=%q entries=%v, want only r2's own result", id, got)
	}
}

// TestOnlyOneLineRunsAtATime: the latch, so two answers cannot interleave with
// no way to tell which replied to what.
func TestOnlyOneLineRunsAtATime(t *testing.T) {
	var s termState
	if !s.begin() {
		t.Fatal("the first line was refused")
	}
	if s.begin() {
		t.Error("a second line started while one was running")
	}
	s.end()
	if !s.begin() {
		t.Error("the slot was not released")
	}
}

// TestStoppingIsHonestAboutWhatItStops.
//
// There is no cancellation on Session.Exec, so `term:stop` stops MikroDash
// waiting and nothing more. disarm() reporting false when nothing is running is
// what keeps termStop from sending a "stopped" frame for a run that never was.
func TestStoppingIsHonestAboutWhatItStops(t *testing.T) {
	var s termState
	if s.disarm() {
		t.Error("disarm reported a run when none was armed")
	}
	q := s.arm()
	if !s.disarm() {
		t.Error("disarm did not report the armed run")
	}
	select {
	case <-q:
	default:
		t.Error("the quit channel was not closed, so the result would still be delivered")
	}
	if s.disarm() {
		t.Error("disarm reported the same run twice; a second close would panic")
	}
}

// TestTheBannerIsBuiltFromWhateverTheDeviceAnswered.
//
// Every field in the greeting comes off a device that may not have answered, so
// the interesting case is not the complete one - it is the empty one. A banner
// that renders "  ·  ·  " for a device that said nothing, or that refuses to
// open at all, would be worse than one that simply says less.
func TestTheBannerIsBuiltFromWhateverTheDeviceAnswered(t *testing.T) {
	full := terminalBanner("CHR Test", routeros.Reply{
		"version": "7.24.4", "board-name": "CHR", "architecture-name": "x86_64", "uptime": "2h13m",
	}, 2026)
	joined := strings.Join(full, "\n")
	for _, want := range []string{"MikroTik RouterOS 7.24.4", "(c) 1999-2026", "CHR Test", "x86_64", "up 2h13m"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the banner is missing %q:\n%s", want, joined)
		}
	}
	// The wordmark is there and spells the name, rather than being six lines of
	// whatever a refactor left behind.
	if !strings.Contains(joined, "MMM") || !strings.Contains(joined, "KKK") {
		t.Error("the banner carries no wordmark")
	}

	// NOTHING CAME BACK. A device that answered neither read still gets a
	// banner, and it must not contain a dangling separator.
	bare := strings.Join(terminalBanner("", routeros.Reply{}, 2026), "\n")
	if strings.Contains(bare, "\u00b7") {
		t.Errorf("a device that answered nothing rendered a separator with nothing around it:\n%s", bare)
	}
	if !strings.Contains(bare, "MikroTik RouterOS") {
		t.Error("a device that answered nothing got no banner at all")
	}
	if strings.Contains(bare, "up ") {
		t.Error("an uptime was rendered for a device that reported none")
	}

	// HALF AN ANSWER. Only the parts that exist are joined.
	half := strings.Join(terminalBanner("edge1", routeros.Reply{"version": "7.20"}, 2026), "\n")
	if !strings.Contains(half, "edge1") || strings.Contains(half, "\u00b7") {
		t.Errorf("a partial answer did not join cleanly:\n%s", half)
	}

	// The year is the caller's, so the copyright line does not silently age.
	if !strings.Contains(strings.Join(terminalBanner("", routeros.Reply{}, 2031), "\n"), "1999-2031") {
		t.Error("the banner hardcodes its year")
	}
}

// TestTheGreetingIsWrittenOncePerDeviceSession.
//
// The banner is two router reads. Writing it on every page focus would spend
// them every time somebody walked onto the page, and would stack banners up the
// pane; writing it never would leave the terminal blank. The latch is the whole
// of the difference, and the two kinds of clear treat it differently on purpose.
func TestTheGreetingIsWrittenOncePerDeviceSession(t *testing.T) {
	var s termState
	if !s.claimGreeting("r1") {
		t.Fatal("the first focus did not claim the greeting")
	}
	if s.claimGreeting("r1") {
		t.Error("a second focus claimed it again; the banner would be printed twice")
	}
	s.greet("r1", "CHR Test", TermEntry{Seq: 1, Lines: []string{"banner"}})
	if s.who() != "CHR Test" {
		t.Errorf("identity = %q, want the name the greeting read", s.who())
	}

	// WIPE IS THE OPERATOR PRESSING CLEAR. A terminal you have just cleared does
	// not print its login banner again.
	s.wipe()
	if _, got, _, _ := s.snapshot(); len(got) != 0 {
		t.Errorf("wipe left %d entries", len(got))
	}
	if s.claimGreeting("r1") {
		t.Error("Clear made the banner come back, which undoes the clear")
	}
	if s.who() != "CHR Test" {
		t.Error("Clear forgot the device's name, so the prompt would lose it")
	}

	// RESET IS A DEVICE SWITCH. The next device prints its own.
	s.reset()
	if s.who() != "" {
		t.Error("a device switch kept the previous device's name in the prompt")
	}
	if !s.claimGreeting("r2") {
		t.Error("a device switch did not re-arm the greeting, so the new device gets no banner")
	}
}
