package server

import (
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"mikrodash/internal/audit"
	"mikrodash/internal/routeros"
	"mikrodash/internal/session"
	"mikrodash/internal/wifiscan"
)

// The Frequency Analyser's three inbound actions.
//
// This is the one deliberately DISRUPTIVE thing the application does: the scan
// takes the chosen radio off the air and drops every client on it. Everything
// here is shaped by that — two independent permission gates, an audit record
// written BEFORE the command is sent, and a runner that bounds the damage.

// scanErr answers in the page's own vocabulary. `scanId` is always present and
// null when the failure happened before a scan existed, so the page never has to
// distinguish a missing key from a null one.
func (cn *conn) scanErr(code string, extra map[string]any) {
	body := map[string]any{"scanId": nil, "code": code}
	for k, v := range extra {
		body[k] = v
	}
	cn.srv.hub.Send(cn.c, "wifiscan:error", body)
}

// mayScan is BOTH gates, deliberately.
//
// `canPage("wireless", …)` carries the install-wide Wireless toggle — a
// deployment that turned the page off must not have a working scan endpoint —
// and `router:scan` keeps the capability a named, greppable thing rather than
// something implied by a page. Either alone would be a hole: the page toggle
// says nothing about who may disrupt a radio, and the capability says nothing
// about whether the feature is switched on at all.
func (cn *conn) mayScan() bool {
	if !cn.canPage("wireless", "write") {
		return false
	}
	if cn.sess == nil {
		return false
	}
	if cn.sess.AuthMode == "none" {
		return true
	}
	if !cn.srv.rbac.Available() {
		return true // the documented gap, reported at startup
	}
	ok, err := cn.srv.rbac.Can(cn.userID, "router:scan", cn.routerID)
	if err != nil {
		log.Printf("[rbac] router:scan on %s: %v", cn.routerID, err)
		return false
	}
	return ok
}

// wifiscanInterfaces answers `wifiscan:interfaces` with the radios that can be
// scanned, and whether this caller may scan at all.
//
// `permitted` is a SEPARATE field from the list, and the button is drawn from
// it. A viewer who may read the Wireless page still gets the list — the page
// shows what is there — but no button. Folding the two together would either
// hide the radios from a reader or offer a scan to someone who cannot run one.
func (cn *conn) wifiscanInterfaces() {
	if cn.routerID == "" || cn.rsession == nil {
		cn.scanErr("unavailable", nil)
		return
	}
	if !cn.canPage("wireless", "read") {
		cn.scanErr("denied", nil)
		return
	}
	list := cn.scannableInterfaces()
	out := make([]map[string]any, 0, len(list))
	for _, i := range list {
		out = append(out, map[string]any{"name": i.Name, "running": i.Running, "clients": i.Clients})
	}
	_, scanning := cn.srv.scans.Running(cn.routerID)
	cn.srv.hub.Send(cn.c, "wifiscan:interfaces", map[string]any{
		"permitted":  cn.mayScan(),
		"interfaces": out,
		"scanning":   scanning,
	})
}

type wifiscanStartReq struct {
	Iface       string `json:"iface"`
	DurationSec int    `json:"durationSec"`
}

