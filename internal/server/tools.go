package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/audit"
	"mikrodash/internal/diag"
	"mikrodash/internal/routeros"
	"mikrodash/internal/safe"
	"mikrodash/internal/session"
)

// The Tools page's diagnostics (slice 8 of the MikroMCP parity work).
//
// ── WHO MAY RUN WHICH, DECIDED BY THE OPERATOR ──────────────────────────────
//
// Read access to Tools runs ping and traceroute: they send a few probes and
// change nothing. Torch and a bandwidth test load the router or a link, so they
// need write access. The assistant's tools follow the page exactly: the same
// permission, the same bounds, the same code — `runPing` below is the one
// implementation both call.
//
// ── ONE RUN AT A TIME PER CONNECTION ────────────────────────────────────────
//
// A run holds an API channel on the router for its whole length, and the
// bottleneck this app is organised around is channels on the router. A second
// click while one is running is answered "busy" rather than queued: a queue
// would run probes the operator has stopped waiting for.
//
// ── AUDITED WHEN IT NEEDS WRITE ACCESS ──────────────────────────────────────
//
// A ping or a traceroute changes nothing, and the audit trail records changes
// and refusals to change. Torch and bandwidth test load the router or a link,
// need write access, and are audited — the run and a refusal both.
//
// ── LIVE: EACH ROW AS IT ARRIVES, THEN THE RESULT ───────────────────────────
//
// A run is streamed rather than sent with Exec, so the page shows each ping
// reply, the traceroute's table growing, each second of torch and of a
// bandwidth test while the run is still going. Every row is folded with the
// rows before it by the same pure Fold* the finished run uses, and sent on the
// tool's own event with Done false, at most every progressEvery. The last frame
// has Done true, and is exactly what the page drew before this was live. The
// bounds are unchanged: the same command, and its Timeout stops it on the
// router (see (*routeros.Client).StreamUntilDone).

// ToolsPingPayload is `tools:ping`: a run in progress, a finished run, or why
// there is none.
type ToolsPingPayload struct {
	// Result is nil when Code is set.
	Result *diag.PingResult `json:"result"`
	// Code is empty on success, and otherwise one of: denied, unavailable,
	// busy, address, failed.
	Code string `json:"code"`
	// Message is the router's own words for a failed run, sanitised.
	Message string `json:"message"`
	// Done is false on a progress frame — Result is the run so far and Code is
	// empty — and true on the one frame that ends the run.
	Done bool `json:"done"`
}

// ToolsTraceroutePayload is `tools:traceroute`, shaped as ToolsPingPayload is.
type ToolsTraceroutePayload struct {
	Result  *diag.TracerouteResult `json:"result"`
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
	Done    bool                   `json:"done"`
}

type toolsPingReq struct {
	Address string `json:"address"`
	Count   int    `json:"count"`
}

type toolsTracerouteReq struct {
	Address string `json:"address"`
	MaxHops int    `json:"maxHops"`
}

// startTool is what every diagnostic handler does before its run: a router, the
// page permission the tool needs, and this connection's one run slot. It
// answers `refuse` with the code when any of them is missing, and otherwise
// runs `work` off the read loop, freeing the slot when it returns.
//
// PINNED: `work` gets the router selected when the run was asked for. The page
// drops a result that lands after a router switch; the goroutine does not read
// `cn.rsession` again, which the read loop writes.
//
// `quit` closes when the run is no longer wanted: the operator switched router
// or the socket closed (releaseRouter runs on both). The run then stops its
// stream, which cancels the command on the router, and sends nothing more.
//
// OFF THE READ LOOP because a run lasts up to thirty-five seconds, and this
// socket's page focus, blur and every other message would wait behind it.
func (cn *conn) startTool(access string, refuse func(code string), work func(rs *session.Session, quit <-chan struct{})) {
	if cn.routerID == "" || cn.rsession == nil {
		refuse("unavailable")
		return
	}
	if !cn.canPage("tools", access) {
		refuse("denied")
		return
	}
	if !cn.toolBusy.CompareAndSwap(false, true) {
		refuse("busy")
		return
	}
	rs := cn.rsession
	quit := make(chan struct{})
	cn.toolMu.Lock()
	cn.toolQuit = quit
	cn.toolMu.Unlock()
	go func() {
		defer cn.toolBusy.Store(false)
		work(rs, quit)
	}()
}

