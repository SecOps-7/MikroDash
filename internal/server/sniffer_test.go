package server

import (
	"errors"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/diag"
	"mikrodash/internal/hub"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
)

// fakeSniffer answers the sniffer's menus and records the order it was asked.
type fakeSniffer struct {
	sent    []string
	fail    map[string]error
	packets []routeros.Reply
	// deviceMode is what /system/device-mode/print answers for `sniffer`.
	deviceMode string
	// file is what /tool/sniffer/save wrote, handed back by /file/read in
	// `chunk`-byte pieces. Empty means the capture was empty and `save` wrote
	// nothing, which is what a real router does.
	file  string
	chunk int
	// onRead runs before each /file/read answers, so a test can look at what
	// has already reached the browser.
	onRead func(nth int)
	reads  int
}

func (f *fakeSniffer) exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	f.sent = append(f.sent, cmd.Path)
	if err := f.fail[cmd.Path]; err != nil {
		return nil, err
	}
	switch cmd.Path {
	case "/system/device-mode/print":
		return []routeros.Reply{{"sniffer": f.deviceMode}}, nil
	case "/interface/print":
		return []routeros.Reply{{"name": "ether1"}}, nil
	case "/tool/sniffer/print":
		return []routeros.Reply{{"running": "true"}}, nil
	case "/tool/sniffer/packet/print":
		return f.packets, nil
	case "/tool/sniffer/protocol/print":
		return []routeros.Reply{{"protocol": "ip", "packets": "7", "bytes": "900", "share": "100"}}, nil
	case "/tool/sniffer/host/print":
		return []routeros.Reply{{"address": "198.51.100.10", "total": "500/400"}}, nil
	case "/file/print":
		if f.file == "" {
			return nil, nil
		}
		return []routeros.Reply{{"size": strconv.Itoa(len(f.file))}}, nil
	case "/file/read":
		f.reads++
		if f.onRead != nil {
			f.onRead(f.reads)
		}
		off := 0
		for _, a := range cmd.Args {
			if v, ok := strings.CutPrefix(a, "=offset="); ok {
				off, _ = strconv.Atoi(v)
			}
		}
		if off >= len(f.file) {
			return []routeros.Reply{{}}, nil
		}
		end := min(off+f.chunk, len(f.file))
		return []routeros.Reply{{"data": f.file[off:end]}}, nil
	}
	return nil, nil
}

func (f *fakeSniffer) session(t *testing.T) *session.Session {
	t.Helper()
	return session.NewForTestWithExec(hub.New(), "r1", f.exec)
}

// withFastTick makes the poll immediate for the length of a test.
func withFastTick(t *testing.T) {
	t.Helper()
	was := snifferTick
	snifferTick = time.Millisecond
	t.Cleanup(func() { snifferTick = was })
}

// ── THE CAPTURE IS STOPPED BEFORE THE FRAME THE OPERATOR IS LEFT WITH ───────
//
// Two properties, and the second is the one that would rot quietly. A capture
// left running costs the router memory and CPU with nothing on any page saying
// so; and a final frame read BEFORE the stop is a snapshot of a capture that was
// still filling, which reads as a finished result and is not one.
func TestACaptureIsStoppedAndThenReadOneLastTime(t *testing.T) {
	withFastTick(t)
	f := &fakeSniffer{deviceMode: "true"}
	quit := make(chan struct{})
	// Stop it from the second progress frame, as pressing Stop does.
	frames := 0
	sn := f.session(t)
	res, code, msg := testConn().runSniffer(connScope{routerID: "r1", rs: sn}, nopRecorder(), sn,
		toolsSnifferReq{Interface: "ether1"}, quit, func(*diag.SnifferResult) {
			frames++
			if frames == 2 {
				close(quit)
			}
		})
	if code != codeStopped || res == nil {
		t.Fatalf("code %q msg %q result %v, want a stopped run carrying its frame", code, msg, res)
	}
	if frames < 2 {
		t.Fatalf("%d progress frames; the poll never ran", frames)
	}
	if res.TotalPackets != 7 || res.TopTalker != "198.51.100.10" {
		t.Errorf("the final frame was not folded from the tables: %+v", res)
	}

	order := strings.Join(f.sent, " ")
	// The device's own gate is asked BEFORE anything is written.
	if i, j := indexOf(f.sent, "/system/device-mode/print"), indexOf(f.sent, "/tool/sniffer/set"); i < 0 || i > j {
		t.Errorf("device-mode was read at %d and the set was at %d:\n%s", i, j, order)
	}
	// ── STOPPED, THEN CONFIGURED, THEN STARTED ──────────────────────────────
	//
	// The leading stop is not tidiness. RouterOS refuses both of the next two
	// while the sniffer is running - `set` traps "cannot set, sniffer running"
	// and `start` traps "already running" - so a capture left on the device by
	// a previous run made EVERY later Start fail, with a RouterOS message the
	// page could not act on. Reproduced on a 7.24.4 CHR before this line
	// existed. A stop on an already-stopped sniffer is accepted, so it is
	// unconditional rather than behind an "is it running" read.
	if !strings.Contains(order, "/tool/sniffer/stop /tool/sniffer/set /tool/sniffer/start") {
		t.Errorf("the run did not stop, configure and then start in that order:\n%s", order)
	}
	stop := lastIndexOf(f.sent, "/tool/sniffer/stop")
	if stop < 0 {
		t.Fatalf("the capture was never stopped:\n%s", order)
	}
	// AFTER THE STOP there is one more whole poll, and nothing else.
	if got := strings.Join(f.sent[stop+1:], " "); got != "/tool/sniffer/print /tool/sniffer/packet/print "+
		"/tool/sniffer/protocol/print /tool/sniffer/host/print" {
		t.Errorf("after the stop the run did %q, want one last read of the four tables", got)
	}
}

