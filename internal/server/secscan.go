package server

import (
	"errors"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/secscan"
)

// The Security Scan page: internal/secscan's catalogue, run on demand.
//
// ── ON DEMAND, ONE MENU AT A TIME, KEPT PER ROUTER ──────────────────────────
//
// The scan runs when the page asks (on opening it with no fresh result, and on
// Rescan) and never in the background (the operator's call, 2026-09-19). It
// reads `secscan.Menus()` one after another, each with its proplist, so it
// holds one of the router's command slots at a time and the collectors keep
// theirs. The report is kept per router in memory: reopening the page, or a
// second viewer of the same router, is answered at once, with its age.
//
// ── ONE SCAN PER ROUTER ─────────────────────────────────────────────────────
//
// Two viewers pressing Rescan on one router run one scan: the second is told
// one is running and asks for the result when it lands. A router switch or a
// closed socket stops the scan between menus (releaseRouter calls
// stopSecScan), so it does not go on reading a router nobody is looking at.
//
// A menu the router does not have, or will not show this API user, is ABSENT,
// and its checks answer "unknown". Only a transport failure fails the scan.

// SecScanPayload is `secscan:result`: a scan's report, its progress, or why
// there is none.
type SecScanPayload struct {
	RouterID string          `json:"routerId"`
	Report   *secscan.Report `json:"report"`
	// ScannedAt is epoch milliseconds, or 0 when this router has no report.
	ScannedAt int64 `json:"scannedAt"`
	// Running is a scan in flight; Done and Total are its menus read so far.
	Running bool `json:"running"`
	Done    int  `json:"done"`
	Total   int  `json:"total"`
	// Code is empty, or one of: denied, unavailable, busy, failed, stopped.
	Code    string `json:"code"`
	Message string `json:"message"`
}

type secScanEntry struct {
	report *secscan.Report
	at     time.Time
}

// secScanStore is the last report per router, and which routers are scanning.
// Its zero value is ready.
type secScanStore struct {
	mu      sync.Mutex
	last    map[string]secScanEntry
	running map[string]bool
}

// get is a router's last report, and whether a scan of it is running.
func (st *secScanStore) get(routerID string) (secScanEntry, bool, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	e, ok := st.last[routerID]
	return e, ok, st.running[routerID]
}

// begin claims the router's scan slot, false when one is already running.
func (st *secScanStore) begin(routerID string) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running == nil {
		st.running = map[string]bool{}
	}
	if st.running[routerID] {
		return false
	}
	st.running[routerID] = true
	return true
}

// end frees the slot and keeps the report, when there is one.
func (st *secScanStore) end(routerID string, rep *secscan.Report, at time.Time) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.running, routerID)
	if rep != nil {
		if st.last == nil {
			st.last = map[string]secScanEntry{}
		}
		st.last[routerID] = secScanEntry{report: rep, at: at}
	}
}

// secScanGet answers `secscan:get`: this router's last report, if any.
func (cn *conn) secScanGet() {
	sc := cn.scope()
	out := SecScanPayload{RouterID: sc.routerID, Total: len(secscan.Menus())}
	switch {
	case sc.routerID == "" || sc.rs == nil:
		out.Code = "unavailable"
	case !cn.canPageIn(sc, "security-scan", "read"):
		out.Code = "denied"
	default:
		e, ok, running := cn.srv.secScans.get(sc.routerID)
		if ok {
			out.Report, out.ScannedAt = e.report, e.at.UnixMilli()
		}
		out.Running = running
	}
	EvSecScanResult.Send(cn.srv.hub, cn.c, out)
}

// secScanRun answers `secscan:run`: scan the selected router now.
func (cn *conn) secScanRun() {
	sc := cn.scope()
	refuse := func(code string) {
		EvSecScanResult.Send(cn.srv.hub, cn.c, SecScanPayload{RouterID: sc.routerID, Code: code})
	}
	if sc.routerID == "" || sc.rs == nil {
		refuse("unavailable")
		return
	}
	if !cn.canPageIn(sc, "security-scan", "read") {
		refuse("denied")
		return
	}
	if !cn.srv.secScans.begin(sc.routerID) {
		refuse("busy")
		return
	}
	quit := make(chan struct{})
	cn.scanMu.Lock()
	cn.scanQuit = quit
	cn.scanMu.Unlock()
	// OFF THE READ LOOP: thirty-odd reads take seconds, and this socket's other
	// messages must not wait behind them.
	go func() {
		rep, code, msg := runSecScan(sc.rs, quit, func(done, total int) {
			EvSecScanResult.Send(cn.srv.hub, cn.c, SecScanPayload{RouterID: sc.routerID, Running: true,
				Done: done, Total: total})
		})
		at := time.Now()
		cn.srv.secScans.end(sc.routerID, rep, at)
		out := SecScanPayload{RouterID: sc.routerID, Code: code, Message: msg, Total: len(secscan.Menus())}
		if rep != nil {
			out.Report, out.ScannedAt, out.Done = rep, at.UnixMilli(), out.Total
		}
		EvSecScanResult.Send(cn.srv.hub, cn.c, out)
	}()
}

// stopSecScan ends this connection's scan in flight: releaseRouter calls it.
func (cn *conn) stopSecScan() {
	cn.scanMu.Lock()
	defer cn.scanMu.Unlock()
	if cn.scanQuit != nil {
		close(cn.scanQuit)
		cn.scanQuit = nil
	}
}

// secScanReader is the part of a router connection a scan needs:
// *session.Session, or a test's fake.
type secScanReader interface {
	Exec(cmd routeros.Cmd) ([]routeros.Reply, error)
}

// codeScanStopped is a scan ended by quit; nothing is kept for it.
const codeScanStopped = "stopped"

// runSecScan reads every menu the catalogue needs, one at a time, and runs it.
// `progress` is told after each menu. Returns the report, or a code and the
// router's words.
func runSecScan(rs secScanReader, quit <-chan struct{}, progress func(done, total int)) (*secscan.Report, string, string) {
	menus := secscan.Menus()
	in := secscan.Inputs{Rows: map[string][]secscan.Row{}, Absent: map[string]bool{}, Now: time.Now()}
	for i, m := range menus {
		select {
		case <-quit:
			return nil, codeScanStopped, ""
		default:
		}
		cmd := routeros.Cmd{Path: m.Path + "/print", Timeout: 15 * time.Second}
		if len(m.Props) > 0 {
			cmd.Args = []string{"=.proplist=" + strings.Join(m.Props, ",")}
		}
		rows, err := rs.Exec(cmd)
		if err != nil {
			var trap *routeros.Trap
			if errors.As(err, &trap) {
				// No such menu on this build, or not for this user: its checks
				// answer "unknown" rather than failing the scan.
				in.Absent[m.Path] = true
			} else {
				return nil, "failed", safe.Message(err.Error())
			}
		} else {
			out := make([]secscan.Row, 0, len(rows))
			for _, r := range rows {
				out = append(out, secscan.Row(r))
			}
			in.Rows[m.Path] = out
		}
		if progress != nil {
			progress(i+1, len(menus))
		}
	}
	rep := secscan.Run(in)
	return &rep, "", ""
}
