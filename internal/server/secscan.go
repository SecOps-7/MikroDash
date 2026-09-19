package server

import (
	"errors"
	"strconv"
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
// ── ONE SCAN PATH, THREE ASKERS ─────────────────────────────────────────────
//
// The page, the Dashboard's Security Score card and the assistant's
// `security_scan` tool all scan through runClaimedScan, so every scan is kept
// for all of them and one router is never scanned twice at once. Each scan's
// progress and result go to the router's Security Score card room, so a card
// follows a scan whoever started it. The card scans by itself only when the
// router has no report at all, so it is never empty (the operator's call,
// 2026-09-19); otherwise it shows the last one and a Rescan button.
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

// SecScorePayload is `secscore:state`: the Dashboard's Security Score card.
// Has is false when this router has no report; the counts are then zero.
type SecScorePayload struct {
	RouterID string `json:"routerId"`
	Has      bool   `json:"has"`
	Score    int    `json:"score"`
	// Issues is failed checks above info, as the page's pill and label count.
	Issues   int `json:"issues"`
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Passed   int `json:"passed"`
	Checks   int `json:"checks"`
	// ScannedAt is epoch milliseconds, or 0.
	ScannedAt int64 `json:"scannedAt"`
	Running   bool  `json:"running"`
	Done      int   `json:"done"`
	Total     int   `json:"total"`
	// Code is empty, or why there is nothing new: failed (the last scan did
	// not finish), denied or unavailable (a Rescan refused).
	Code    string `json:"code"`
	Message string `json:"message"`
}

// secScoreOf is the card's view of a router's scan state.
func secScoreOf(routerID string, e secScanEntry, ok, running bool) SecScorePayload {
	p := SecScorePayload{RouterID: routerID, Running: running, Total: len(secscan.Menus())}
	if !ok || e.report == nil {
		return p
	}
	r := e.report
	p.Has, p.Score, p.Passed, p.Checks, p.ScannedAt = true, r.Score, r.Passed, len(r.Findings), e.at.UnixMilli()
	p.Critical, p.High, p.Medium, p.Low = r.Failed["critical"], r.Failed["high"], r.Failed["medium"], r.Failed["low"]
	p.Issues = p.Critical + p.High + p.Medium + p.Low
	return p
}

// secScoreRoom is a router's Security Score card room.
func secScoreRoom(routerID string) string {
	return dashCardRoomFor(routerID, "secscore")
}

// secScoreNow is the card's view of a router right now.
func (s *Server) secScoreNow(routerID string) SecScorePayload {
	e, ok, running := s.secScans.get(routerID)
	return secScoreOf(routerID, e, ok, running)
}

// runClaimedScan scans a router whose slot the caller has claimed
// (secScans.begin), keeps the report, and frees the slot. Every step is told to
// the router's Security Score cards; `progress` is the asker's own.
func (s *Server) runClaimedScan(routerID string, rs secScanReader, quit <-chan struct{},
	progress func(done, total int)) (*secscan.Report, time.Time, string, string) {
	EvSecScore.Broadcast(s.hub, secScoreRoom(routerID), s.secScoreNow(routerID))
	rep, code, msg := runSecScan(rs, quit, func(done, total int) {
		if progress != nil {
			progress(done, total)
		}
		p := s.secScoreNow(routerID)
		p.Done = done
		EvSecScore.Broadcast(s.hub, secScoreRoom(routerID), p)
	})
	at := time.Now()
	s.secScans.end(routerID, rep, at)
	p := s.secScoreNow(routerID)
	if code == "failed" {
		p.Code, p.Message = code, msg
	}
	EvSecScore.Broadcast(s.hub, secScoreRoom(routerID), p)
	return rep, at, code, msg
}

// startSecScan claims the router's slot and scans off the read loop: thirty-odd
// reads take seconds, and this socket's other messages must not wait behind
// them. False when a scan of the router is already running. The scan is this
// connection's, so a router switch or a closed socket stops it (stopSecScan).
func (cn *conn) startSecScan(sc connScope, progress func(done, total int),
	done func(rep *secscan.Report, at time.Time, code, msg string)) bool {
	if !cn.srv.secScans.begin(sc.routerID) {
		return false
	}
	quit := make(chan struct{})
	cn.scanMu.Lock()
	cn.scanQuit = quit
	cn.scanMu.Unlock()
	go func() {
		rep, at, code, msg := cn.srv.runClaimedScan(sc.routerID, sc.rs, quit, progress)
		if done != nil {
			done(rep, at, code, msg)
		}
	}()
	return true
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
	started := cn.startSecScan(sc, func(done, total int) {
		EvSecScanResult.Send(cn.srv.hub, cn.c, SecScanPayload{RouterID: sc.routerID, Running: true,
			Done: done, Total: total})
	}, func(rep *secscan.Report, at time.Time, code, msg string) {
		out := SecScanPayload{RouterID: sc.routerID, Code: code, Message: msg, Total: len(secscan.Menus())}
		if rep != nil {
			out.Report, out.ScannedAt, out.Done = rep, at.UnixMilli(), out.Total
		}
		EvSecScanResult.Send(cn.srv.hub, cn.c, out)
	})
	if !started {
		refuse("busy")
	}
}

// secScoreFocus answers the Security Score card being shown (dashCardFocus has
// checked the Security Scan page): this router's last result, and a scan when
// there is none, so the card is never empty.
func (cn *conn) secScoreFocus() {
	sc := cn.scope()
	if sc.routerID == "" || sc.rs == nil {
		return
	}
	p := cn.srv.secScoreNow(sc.routerID)
	EvSecScore.Send(cn.srv.hub, cn.c, p)
	if !p.Has && !p.Running {
		cn.startSecScan(sc, nil, nil)
	}
}

// secScoreScan answers the card's Rescan, always with a frame, so the card's
// button never waits on nothing. A scan already running is simply followed:
// the card is in its room and hears it.
func (cn *conn) secScoreScan() {
	sc := cn.scope()
	switch {
	case sc.routerID == "" || sc.rs == nil:
		EvSecScore.Send(cn.srv.hub, cn.c, SecScorePayload{RouterID: sc.routerID, Code: "unavailable"})
	case !cn.canPageIn(sc, "security-scan", "read"):
		EvSecScore.Send(cn.srv.hub, cn.c, SecScorePayload{RouterID: sc.routerID, Code: "denied"})
	default:
		cn.startSecScan(sc, nil, nil)
		EvSecScore.Send(cn.srv.hub, cn.c, cn.srv.secScoreNow(sc.routerID))
	}
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

// ── THE ASSISTANT'S `security_scan` ─────────────────────────────────────────
//
// Always a fresh scan (the operator's call, 2026-09-19). When another asker's
// scan of this router is already running, that one is as fresh, so the tool
// waits for it rather than refusing or scanning twice.

// securityPosture is what the model gets: the report without the page's
// presentation (why-texts, passes one by one), with each failure's fix.
type securityPosture struct {
	Score      int                     `json:"score"`
	Grade      string                  `json:"grade"`
	ScannedAt  string                  `json:"scannedAt"`
	Checks     int                     `json:"checks"`
	Passed     int                     `json:"passed"`
	Failed     map[string]int          `json:"failedBySeverity"`
	Issues     []postureIssue          `json:"failedChecks"`
	Unanswered []string                `json:"unansweredChecks"`
	Categories []secscan.CategoryScore `json:"categories"`
	Facts      secscan.Facts           `json:"facts"`
}

type postureIssue struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Severity string   `json:"severity"`
	Title    string   `json:"title"`
	Found    []string `json:"found"`
	Fix      string   `json:"fix"`
	Page     string   `json:"page,omitempty"`
}

// postureFoundMax caps each failure's list of offending objects: a router with
// two hundred open rules is told "the first ten, and how many more".
const postureFoundMax = 10

// secScanWaitMax bounds the wait for another asker's scan.
var secScanWaitMax = 2 * time.Minute

// postureGrade is the page's grade words (scoreGrade's thresholds, in
// web/src/pages/tools-ping-cards.ts).
func postureGrade(score int) string {
	switch {
	case score >= 80:
		return "good"
	case score >= 60:
		return "fair"
	default:
		return "at risk"
	}
}

func postureOf(r *secscan.Report, at time.Time) securityPosture {
	p := securityPosture{Score: r.Score, Grade: postureGrade(r.Score), ScannedAt: at.UTC().Format(time.RFC3339),
		Checks: len(r.Findings), Passed: r.Passed, Failed: r.Failed, Issues: []postureIssue{},
		Unanswered: []string{}, Categories: r.Categories, Facts: r.Facts}
	for _, f := range r.Findings {
		switch f.Status {
		case secscan.Fail:
			found := f.Detail
			if len(found) > postureFoundMax {
				found = append(append([]string{}, found[:postureFoundMax]...),
					"and "+strconv.Itoa(len(f.Detail)-postureFoundMax)+" more")
			}
			p.Issues = append(p.Issues, postureIssue{ID: f.ID, Category: f.Category, Severity: string(f.Severity),
				Title: f.Title, Found: found, Fix: f.Fix, Page: f.Link})
		case secscan.Unknown:
			p.Unanswered = append(p.Unanswered, f.Title)
		}
	}
	return p
}

// freshPosture scans the router now, or waits for the scan already running.
func (cn *conn) freshPosture(sc connScope) (any, string) {
	start := time.Now()
	if cn.srv.secScans.begin(sc.routerID) {
		rep, at, code, msg := cn.srv.runClaimedScan(sc.routerID, sc.rs, nil, nil)
		if code != "" || rep == nil {
			return nil, "The security scan did not finish: " + msg
		}
		return postureOf(rep, at), ""
	}
	for time.Since(start) < secScanWaitMax {
		time.Sleep(secScanPoll)
		e, ok, running := cn.srv.secScans.get(sc.routerID)
		if running {
			continue
		}
		// A report from before the wait began is the last scan's, not this one:
		// the running scan failed or was stopped.
		if !ok || e.report == nil || e.at.Before(start) {
			return nil, "The security scan that was already running did not finish; ask again to run a new one."
		}
		return postureOf(e.report, e.at), ""
	}
	return nil, "A security scan of this router is still running; ask again in a moment."
}

// secScanPoll is how often freshPosture looks for the running scan's end.
var secScanPoll = 250 * time.Millisecond