// stopTool ends this connection's page run, if one is going: releaseRouter
// calls it on a router switch, a revoked grant and the socket closing.
func (cn *conn) stopTool() {
	cn.toolMu.Lock()
	defer cn.toolMu.Unlock()
	if cn.toolQuit != nil {
		close(cn.toolQuit)
		cn.toolQuit = nil
	}
}

// toolsPing answers `tools:ping` from the Tools page.
func (cn *conn) toolsPing(raw json.RawMessage) {
	var req toolsPingReq
	_ = json.Unmarshal(raw, &req)
	cn.startTool("read",
		func(code string) { EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Code: code, Done: true}) },
		func(rs *session.Session, quit <-chan struct{}) {
			res, code, msg := runPing(rs, req.Address, req.Count, quit, func(p *diag.PingResult) {
				EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Result: p})
			})
			if code != codeStopped {
				EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Result: res, Code: code, Message: msg, Done: true})
			}
		})
}

// toolsTraceroute answers `tools:traceroute` from the Tools page.
func (cn *conn) toolsTraceroute(raw json.RawMessage) {
	var req toolsTracerouteReq
	_ = json.Unmarshal(raw, &req)
	cn.startTool("read",
		func(code string) {
			EvToolsTraceroute.Send(cn.srv.hub, cn.c, ToolsTraceroutePayload{Code: code, Done: true})
		},
		func(rs *session.Session, quit <-chan struct{}) {
			res, code, msg := runTraceroute(rs, req.Address, req.MaxHops, quit, func(r *diag.TracerouteResult) {
				EvToolsTraceroute.Send(cn.srv.hub, cn.c, ToolsTraceroutePayload{Result: r})
			})
			if code != codeStopped {
				EvToolsTraceroute.Send(cn.srv.hub, cn.c, ToolsTraceroutePayload{Result: res, Code: code, Message: msg, Done: true})
			}
		})
}

// runPing runs one bounded ping on a router and folds its reply.
//
// Returns the result, or a code and the router's own message. The caller has
// already checked permission; this checks only what the request itself can get
// wrong. `progress`, when not nil, is handed the run so far as replies arrive;
// `quit`, when not nil, ends the run early (see streamDiag).
func runPing(rs diagStream, address string, count int, quit <-chan struct{}, progress func(*diag.PingResult)) (*diag.PingResult, string, string) {
	cmd, err := diag.PingCommand(address, count)
	if err != nil {
		return nil, "address", err.Error()
	}
	fold := func(rows []routeros.Reply) *diag.PingResult {
		r := diag.FoldPing(address, rows)
		return &r
	}
	rows, code, msg := streamDiag(rs, cmd, quit, nil, report(progress, fold))
	if code != "" {
		return nil, code, msg
	}
	return fold(rows), "", ""
}

// runTraceroute runs one bounded traceroute on a router and folds its reply.
// Progress is the table as of the last section complete, so a half-arrived
// section is never drawn.
func runTraceroute(rs diagStream, address string, maxHops int, quit <-chan struct{}, progress func(*diag.TracerouteResult)) (*diag.TracerouteResult, string, string) {
	cmd, err := diag.TracerouteCommand(address, maxHops)
	if err != nil {
		return nil, "address", err.Error()
	}
	fold := func(rows []routeros.Reply) *diag.TracerouteResult {
		r := diag.FoldTraceroute(address, rows)
		return &r
	}
	rows, code, msg := streamDiag(rs, cmd, quit, diag.CompleteSections, report(progress, fold))
	if code != "" {
		return nil, code, msg
	}
	return fold(rows), "", ""
}

// report adapts a tool's progress callback to the rows streamDiag hands out,
// through the same fold as the finished run. Nil stays nil: the assistant's
// tools ask for no progress.
func report[T any](progress func(*T), fold func([]routeros.Reply) *T) func([]routeros.Reply) {
	if progress == nil {
		return nil
	}
	return func(rows []routeros.Reply) { progress(fold(rows)) }
}

// diagStream is the part of a router connection a diagnostic needs:
// *session.Session, or a test's fake.
type diagStream interface {
	StreamUntilDone(cmd routeros.Cmd, onRow func(routeros.Reply), onDone func(error)) (func(), error)
}

// codeStopped is a run ended by `quit`. Never sent: nobody is waiting for it.
const codeStopped = "stopped"

// progressEvery is the most often a run's progress is sent. Four frames a second
// is as fast as a person reads a table changing, and a ping's one reply a second
// still lands on its own. A variable so a test need not wait it out.
var progressEvery = 250 * time.Millisecond