// wifiscanStart admits and launches one scan.
func (cn *conn) wifiscanStart(raw json.RawMessage) {
	if cn.routerID == "" || cn.rsession == nil {
		cn.scanErr("unavailable", nil)
		return
	}
	var req wifiscanStartReq
	_ = json.Unmarshal(raw, &req)

	if !cn.mayScan() {
		// The REFUSAL is recorded too, and with the interface named. Someone
		// repeatedly trying to take a radio off the air is exactly what an audit
		// trail is for, and a denial that leaves no trace is invisible.
		cn.recorder().Denied(audit.Event{
			Action: "wifi.scan", TargetType: "interface", TargetName: req.Iface,
			RouterID: cn.routerID,
		})
		cn.scanErr("denied", nil)
		return
	}

	ifaces, known := cn.admitInterfaces()
	admit := wifiscan.AdmitRequest{
		RouterID: cn.routerID, HasROS: cn.rsession != nil,
		Connected:   cn.rsession != nil && cn.rsession.Connected(),
		Iface:       req.Iface,
		DurationSec: req.DurationSec,
		SocketID:    cn.c.ID,
		Interfaces:  ifaces, InterfacesKnown: known,
	}

	// BEFORE the command is sent, not after. A scan that starts and then fails
	// to be recorded still took the radio off the air, and the note says what the
	// action costs so the trail is readable by someone who does not know the
	// feature.
	cn.recorder().Record(audit.Event{
		Action: "wifi.scan", TargetType: "interface", TargetName: req.Iface,
		RouterID: cn.routerID,
		Note:     "takes the radio off the air; connected clients are disconnected for the scan",
	})

	// READ THE OPERATING CHANNEL BEFORE THE SCAN, not during one.
	//
	// During a scan the radio is off its channel, and the interface's own
	// `channel.frequency` is a configured RANGE ("5180-5730") rather than where
	// the radio actually is — so asking later, or asking the catalogue, gives an
	// answer that is wrong in two different ways.
	//
	// A fast `=once=` read, and ADVISORY: the dialog marks the current channel on
	// the chart with it, and a scan without it is still worth having. So a failure
	// is swallowed rather than refused.
	currentChannel := cn.currentChannelMhz(req.Iface)

	// The terminal event is bound to THIS connection when the scan begins, not
	// looked up when it ends. By then the operator may have navigated away, and
	// the registry is fleet-wide — a scan must report to the dialog that started
	// it, not to whichever connection asked most recently.
	scan, v := cn.srv.scans.Begin(admit, func(d wifiscan.Done) {
		cn.srv.hub.Send(cn.c, "wifiscan:done", map[string]any{
			"scanId": d.ScanID, "reason": d.Reason, "rows": d.Rows,
			"sampleCount": d.SampleCount, "truncated": d.Truncated,
		})
	})
	if !v.OK {
		extra := map[string]any{}
		if v.Message != "" {
			extra["message"] = v.Message
		}
		if v.Iface != "" {
			extra["iface"] = v.Iface
		}
		if v.HasRetryAt {
			extra["retryAt"] = v.RetryAt
		}
		cn.scanErr(v.Code, extra)
		return
	}

	cn.srv.hub.Send(cn.c, "wifiscan:state", map[string]any{
		"scanning": true, "scanId": scan.ID, "iface": scan.Iface,
		"durationSec": scan.DurationSec, "startedAt": scan.StartedAt,
		"endsAt": scan.EndsAt, "currentChannelMhz": currentChannel, "rows": []any{},
	})
	log.Printf("[%s][wifiscan] %s for %ds — clients on that radio will drop",
		cn.routerLabel(), scan.Iface, scan.DurationSec)

	go cn.srv.runScan(scan, cn)
}

// wifiscanStop aborts whatever this router is running.
//
// GATED, unlike a read: stopping is not obviously harmful, but a scan someone
// else started is not this caller's to end — and the live handler gates it for
// that reason. It is NOT gated on the scan's owner, because an operator who can
// scan a router can also stop a colleague's scan on it, which is the useful
// behaviour when a radio has been off the air too long.
func (cn *conn) wifiscanStop(json.RawMessage) {
	if cn.routerID == "" {
		return
	}
	if !cn.mayScan() {
		cn.scanErr("denied", nil)
		return
	}
	cn.srv.scans.AbortAllForRouter(cn.routerID)
}