// A device whose sniffer is switched off is told so, and NOTHING is written to
// it: the refusal must not leave the router's filters rewritten for a capture
// that never ran.
func TestADeviceThatRefusesTheSnifferIsNotConfigured(t *testing.T) {
	withFastTick(t)
	f := &fakeSniffer{deviceMode: "false"}
	sn := f.session(t)
	res, code, _ := testConn().runSniffer(connScope{routerID: "r1", rs: sn}, nopRecorder(), sn,
		toolsSnifferReq{}, make(chan struct{}), nil)
	if code != "device-mode" || res != nil {
		t.Fatalf("code %q result %v, want device-mode and nothing", code, res)
	}
	for _, p := range f.sent {
		if strings.HasPrefix(p, "/tool/sniffer/") {
			t.Errorf("the run sent %s to a device that refuses the sniffer: %v", p, f.sent)
		}
	}
	// THE CONTROL: the same request on a device that allows it does configure
	// and start. Without this the check above passes against a run that refuses
	// everything.
	ok := &fakeSniffer{deviceMode: "true"}
	okSn := ok.session(t)
	quit := make(chan struct{})
	close(quit)
	if _, code, _ := testConn().runSniffer(connScope{routerID: "r1", rs: okSn}, nopRecorder(), okSn,
		toolsSnifferReq{}, quit, nil); code != codeStopped {
		t.Fatalf("the control run answered %q, want it to have run", code)
	}
	if indexOf(ok.sent, "/tool/sniffer/start") < 0 {
		t.Errorf("the control run never started the sniffer: %v", ok.sent)
	}
}

// A device with no /system/device-mode at all is ALLOWED rather than refused:
// the pessimistic answer would be a confident wrong one about a tool that works.
func TestADeviceWithNoDeviceModeMenuIsAllowed(t *testing.T) {
	f := &fakeSniffer{fail: map[string]error{
		"/system/device-mode/print": &routeros.Trap{Message: "no such command prefix"}}}
	allowed, err := snifferAllowedOn(f.session(t))
	if err != nil || !allowed {
		t.Errorf("a trap on device-mode gave allowed=%v err=%v, want allowed and no error", allowed, err)
	}
	// THE CONTROL: a transport failure is not a trap and is not swallowed.
	g := &fakeSniffer{fail: map[string]error{"/system/device-mode/print": errors.New("not connected")}}
	if allowed, err := snifferAllowedOn(g.session(t)); err == nil || allowed {
		t.Errorf("a dead connection gave allowed=%v err=%v, want the error", allowed, err)
	}
}