// streamDiag sends one diagnostic command and returns its rows, or "failed" with
// the reason, or codeStopped when `quit` closed first.
//
// ── PROGRESS ────────────────────────────────────────────────────────────────
//
// `progress`, when not nil, is called from this goroutine with the rows so far —
// cut by `complete`, when not nil, to the part that can be folded — at most once
// per progressEvery, and only when that part has grown. The final rows are
// returned, never handed to `progress`: the caller sends them as the result.
//
// ── HOW THE STREAM ENDS, AND THAT IT DOES ───────────────────────────────────
//
//   - By itself, at `!done` or a trap: onDone reports it, and no /cancel is
//     written to a command that has already finished.
//   - Past cmd.Timeout: the client cancels it on the router and onDone reports a
//     timeout. The bound is the client's, the same one Exec had.
//   - `quit`: stop() cancels it on the router and returns once it has ended —
//     or, if the router will not end it, once the connection it was on has been
//     given up (see (*routeros.Client).StreamUntilDone).
//
// Every path returns, so the caller's run slot and this goroutine go with it.
//
// A name that does not resolve, or an API user without the `test` policy, is a
// trap with the router's own words in it — which is what the operator needs to
// read, without this client's prefix on it. SANITISED, as every outbound error
// is: a transport failure carries the router's address.
func streamDiag(rs diagStream, cmd routeros.Cmd, quit <-chan struct{}, complete func([]routeros.Reply) []routeros.Reply,
	progress func([]routeros.Reply)) ([]routeros.Reply, string, string) {
	var mu sync.Mutex
	var rows []routeros.Reply
	grew := make(chan struct{}, 1)
	ended := make(chan error, 1)
	stop, err := rs.StreamUntilDone(cmd,
		func(r routeros.Reply) {
			mu.Lock()
			rows = append(rows, r)
			mu.Unlock()
			select {
			case grew <- struct{}{}:
			default:
			}
		},
		func(err error) { ended <- err })
	if err != nil {
		code, msg := diagFailure(err)
		return nil, code, msg
	}
	// A full-slice expression, so a later append cannot write under a frame
	// still being folded.
	snapshot := func() []routeros.Reply {
		mu.Lock()
		defer mu.Unlock()
		return rows[:len(rows):len(rows)]
	}

	tick := time.NewTicker(progressEvery)
	defer tick.Stop()
	dirty, reported := false, 0
	for {
		select {
		case err := <-ended:
			if err != nil {
				code, msg := diagFailure(err)
				return nil, code, msg
			}
			return snapshot(), "", ""
		case <-quit:
			stop()
			return nil, codeStopped, ""
		case <-grew:
			dirty = true
		case <-tick.C:
			if !dirty || progress == nil {
				continue
			}
			dirty = false
			part := snapshot()
			if complete != nil {
				part = complete(part)
			}
			if len(part) > reported {
				reported = len(part)
				progress(part)
			}
		}
	}
}

// diagFailure is the page's words for a run that did not finish.
func diagFailure(err error) (string, string) {
	var trap *routeros.Trap
	if errors.As(err, &trap) {
		return "failed", "the router said: " + safe.Message(trap.Message)
	}
	return "failed", safe.Message(err.Error())
}

// diagRunners answers each diagnostic tool, keyed by its `Diagnostic` name.
// Each returns the result to hand the model, or the sentence explaining why
// there is none. TestEveryDiagnosticToolHasARunner holds this map to the
// catalogue in both directions.
var diagRunners = map[string]func(rs *session.Session, args []byte) (any, string){
	"ping": func(rs *session.Session, args []byte) (any, string) {
		var req toolsPingReq
		if json.Unmarshal(args, &req) != nil {
			return nil, "The arguments could not be read. Pass an object with `address`, and optionally `count`."
		}
		res, code, msg := runPing(rs, req.Address, req.Count, nil, nil)
		if code != "" {
			return nil, "The ping did not run: " + msg
		}
		return res, ""
	},
	"traceroute": func(rs *session.Session, args []byte) (any, string) {
		var req toolsTracerouteReq
		if json.Unmarshal(args, &req) != nil {
			return nil, "The arguments could not be read. Pass an object with `address`, and optionally `maxHops`."
		}
		res, code, msg := runTraceroute(rs, req.Address, req.MaxHops, nil, nil)
		if code != "" {
			return nil, "The traceroute did not run: " + msg
		}
		return res, ""
	},
}

