package server

// The Packet Sniffer page: `/tool/sniffer` on the selected router, and the pcap
// the operator takes away from it.
//
// ── IT IS THE FIFTH TOOL, ON THE SAME LIFECYCLE AS THE OTHER FOUR ───────────
//
// `startTool` in tools.go is what gives it one run at a time per connection, the
// session pinned to the router the capture was asked of, a quit channel that
// `releaseRouter` closes on a router switch or a closed socket, and the work off
// the read loop. A capture runs for minutes, so every one of those matters, and
// the busy flag being shared with torch and the terminal is right rather than
// incidental: two of them are two channels on one router.
//
// ── WHAT IS DIFFERENT: THE ROUTER HOLDS THE STATE, NOT THE STREAM ───────────
//
// The other four diagnostics are one command that streams its own rows and ends.
// The sniffer is configured, started, and then simply on: packets pile up in the
// router's memory and four ordinary `print`s say what it has. So there is no
// `StreamUntilDone` here and no `.section` to delimit; there is a poll every
// snifferTick, and each poll is a whole frame rather than an increment.
//
// A POLL IS FOUR READS, and they are sequential on one channel. The state read
// is the cheapest of the four and it is the reason `running` on a frame is the
// ROUTER's answer rather than this app's belief about what it started.
//
// ── WRITE ACCESS, AUDITED, AND THE DEVICE MAY STILL REFUSE ──────────────────
//
// Starting a capture writes the filters to the router and turns the sniffer on,
// so it needs write access on `tools-sniffer` and the run is audited - the run
// and a refusal both, as torch and the bandwidth test are.
//
// On top of that there is a gate this app does not own: `/system/device-mode`
// has a `sniffer` flag, and a device whose flag is false refuses the tool
// outright ("not allowed by device-mode"), which can only be changed at the
// device with the reset button. That is read before the run and reported in
// `tools:caps`, so the page says the device will not allow it rather than
// offering a button that always fails.

import (
	"bytes"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mikrodash/internal/audit"
	"mikrodash/internal/backups"
	"mikrodash/internal/diag"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/session"
)

// ToolsSnifferPayload is `tools:sniffer`, shaped as ToolsPingPayload is.
type ToolsSnifferPayload struct {
	Result *diag.SnifferResult `json:"result"`
	// Code is empty on success, and otherwise one of the shared tool codes
	// (denied, unavailable, busy, failed, stopped) or one of this page's two:
	// `filter`, a request the router would refuse, and `device-mode`, a device
	// whose sniffer is switched off.
	Code    string `json:"code"`
	Message string `json:"message"`
	Done    bool   `json:"done"`
}

type toolsSnifferReq struct {
	Interface  string `json:"interface"`
	IPProtocol string `json:"ipProtocol"`
	Port       string `json:"port"`
	Address    string `json:"address"`
	Direction  string `json:"direction"`
}

func (r toolsSnifferReq) filters() diag.SnifferFilters {
	return diag.SnifferFilters{Interface: r.Interface, IPProtocol: r.IPProtocol,
		Port: r.Port, Address: r.Address, Direction: r.Direction}
}

// snifferTick is how often a running capture's tables are re-read. Two seconds:
// fast enough that a table being watched moves, slow enough that four reads per
// poll are not most of what the router is doing. A variable so a test need not
// wait it out.
var snifferTick = 2 * time.Second

// snifferCmdTimeout bounds one of the sniffer's commands. None of them is a
// stream, and `internal/routeros` says of a zero timeout that it is "correct for
// a stream and wrong for everything else".
const snifferCmdTimeout = 20 * time.Second

// toolsSniffer answers `tools:sniffer` from the Packet Sniffer page.
func (cn *conn) toolsSniffer(raw json.RawMessage) {
	var req toolsSnifferReq
	_ = json.Unmarshal(raw, &req)
	// TAKEN ON THE READ LOOP, because the run needs the write queue, the rate
	// limiter and an audit row, and only the loop may read cn.sess, cn.routerID
	// and the session the recorder is built from. The Terminal takes its
	// recorder the same way, for the same reason.
	sc := cn.scope()
	rec := cn.recorder()
	cn.startTool("tools-sniffer", "write",
		func(code string) {
			if code == "denied" {
				cn.recorder().Denied(audit.Event{Action: "tools.sniffer", TargetType: "interface",
					TargetName: req.Interface, RouterID: cn.routerID})
			}
			EvToolsSniffer.Send(cn.srv.hub, cn.c, ToolsSnifferPayload{Code: code, Done: true})
		},
		func(rs *session.Session, quit <-chan struct{}) {
			res, code, msg := cn.runSniffer(sc, rec, rs, req, quit, func(r *diag.SnifferResult) {
				EvToolsSniffer.Send(cn.srv.hub, cn.c, ToolsSnifferPayload{Result: r})
			})
			EvToolsSniffer.Send(cn.srv.hub, cn.c, ToolsSnifferPayload{Result: res, Code: code, Message: msg, Done: true})
		})
}