// scannableInterfaces asks the wireless collector for the radios that can be
// scanned, and how many clients each would drop.
//
// NIL WHEN THE COLLECTOR HAS NOT READ YET, and an empty slice when it has read
// and found none. The guard distinguishes them: not-yet-known answers
// "unavailable" (try again in a moment) where a known-empty answers
// "no-such-interface" (this router has nothing to scan). Collapsing the two
// would tell an operator their radios do not exist during the first seconds
// after a page loads.
func (cn *conn) scannableInterfaces() []wifiscan.Scannable {
	if cn.rsession == nil || cn.rsession.Wireless() == nil {
		return nil
	}
	// THE WIRELESS COLLECTOR, not the Wifi one. `faOpenBtn` is on the Wireless
	// page and `ws.go` resumes THIS collector for it; reading the Wifi
	// collector's copy meant the catalogue was empty unless the operator had
	// visited the `wifi` page first, so the button never appeared. Live reads
	// the same collector (`src/collectors/wireless.js:391`).
	cat, clients := cn.rsession.Wireless().ScanCatalogue()
	if cat == nil {
		return nil
	}
	return wifiscan.ScannableInterfaces(cat, clients)
}

// admitInterfaces is the same catalogue in the shape the guard reads.
func (cn *conn) admitInterfaces() ([]wifiscan.Interface, bool) {
	if cn.rsession == nil || cn.rsession.Wireless() == nil {
		return nil, false
	}
	cat, clients := cn.rsession.Wireless().ScanCatalogue()
	if cat == nil {
		return nil, false
	}
	_ = clients
	out := make([]wifiscan.Interface, 0, len(cat))
	for _, c := range cat {
		out = append(out, wifiscan.Interface{
			Name: c.Name, ID: c.ID, Master: c.Master, CapsmanManaged: c.CapsmanManaged,
		})
	}
	return out, true
}

func (cn *conn) routerLabel() string {
	if cn.rsession != nil && cn.rsession.Label != "" {
		return cn.rsession.Label
	}
	return cn.routerID
}

// runScan drives one admitted scan and relays its events to the connection that
// started it.
//
// EVENTS GO TO THE ORIGINATING CONNECTION, not to the router's room. A scan is
// one operator's action on one radio, and the dialog that shows it is theirs;
// broadcasting freeze-frames to every viewer of the Wireless page would put a
// chart nobody opened in front of them four times a second.
func (s *Server) runScan(scan *wifiscan.Scan, cn *conn) {
	wifiscan.Run(s.scans, scan, scanConn{cn.rsession}, scanEmitter{srv: s, cn: cn})
}

// scanConn adapts a router session to what the runner needs.
type scanConn struct{ rs *session.Session }

func (c scanConn) StreamUntilDone(cmd routeros.Cmd, onRow func(routeros.Reply), onDone func()) (func(), error) {
	return c.rs.StreamUntilDone(cmd, onRow, onDone)
}
func (c scanConn) Connected() bool { return c.rs != nil && c.rs.Connected() }

type scanEmitter struct {
	srv *Server
	cn  *conn
}

func (e scanEmitter) Rows(scanID string, rows []wifiscan.Row, truncated bool) {
	e.srv.hub.Send(e.cn.c, "wifiscan:rows", map[string]any{
		"scanId": scanID, "rows": rows, "truncated": truncated,
	})
}

func (e scanEmitter) Error(scanID, code, message string) {
	e.srv.hub.Send(e.cn.c, "wifiscan:error", map[string]any{
		"scanId": scanID, "code": code, "message": message,
	})
}

// currentChannelMhz reads where the radio is right now.
//
// `/interface/wifi/monitor =once=` answers something like "5180/ax/eeCe"; only
// the leading number is wanted. Returns nil when it cannot be read, and nil is a
// real value in the payload — the dialog draws no marker rather than a marker in
// the wrong place.
func (cn *conn) currentChannelMhz(iface string) *int {
	if cn.rsession == nil || iface == "" {
		return nil
	}
	rows, err := cn.rsession.Exec(routeros.Cmd{
		Path: "/interface/wifi/monitor",
		Args: []string{"=numbers=" + iface, "=once="},
	})
	if err != nil || len(rows) == 0 {
		return nil
	}
	raw := rows[0]["channel"]
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return nil
	}
	return &n
}