// runDiagTool runs a diagnostic the assistant asked for.
//
// The arguments are the page's own request, bounded by internal/diag exactly as
// the page's are, so the model can ask for nothing a viewer of the page could
// not.
func (cn *conn) runDiagTool(sc connScope, t aitools.Tool, tc aiprovider.ToolCall) string {
	run, ok := diagRunners[t.Diagnostic]
	if !ok {
		return "That tool is not available on this build."
	}
	// THE TOOL'S OWN ACCESS, which for a diagnostic is not always read: the
	// caller checked read, and torch and bandwidth test will need write.
	if !cn.canPageIn(sc, t.Page, t.Access) {
		return "You do not have access to that tool, so nothing was run."
	}
	if sc.rs == nil {
		return "No device is selected, so nothing was run."
	}
	// THE SAME ONE-AT-A-TIME RULE as the page, and on the same flag: the page
	// and the assistant on one connection are one person asking twice.
	if !cn.toolBusy.CompareAndSwap(false, true) {
		return "Another diagnostic is still running for this operator; try again when it has finished."
	}
	defer cn.toolBusy.Store(false)
	res, why := run(sc.rs, []byte(tc.Function.Arguments))
	if why != "" {
		return why
	}
	body, err := json.Marshal(struct {
		Tool   string `json:"tool"`
		Result any    `json:"result"`
	}{Tool: t.Name, Result: res})
	if err != nil {
		return "That result could not be encoded."
	}
	// WRAPPED like every tool result: reply hosts and the router's error words
	// are text from the network.
	return aicontext.Wrap(string(body))
}

// ToolsCapsPayload is `tools:caps`: what this viewer may run on the selected
// router, and the interfaces torch can watch.
//
// MayWrite is a SEPARATE field from the list, as the wifi scan's `permitted`
// is, and the write tools' Run buttons are drawn from it: a reader of the page
// still sees the interfaces, and no button that would only be refused.
type ToolsCapsPayload struct {
	MayWrite   bool     `json:"mayWrite"`
	Interfaces []string `json:"interfaces"`
}