// runSniffer configures the sniffer, starts it, and polls its tables until the
// operator stops it or the cap is reached.
//
// THE SNIFFER IS STOPPED ON EVERY PATH OUT, and the final frame is read AFTER
// the stop, so what the operator is left looking at is the finished capture
// rather than a snapshot taken while it was still filling. A capture left
// running is not harmless: it costs the router memory and CPU until somebody
// notices, and nothing on any page would say it was on.
// The receiver is used for ONE thing - `inWriteQueueOn`, which reads the
// server's write limiter and nothing on the connection - so everything about
// the router, the viewer and the audit trail comes in as a parameter, read on
// the loop. See ws.go's header on what a goroutine may not read.
func (cn *conn) runSniffer(sc connScope, rec *audit.Recorder, rs *session.Session, req toolsSnifferReq,
	quit <-chan struct{}, progress func(*diag.SnifferResult)) (*diag.SnifferResult, string, string) {

	args, err := diag.SnifferSetArgs(req.filters())
	if err != nil {
		return nil, "filter", err.Error()
	}
	// THE DEVICE'S OWN GATE, read before anything is written. A device with the
	// sniffer switched off answers the start with a trap, and "not allowed by
	// device-mode" needs explaining rather than repeating.
	if allowed, err := snifferAllowedOn(rs); err != nil {
		return nil, "failed", safe.Message(err.Error())
	} else if !allowed {
		return nil, "device-mode", ""
	}
	// The interface must be one the router has, for torch's reason: RouterOS
	// answers a wrong name with "input does not match any value of interface",
	// which is true and unhelpful.
	if req.Interface != "" {
		names, err := interfaceNames(rs)
		if err != nil {
			return nil, "failed", safe.Message(err.Error())
		}
		known := false
		for _, n := range names {
			if n == req.Interface {
				known = true
				break
			}
		}
		if !known {
			return nil, "filter", "this router has no interface of that name"
		}
	}

	// BEFORE THE COMMAND IS SENT, as torch's and the bandwidth test's are: a run
	// that starts and then fails to be recorded still configured the router.
	on := req.Interface
	if on == "" {
		on = "all interfaces"
	}
	rec.Record(audit.Event{
		Action: "tools.sniffer", TargetType: "interface", TargetName: on, RouterID: sc.routerID,
		Note: "captures packets into the router's memory and rewrites its sniffer filters",
		Extra: []audit.KV{{Key: "protocol", Value: req.IPProtocol}, {Key: "port", Value: req.Port},
			{Key: "address", Value: req.Address}, {Key: "direction", Value: req.Direction}},
	})

	if err := cn.inWriteQueueOn(sc, func() error {
		if _, e := rs.Exec(routeros.Cmd{Path: "/tool/sniffer/set", Args: args, Timeout: snifferCmdTimeout}); e != nil {
			return e
		}
		_, e := rs.Exec(routeros.Cmd{Path: "/tool/sniffer/start", Timeout: snifferCmdTimeout})
		return e
	}); err != nil {
		return diagFailureOf(err)
	}

	stopCapture := func() {
		if e := rs.InWriteQueue(func() error {
			_, err := rs.Exec(routeros.Cmd{Path: "/tool/sniffer/stop", Timeout: snifferCmdTimeout})
			return err
		}); e != nil {
			log.Printf("[sniffer] stopping the capture on %s: %v", sc.routerID, e)
		}
	}

	tick := time.NewTicker(snifferTick)
	defer tick.Stop()
	// THE SAME CAP A CONTINUOUS PING OR TORCH HAS, and for the same reason: the
	// tab nobody closes.
	capAt := time.NewTimer(diag.ContinuousMax)
	defer capAt.Stop()

	for running := true; running; {
		select {
		case <-quit:
			running = false
		case <-capAt.C:
			running = false
		case <-tick.C:
			r, err := readSniffer(rs)
			if err != nil {
				stopCapture()
				return diagFailureOf(err)
			}
			progress(&r)
		}
	}
	stopCapture()
	r, err := readSniffer(rs)
	if err != nil {
		return diagFailureOf(err)
	}
	return &r, codeStopped, ""
}