// ── THE DOWNLOAD IS NAMED FROM THE BYTES, NOT FROM THE COMMAND ──────────────
//
// RouterOS 7.20 and later write PCAPNG whatever the file is called, which is in
// a note on the documentation page rather than on `save`. Measured on a 7.24.4
// CHR: 0a 0d 0d 0a. Naming every download `.pcap` would be a file whose name
// disagrees with its contents.
func TestTheCaptureIsNamedFromItsMagicBytes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		magic []byte
		want  string
	}{
		{"pcapng, which is what RouterOS 7.20+ writes", []byte{0x0a, 0x0d, 0x0d, 0x0a}, ".pcapng"},
		{"classic pcap, little endian", []byte{0xd4, 0xc3, 0xb2, 0xa1}, ".pcap"},
		{"classic pcap, big endian", []byte{0xa1, 0xb2, 0xc3, 0xd4}, ".pcap"},
		{"nanosecond pcap", []byte{0x4d, 0x3c, 0xb2, 0xa1}, ".pcap"},
		{"something else entirely", []byte("not a capture"), ".cap"},
		{"too short to have magic", []byte{0x0a}, ".cap"},
	} {
		if got := captureExt(append(tc.magic, 0, 1, 2, 3)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

// The export removes its file from the router WHATEVER HAPPENED: one capture per
// download left behind adds up on exactly the devices somebody is debugging.
func TestTheExportRemovesItsFileOnEveryPath(t *testing.T) {
	// The read fails after the save has written the file.
	f := &fakeSniffer{fail: map[string]error{"/file/print": errors.New("no")}}
	if err := saveAndStreamCapture(f.session(t), "md-cap", testSink()); err == nil {
		t.Fatal("a failed read reported success")
	}
	if indexOf(f.sent, "/file/remove") < 0 {
		t.Errorf("a failed export left the file on the router: %v", f.sent)
	}
	// AND WHEN NOTHING WAS CAPTURED: `save` writes no file, which is not a
	// failure - the caller answers "nothing to export" - and there is still
	// nothing left behind.
	g, sink := &fakeSniffer{}, testSink()
	if err := saveAndStreamCapture(g.session(t), "md-cap", sink); err != nil || sink.n != 0 {
		t.Errorf("an empty capture wrote %d bytes and gave %v, want nothing and no error", sink.n, err)
	}
	if indexOf(g.sent, "/file/remove") < 0 {
		t.Errorf("an empty export left no remove behind it: %v", g.sent)
	}
}

// ── THE CAPTURE IS FORWARDED AS IT ARRIVES, WHICH IS WHAT FEEDS THE BAR ─────
//
// A megabyte measured 39 seconds off a router the poll is also using, so the
// export streams: `Content-Length` is declared from `/file/print` and each
// 32 KB chunk goes on as it lands. The browser counts what has arrived against
// that header and draws a real progress bar.
//
// THE THIRD ASSERTION IS THE ONE THAT WOULD ROT: a handler that buffered the
// whole file and wrote it at the end passes the first two and shows the
// operator nothing for 39 seconds. So the fake looks, from inside the second
// read, at what the browser already has.
func TestTheCaptureReachesTheBrowserWhileItIsStillBeingRead(t *testing.T) {
	rec := httptest.NewRecorder()
	body := string([]byte{0x0a, 0x0d, 0x0d, 0x0a}) + strings.Repeat("x", 26)
	var midway int
	f := &fakeSniffer{file: body, chunk: 10, onRead: func(nth int) {
		if nth == 3 {
			midway = rec.Body.Len()
		}
	}}
	sink := &pcapSink{w: rec, base: "router-capture"}
	if err := saveAndStreamCapture(f.session(t), "md-cap", sink); err != nil {
		t.Fatalf("the export failed: %v", err)
	}
	if rec.Body.String() != body {
		t.Errorf("the browser got %d bytes, want the file's %d", rec.Body.Len(), len(body))
	}
	if got, want := rec.Header().Get("Content-Length"), strconv.Itoa(len(body)); got != want {
		t.Errorf("Content-Length %q, want %q - without it a short read is a download that saves", got, want)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, `"router-capture.pcapng"`) {
		t.Errorf("Content-Disposition %q, want the name the magic bytes chose", got)
	}
	if midway != 20 {
		t.Errorf("the browser held %d bytes when the third chunk was asked for, want the 20 already read - "+
			"the export is buffering, and nothing moves until it finishes", midway)
	}
}

// A short read is discovered only after some of the file has gone, so what makes
// it safe is that the response stops short of the Content-Length it promised.
// This pins that the handler still reports the failure rather than completing.
func TestAShortReadFailsTheExportRatherThanTruncatingIt(t *testing.T) {
	rec := httptest.NewRecorder()
	// /file/print says 30 bytes, /file/read has 12: the router disagrees with
	// itself, which is exactly what the length check exists for.
	f := &fakeSniffer{file: "123456789012", chunk: 6}
	f.fail = nil
	sink := &pcapSink{w: rec, base: "router-capture", size: 0}
	// Report a size larger than the file by answering /file/print from a longer
	// string, then shortening what /file/read will give.
	f.file = strings.Repeat("y", 30)
	f.onRead = func(nth int) {
		if nth == 2 {
			f.file = f.file[:6]
		}
	}
	if err := saveAndStreamCapture(f.session(t), "md-cap", sink); err == nil {
		t.Fatal("a capture that read short reported success")
	}
	if rec.Header().Get("Content-Length") != "30" {
		t.Errorf("Content-Length %q, want the 30 promised - the browser must see the response stop short",
			rec.Header().Get("Content-Length"))
	}
}

// testSink is a pcapSink writing to a recorder, so a test can read what the
// export actually put on the wire - the bytes, the status and the headers.
func testSink() *pcapSink {
	return &pcapSink{w: httptest.NewRecorder(), base: "router-capture"}
}

// testConn is a connection with nothing on it but the server the write queue
// reads its rate limiter from - which is nil here, so the limiter is skipped.
// Everything else the run needs comes in as a parameter, which is the point of
// the signature.
func testConn() *conn { return &conn{srv: &Server{}} }

// nopRecorder is a recorder with no sink: the run writes its audit row and the
// row goes nowhere, which is what these tests want. That the row is WRITTEN at
// all, and what it carries, is audit_test.go's question.
func nopRecorder() *audit.Recorder {
	return audit.New(nil, audit.ForUser("u1", "tester", "198.51.100.7"), func() int64 { return 1 })
}

func indexOf(all []string, want string) int {
	for i, s := range all {
		if s == want {
			return i
		}
	}
	return -1
}

func lastIndexOf(all []string, want string) int {
	for i := len(all) - 1; i >= 0; i-- {
		if all[i] == want {
			return i
		}
	}
	return -1
}