// ToolsTorchPayload is `tools:torch`, shaped as ToolsPingPayload is.
type ToolsTorchPayload struct {
	Result  *diag.TorchResult `json:"result"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Done    bool              `json:"done"`
}

type toolsTorchReq struct {
	Interface string `json:"interface"`
	Seconds   int    `json:"seconds"`
}

// toolsCaps answers `tools:caps`.
func (cn *conn) toolsCaps() {
	out := ToolsCapsPayload{Interfaces: []string{}}
	if cn.routerID == "" || cn.rsession == nil || !cn.canPage("tools", "read") {
		EvToolsCaps.Send(cn.srv.hub, cn.c, out)
		return
	}
	out.MayWrite = cn.canPage("tools", "write")
	if names, err := interfaceNames(cn.rsession); err == nil {
		out.Interfaces = names
	}
	EvToolsCaps.Send(cn.srv.hub, cn.c, out)
}

// interfaceNames reads the router's interface names, and only those.
func interfaceNames(rs *session.Session) ([]string, error) {
	rows, err := rs.Exec(routeros.Cmd{Path: "/interface/print", Args: []string{"=.proplist=name"}})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r["name"] != "" {
			out = append(out, r["name"])
		}
	}
	return out, nil
}

// toolsTorch answers `tools:torch` from the Tools page.
//
// WRITE ACCESS, and AUDITED — both the run and a refusal — because torch loads
// the router's CPU for as long as it watches, and who did that when is what an
// audit trail is for.
func (cn *conn) toolsTorch(raw json.RawMessage) {
	var req toolsTorchReq
	_ = json.Unmarshal(raw, &req)
	cn.startTool("write",
		func(code string) {
			if code == "denied" {
				cn.recorder().Denied(audit.Event{Action: "tools.torch", TargetType: "interface",
					TargetName: req.Interface, RouterID: cn.routerID})
			}
			EvToolsTorch.Send(cn.srv.hub, cn.c, ToolsTorchPayload{Code: code, Done: true})
		},
		func(rs *session.Session, quit <-chan struct{}) {
			res, code, msg := cn.runTorch(rs, req.Interface, req.Seconds, "", quit, func(r *diag.TorchResult) {
				EvToolsTorch.Send(cn.srv.hub, cn.c, ToolsTorchPayload{Result: r})
			})
			if code != codeStopped {
				EvToolsTorch.Send(cn.srv.hub, cn.c, ToolsTorchPayload{Result: res, Code: code, Message: msg, Done: true})
			}
		})
}

// runTorch watches one interface for a bounded time and folds what it saw.
//
// The interface must be one the router has: RouterOS answers a wrong name with
// "input does not match any value of interface", which is true and unhelpful,
// and a name read fresh here makes the refusal say what was wrong. The audit
// row is written BEFORE the command is sent, as the wifi scan's is: a run that
// starts and then fails to be recorded still loaded the router.
//
// Progress is each complete second's flows, averaged over the seconds so far.
func (cn *conn) runTorch(rs *session.Session, iface string, seconds int, via string,
	quit <-chan struct{}, progress func(*diag.TorchResult)) (*diag.TorchResult, string, string) {
	cmd, err := diag.TorchCommand(iface, seconds)
	if err != nil {
		return nil, "interface", err.Error()
	}
	names, err := interfaceNames(rs)
	if err != nil {
		return nil, "failed", safe.Message(err.Error())
	}
	known := false
	for _, n := range names {
		if n == iface {
			known = true
			break
		}
	}
	if !known {
		return nil, "interface", "this router has no interface of that name"
	}
	ev := audit.Event{Action: "tools.torch", TargetType: "interface", TargetName: iface,
		RouterID: cn.routerID, Note: "watches the interface's traffic; loads the router's CPU while it runs"}
	if via != "" {
		ev.Extra = []audit.KV{{Key: "via", Value: via}}
	}
	cn.recorder().Record(ev)
	fold := func(rows []routeros.Reply) *diag.TorchResult {
		r := diag.FoldTorch(iface, rows)
		r.Seconds = diag.TorchSeconds(seconds)
		return &r
	}
	rows, code, msg := streamDiag(rs, cmd, quit, diag.CompleteSections, report(progress, fold))
	if code != "" {
		return nil, code, msg
	}
	return fold(rows), "", ""
}

// runTorchAction is the approved `torch` action: the page's run, for the
// page's fixed default duration, on the page's one run slot.
func (cn *conn) runTorchAction(iface string) writeOutcome {
	if !cn.toolBusy.CompareAndSwap(false, true) {
		return writeOutcome{Code: "busy"}
	}
	defer cn.toolBusy.Store(false)
	res, code, msg := cn.runTorch(cn.rsession, iface, diag.TorchDefaultSeconds, "agent", nil, nil)
	if code != "" {
		return writeOutcome{Code: code, Detail: map[string]any{"message": msg}}
	}
	return writeOutcome{Name: iface, Detail: map[string]any{"torch": res}}
}

// torchSummary is what the operator reads of an approved run, in the chat as the
// action's outcome: the totals, and the busiest flows one to a line.
//
// NOT WRAPPED in the untrusted block. This text is drawn for the operator — by
// the Markdown renderer, which builds text nodes and never parses HTML — and is
// not handed back to the model, so the block's preamble would be addressed to
// nobody and read by the one person it is not for.
func torchSummary(r *diag.TorchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Done: watched %s for %d s. Average rx %d bit/s, tx %d bit/s.",
		r.Interface, r.Seconds, r.TotalRxBps, r.TotalTxBps)
	if len(r.Flows) == 0 {
		b.WriteString(" No traffic was seen.")
		return b.String()
	}
	b.WriteString(" Busiest flows:\n")
	for i, f := range r.Flows {
		if i == 10 {
			break
		}
		fmt.Fprintf(&b, "\n- %s %s -> %s: rx %d bit/s, tx %d bit/s",
			f.Protocol, flowEnd(f.SrcAddr, f.SrcPort), flowEnd(f.DstAddr, f.DstPort), f.RxBps, f.TxBps)
	}
	return b.String()
}

// flowEnd is one end of a flow: an address, with its port when it has one.
func flowEnd(addr, port string) string {
	if port == "" {
		return addr
	}
	return addr + ":" + port
}

// ToolsBtestPayload is `tools:btest`, shaped as ToolsPingPayload is. A test that
// ran and failed (a refused login, an unreachable server) carries its Result as
// well as the "failed" code, so the page can say what RouterOS said.
type ToolsBtestPayload struct {
	Result  *diag.BtestResult `json:"result"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Done    bool              `json:"done"`
}

// toolsBtestReq is the page's request. The PASSWORD is in it, and goes from here
// into one sentence and nowhere else.
type toolsBtestReq struct {
	Address   string `json:"address"`
	User      string `json:"user"`
	Password  string `json:"password"`
	Seconds   int    `json:"seconds"`
	Protocol  string `json:"protocol"`
	Direction string `json:"direction"`
}

