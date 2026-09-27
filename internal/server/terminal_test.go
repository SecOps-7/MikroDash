package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikrodash/internal/hub"
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

// termAdminConn is rawAdminConn's sibling: a signed-in global administrator,
// with `terminalEnabled` set as the test asks. It reuses that file's grant
// graph deliberately rather than restating one, so the two gates are proved
// against the same fixture and cannot drift apart.
func termAdminConn(t *testing.T, enabled bool) *conn {
	t.Helper()
	cn := rawAdminConn(t, false)
	dir := cn.srv.store.Dir
	if err := os.WriteFile(filepath.Join(dir, "settings.json"),
		[]byte(fmt.Sprintf(`{"terminalEnabled": %v}`, enabled)), 0o600); err != nil {
		t.Fatal(err)
	}
	return cn
}

// TestTheTerminalRefusesTheSameWayWhicheverGateFired.
//
// The refusal an operator sees must not say WHICH of the three gates stopped
// them, or a viewer can map an installation - whether the setting is on, and
// whether they are an administrator - by reading the wording. The audit note
// carries the real reason, which is the half nobody outside can see.
//
// Each case is driven against a connection that fails ONE gate, so this proves
// the wording is shared rather than that everything refuses identically.
func TestTheTerminalRefusesTheSameWayWhicheverGateFired(t *testing.T) {
	notAdmin := &conn{srv: &Server{hub: hub.New()}, sess: &Session{Username: "x", AuthMode: "modern"}}
	signInOff := &conn{srv: &Server{hub: hub.New()}, sess: &Session{Username: "x", AuthMode: "none"}}
	settingOff := termAdminConn(t, false)

	cases := map[string]struct {
		cn       *conn
		wantNote string
	}{
		"not an administrator": {notAdmin, "not a signed-in global administrator"},
		"sign-in is off":       {signInOff, "not a signed-in global administrator"},
		"the setting is off":   {settingOff, "terminalEnabled is off"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, note := tc.cn.terminalGateNote(tc.cn.scope())
			if note != tc.wantNote {
				t.Errorf("audit note = %q, want %q", note, tc.wantNote)
			}
			got := tc.cn.terminalGate(tc.cn.recorder(), tc.cn.scope())
			if got != terminalRefusal {
				t.Errorf("refusal = %q, want the shared sentence %q", got, terminalRefusal)
			}
			// The wording must not leak the reason.
			for _, leak := range []string{"administrator", "terminalEnabled", "setting"} {
				if strings.Contains(strings.ToLower(note), strings.ToLower(leak)) &&
					strings.Contains(got, leak) && leak != "administrator" {
					t.Errorf("the refusal repeats %q from the audit note", leak)
				}
			}
		})
	}
}

// TestSignInOffCannotRunATerminalLine.
//
// `(*Server).isGlobalAdmin` answers TRUE when sign-in is switched off, which is
// right where it is used and wrong here. The AuthMode clause in
// terminalGateNote is the compensation, and it is pinned on its own so a
// refactor that drops it fails here rather than only inside a combined case.
func TestSignInOffCannotRunATerminalLine(t *testing.T) {
	cn := termAdminConn(t, true)
	cn.sess = &Session{Username: "boss", AuthMode: "none"}
	if !cn.srv.isGlobalAdmin(cn.sess) {
		t.Fatal("isGlobalAdmin no longer returns true with sign-in off; this test now proves nothing")
	}
	if _, note := cn.terminalGateNote(cn.scope()); note != "not a signed-in global administrator" {
		t.Errorf("note = %q, want the terminal to refuse when nobody can be held to the command", note)
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
	s.clear()
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