// diagFailureOf is diagFailure's two returns as the three a runner gives back.
func diagFailureOf(err error) (*diag.SnifferResult, string, string) {
	code, msg := diagFailure(err)
	return nil, code, msg
}

// readSniffer is one poll: the state, the packets, the protocol shares and the
// hosts, folded into one frame.
func readSniffer(rs *session.Session) (diag.SnifferResult, error) {
	read := func(path string, args ...string) ([]routeros.Reply, error) {
		return rs.Exec(routeros.Cmd{Path: path, Args: args, Timeout: snifferCmdTimeout})
	}
	state, err := read("/tool/sniffer/print", "=.proplist=running")
	if err != nil {
		return diag.SnifferResult{}, err
	}
	packets, err := read("/tool/sniffer/packet/print", "=.proplist="+diag.SnifferPacketProps)
	if err != nil {
		return diag.SnifferResult{}, err
	}
	protocols, err := read("/tool/sniffer/protocol/print")
	if err != nil {
		return diag.SnifferResult{}, err
	}
	hosts, err := read("/tool/sniffer/host/print")
	if err != nil {
		return diag.SnifferResult{}, err
	}
	running := len(state) > 0 && state[0]["running"] == "true"
	return diag.FoldSniffer(running, packets, protocols, hosts), nil
}

// snifferAllowedOn reads the device's own sniffer flag.
//
// TRUE WHEN THE MENU CANNOT BE READ, which is the safe direction here rather
// than the cautious one: a device with no `/system/device-mode` at all would
// otherwise be told its sniffer is switched off, which is a confident wrong
// answer about a tool that would have worked. A device that really does refuse
// says so in the trap the start then returns.
func snifferAllowedOn(rs *session.Session) (bool, error) {
	rows, err := rs.Exec(routeros.Cmd{Path: "/system/device-mode/print",
		Args: []string{"=.proplist=sniffer"}, Timeout: snifferCmdTimeout})
	if err != nil {
		var trap *routeros.Trap
		if errors.As(err, &trap) {
			return true, nil
		}
		return false, err
	}
	if len(rows) == 0 {
		return true, nil
	}
	// ABSENT IS ALLOWED, present is what it says. A build that does not gate the
	// sniffer does not carry the word at all.
	v, ok := rows[0]["sniffer"]
	return !ok || v == "true", nil
}

// ── THE PCAP EXPORT ─────────────────────────────────────────────────────────
//
// `GET /api/tools/sniffer/pcap?routerId=<id>`: the router writes what it has
// captured to a file, this reads the file off it, and the file is removed again.
//
// ── WHY IT IS AN HTTP ROUTE AND NOT AN EVENT ────────────────────────────────
//
// The bytes are binary. Everything on the socket goes through `encoding/json`,
// which replaces invalid UTF-8 with U+FFFD - `internal/backups`' header has the
// same note about it - so a capture sent that way would arrive the right length
// and the wrong bytes. A route hands the browser the file.
//
// ── HOW THE BYTES COME OFF THE ROUTER ───────────────────────────────────────
//
// `internal/backups.ReadRouterFile`, unchanged. Its package comment records what
// was tried and does not work: `/export` returns an empty array, `/file/print`'s
// `contents` is populated only for a few KB, and `/tool/fetch upload=yes`
// refuses anything but [s]ftp. `/file/read` in 32768-byte chunks is what works,
// and a short read is an error rather than a shorter file.
//
// ── AND THE FILE IS REMOVED AGAIN ───────────────────────────────────────────
//
// On every path, including the ones that fail: a capture left on an operator's
// flash is this app's litter, and one per download adds up on exactly the
// devices somebody is debugging.

const snifferPcapPath = "/api/tools/sniffer/pcap"

func (s *Server) registerSnifferPcap(mux *http.ServeMux) {
	mux.HandleFunc("GET "+snifferPcapPath, s.snifferPcap)
}

// mayExportCapture is `tools-sniffer` at WRITE on this router, both gates - the
// same permission that starts a capture, because the export writes a file to the
// router's storage and removes it again.
func (s *Server) mayExportCapture(sess *Session, routerID string) bool {
	if sess == nil {
		return false
	}
	if sess.AuthMode == "none" {
		return true
	}
	if !sess.CanPage("tools-sniffer", "write", routerID) {
		return false
	}
	if !s.rbac.Available() {
		return true // the documented install-wide gap, reported at startup
	}
	ok, err := s.rbac.CanPage(s.userIDFor(sess.Username), "tools-sniffer", "write", routerID)
	if err != nil {
		log.Printf("[rbac] export capture on %s: %v", routerID, err)
		return false
	}
	return ok
}