// toolsBtest answers `tools:btest` from the Tools page: write access, audited,
// as torch is.
func (cn *conn) toolsBtest(raw json.RawMessage) {
	var req toolsBtestReq
	_ = json.Unmarshal(raw, &req)
	cn.startTool("write",
		func(code string) {
			if code == "denied" {
				cn.recorder().Denied(btestAudit(cn.routerID, req, ""))
			}
			EvToolsBtest.Send(cn.srv.hub, cn.c, ToolsBtestPayload{Code: code, Done: true})
		},
		func(rs *session.Session, quit <-chan struct{}) {
			res, code, msg := cn.runBtest(rs, req, "", quit, func(r *diag.BtestResult) {
				EvToolsBtest.Send(cn.srv.hub, cn.c, ToolsBtestPayload{Result: r})
			})
			if code != codeStopped {
				EvToolsBtest.Send(cn.srv.hub, cn.c, ToolsBtestPayload{Result: res, Code: code, Message: msg, Done: true})
			}
		})
}

// btestAudit is a bandwidth test's audit row. The user is recorded — who the far
// server was asked to let in is part of what happened — and the password never
// is: it is not a parameter here, so it cannot be.
func btestAudit(routerID string, req toolsBtestReq, via string) audit.Event {
	ev := audit.Event{Action: "tools.bandwidth-test", TargetType: "host", TargetName: req.Address,
		RouterID: routerID, Note: "saturates the link and loads both routers while it runs"}
	ev.Extra = []audit.KV{{Key: "user", Value: req.User}, {Key: "protocol", Value: req.Protocol},
		{Key: "direction", Value: req.Direction}}
	if via != "" {
		ev.Extra = append(ev.Extra, audit.KV{Key: "via", Value: via})
	}
	return ev
}

// runBtest runs one bounded bandwidth test and folds its reports. The audit row
// is written before the command is sent.
func (cn *conn) runBtest(rs *session.Session, req toolsBtestReq, via string,
	quit <-chan struct{}, progress func(*diag.BtestResult)) (*diag.BtestResult, string, string) {
	cmd, err := diag.BandwidthTestCommand(req.Address, req.User, req.Password, req.Seconds, req.Protocol, req.Direction)
	if err != nil {
		return nil, "request", err.Error()
	}
	cn.recorder().Record(btestAudit(cn.routerID, req, via))
	return streamBtest(rs, cmd, req.Address, quit, progress)
}

// streamBtest runs a built bandwidth-test command. Each report is complete —
// one row per second, carrying the running totals — so progress is every row.
// A test that ends on any status but "done testing" failed, and says why.
func streamBtest(rs diagStream, cmd routeros.Cmd, address string,
	quit <-chan struct{}, progress func(*diag.BtestResult)) (*diag.BtestResult, string, string) {
	fold := func(rows []routeros.Reply) *diag.BtestResult {
		r := diag.FoldBandwidthTest(address, rows)
		return &r
	}
	rows, code, msg := streamDiag(rs, cmd, quit, nil, report(progress, fold))
	if code != "" {
		return nil, code, msg
	}
	r := fold(rows)
	if !r.Done {
		return r, "failed", "the test did not run: " + safe.Message(r.Status)
	}
	return r, "", ""
}

// runBtestAction is the approved `bandwidth_test` action: the page's run with
// the page's defaults, and the login the approver typed.
func (cn *conn) runBtestAction(address, user, password string) writeOutcome {
	if !cn.toolBusy.CompareAndSwap(false, true) {
		return writeOutcome{Code: "busy"}
	}
	defer cn.toolBusy.Store(false)
	res, code, msg := cn.runBtest(cn.rsession, toolsBtestReq{Address: address, User: user, Password: password,
		Protocol: diag.BtestProtocols[0], Direction: diag.BtestDirections[0]}, "agent", nil, nil)
	if code != "" {
		return writeOutcome{Code: code, Detail: map[string]any{"message": msg}}
	}
	return writeOutcome{Name: address, Detail: map[string]any{"btest": res}}
}

// btestSummary is what the operator reads of an approved test.
func btestSummary(r *diag.BtestResult) string {
	s := fmt.Sprintf("Done: tested to %s for %s. Average receive %d bit/s, transmit %d bit/s.",
		r.Address, r.Duration, r.RxBps, r.TxBps)
	if r.LostPackets > 0 {
		s += fmt.Sprintf(" %d packets lost.", r.LostPackets)
	}
	return s + fmt.Sprintf(" CPU load: this router %d%%, the far one %d%%.", r.LocalCPU, r.RemoteCPU)
}
