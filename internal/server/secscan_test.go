package server

import (
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/secscan"
)

// fakeScanReader answers every read, counting how many run at once.
type fakeScanReader struct {
	inFlight, peak atomic.Int32
	reads          atomic.Int32
	trap           map[string]bool // paths that answer "no such command"
	failAt         string          // a path whose read fails in transport
	onRead         func(path string)
}

func (f *fakeScanReader) Exec(cmd routeros.Cmd) ([]routeros.Reply, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	f.reads.Add(1)
	time.Sleep(time.Millisecond)
	path := strings.TrimSuffix(cmd.Path, "/print")
	if f.onRead != nil {
		f.onRead(path)
	}
	if path == f.failAt {
		return nil, errors.New("connection reset")
	}
	if f.trap[path] {
		return nil, &routeros.Trap{Message: "no such command or directory"}
	}
	return []routeros.Reply{}, nil
}

// A SCAN READS ONE MENU AT A TIME, and every menu once: it costs the router
// one command slot, not a burst.
func TestAScanReadsOneMenuAtATime(t *testing.T) {
	f := &fakeScanReader{}
	var progress []int
	rep, code, _ := runSecScan(f, nil, func(done, total int) { progress = append(progress, done) })
	if code != "" || rep == nil {
		t.Fatalf("the scan failed: %q", code)
	}
	if p := f.peak.Load(); p != 1 {
		t.Errorf("%d reads ran at once, want 1", p)
	}
	menus := secscan.Menus()
	if int(f.reads.Load()) != len(menus) || len(progress) != len(menus) || progress[len(progress)-1] != len(menus) {
		t.Errorf("%d reads and progress %v for %d menus", f.reads.Load(), progress, len(menus))
	}
}

// AN ABSENT MENU IS UNKNOWN, NOT A FAILED SCAN; a transport failure is.
func TestAnAbsentMenuDoesNotFailTheScan(t *testing.T) {
	f := &fakeScanReader{trap: map[string]bool{"/system/routerboard": true}}
	rep, code, _ := runSecScan(f, nil, nil)
	if code != "" || rep == nil {
		t.Fatalf("a missing menu failed the scan: %q", code)
	}
	for _, fd := range rep.Findings {
		if fd.ID == "sys.firmware" && fd.Status != secscan.Unknown {
			t.Errorf("sys.firmware is %s with /system/routerboard absent, want unknown", fd.Status)
		}
	}
	f = &fakeScanReader{failAt: "/ip/dns"}
	if rep, code, msg := runSecScan(f, nil, nil); code != "failed" || rep != nil || msg == "" {
		t.Errorf("a transport failure answered %q %q with a report %v, want failed and no report", code, msg, rep != nil)
	}
}

// A STOP ENDS THE SCAN BETWEEN MENUS: a router switch or a closed socket
// closes quit, and the scan reads no further.
func TestAStoppedScanReadsNoFurther(t *testing.T) {
	quit := make(chan struct{})
	f := &fakeScanReader{}
	f.onRead = func(string) {
		if f.reads.Load() == 3 {
			close(quit)
		}
	}
	rep, code, _ := runSecScan(f, quit, nil)
	if code != codeScanStopped || rep != nil {
		t.Errorf("a stopped scan answered %q with a report %v", code, rep != nil)
	}
	if n := f.reads.Load(); n != 3 {
		t.Errorf("a scan stopped after 3 reads made %d", n)
	}
}

// THE STORE KEEPS ONE REPORT PER ROUTER and one scan per router at a time.
func TestTheScanStoreIsPerRouter(t *testing.T) {
	var st secScanStore
	if !st.begin("r1") || st.begin("r1") {
		t.Fatal("a second scan of r1 was allowed while the first ran")
	}
	if !st.begin("r2") {
		t.Fatal("r2 could not scan while r1 did")
	}
	st.end("r1", &secscan.Report{Score: 42}, time.Unix(100, 0))
	st.end("r2", nil, time.Unix(100, 0))
	if e, ok, running := st.get("r1"); !ok || e.report.Score != 42 || running {
		t.Errorf("r1: %v %v %v", e, ok, running)
	}
	if _, ok, _ := st.get("r2"); ok {
		t.Error("a scan with no report left one behind")
	}
	if !st.begin("r1") {
		t.Error("r1 could not scan again after its scan ended")
	}
}