func (s *Server) snifferPcap(w http.ResponseWriter, r *http.Request) {
	sess, err := s.auth.Validate(r.Header.Get("Cookie"))
	if err != nil || sess == nil {
		writeJSONErr(w, http.StatusUnauthorized, "not signed in")
		return
	}
	routerID := r.URL.Query().Get("routerId")
	if !s.mayExportCapture(sess, routerID) {
		s.httpRecorder(r, sess).Denied(audit.Event{Action: "tools.sniffer.export",
			TargetType: "file", RouterID: routerID})
		writeJSONErr(w, http.StatusForbidden, "Not permitted")
		return
	}

	// NAMED FOR THIS DOWNLOAD, not a fixed name: two operators exporting at once
	// would otherwise save over each other's file and read each other's bytes.
	name := "mikrodash-sniffer-" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	var body []byte
	err = s.inRouterWriteQueueWith(routerID, func(sn *session.Session) error {
		var rerr error
		body, rerr = saveAndReadCapture(sn, name)
		return rerr
	})
	if err != nil {
		log.Printf("[sniffer] export from %s: %v", routerID, err)
		writeJSONErr(w, http.StatusBadGateway, safe.Message(err.Error()))
		return
	}
	if len(body) == 0 {
		writeJSONErr(w, http.StatusNotFound, "Nothing has been captured to export.")
		return
	}

	file := backups.SlugFor(routerLabelFor(s, routerID)) + "-capture" + captureExt(body)
	s.httpRecorder(r, sess).Record(audit.Event{
		Action: "tools.sniffer.export", TargetType: "file", Scope: "router",
		RouterID: routerID, TargetName: file,
		Extra: []audit.KV{{Key: "bytes", Value: len(body)}},
		Note:  "downloaded the captured packets and removed the file from the router",
	})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	_, _ = w.Write(body)
}

// saveAndReadCapture has the router write its captured packets to `name`, reads
// them back, and removes the file whatever happened.
func saveAndReadCapture(sn *session.Session, name string) ([]byte, error) {
	exec := func(path string, args ...string) ([]routeros.Reply, error) {
		return sn.Exec(routeros.Cmd{Path: path, Args: args, Timeout: snifferCmdTimeout})
	}
	if _, err := exec("/tool/sniffer/save", "=file-name="+name); err != nil {
		return nil, err
	}
	defer func() {
		if _, err := exec("/file/remove", "=numbers="+name); err != nil {
			log.Printf("[sniffer] removing %s from the router: %v", name, err)
		}
	}()

	rows, err := exec("/file/print", "?name="+name, "=.proplist=size")
	if err != nil {
		return nil, err
	}
	// NO FILE IS NOT A FAILURE. `save` with nothing captured writes nothing, and
	// the caller answers that as "nothing to export" rather than as a fault.
	if len(rows) == 0 {
		return nil, nil
	}
	size, _ := strconv.Atoi(strings.ReplaceAll(rows[0]["size"], " ", ""))
	if size == 0 {
		return nil, nil
	}
	return backups.ReadRouterFile(func(cmd string, args ...string) ([]map[string]string, error) {
		replies, err := exec(cmd, args...)
		out := make([]map[string]string, 0, len(replies))
		for _, r := range replies {
			out = append(out, map[string]string(r))
		}
		return out, err
	}, name, size)
}

// captureExt names the download from the bytes that actually came back.
//
// ROUTEROS 7.20 AND LATER WRITE PCAPNG whatever the file is called, and the
// documentation says so in a note rather than in the `save` command's own
// description; measured on a 7.24.4 CHR, the first four bytes were 0a 0d 0d 0a.
// Naming the download `.pcap` regardless would hand Wireshark a file whose name
// disagrees with its contents, and give anybody scripting around it a wrong
// answer. So the magic is read rather than assumed, both formats are recognised,
// and something neither is called `.cap` - the neutral name Wireshark also
// opens - instead of being asserted to be one of them.
func captureExt(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0x0a, 0x0d, 0x0d, 0x0a}):
		return ".pcapng"
	case bytes.HasPrefix(b, []byte{0xd4, 0xc3, 0xb2, 0xa1}), // pcap, microseconds
		bytes.HasPrefix(b, []byte{0xa1, 0xb2, 0xc3, 0xd4}),
		bytes.HasPrefix(b, []byte{0x4d, 0x3c, 0xb2, 0xa1}), // pcap, nanoseconds
		bytes.HasPrefix(b, []byte{0xa1, 0xb2, 0x3c, 0x4d}):
		return ".pcap"
	}
	return ".cap"
}
