package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

// ToolsPingPayload is `tools:ping`: a finished run, or why there is none.
type ToolsPingPayload struct {
	// Result is nil when Code is set.
	Result *diag.PingResult `json:"result"`
	// Code is empty on success, and otherwise one of: denied, unavailable,
	// busy, address, failed.
	Code string `json:"code"`
	// Message is the router's own words for a failed run, sanitised.
	Message string `json:"message"`
}

// ToolsTraceroutePayload is `tools:traceroute`, shaped as ToolsPingPayload is.
type ToolsTraceroutePayload struct {
	Result  *diag.TracerouteResult `json:"result"`
	Code    string                 `json:"code"`
	Message string                 `json:"message"`
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
// OFF THE READ LOOP because a run lasts up to thirty-five seconds, and this
// socket's page focus, blur and every other message would wait behind it.
func (cn *conn) startTool(access string, refuse func(code string), work func(rs *session.Session)) {
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
	go func() {
		defer cn.toolBusy.Store(false)
		work(rs)
	}()
}

// toolsPing answers `tools:ping` from the Tools page.
func (cn *conn) toolsPing(raw json.RawMessage) {
	var req toolsPingReq
	_ = json.Unmarshal(raw, &req)
	cn.startTool("read",
		func(code string) { EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Code: code}) },
		func(rs *session.Session) {
			res, code, msg := runPing(rs, req.Address, req.Count)
			EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Result: res, Code: code, Message: msg})
		})
}

// toolsTraceroute answers `tools:traceroute` from the Tools page.
func (cn *conn) toolsTraceroute(raw json.RawMessage) {
	var req toolsTracerouteReq
	_ = json.Unmarshal(raw, &req)
	cn.startTool("read",
		func(code string) { EvToolsTraceroute.Send(cn.srv.hub, cn.c, ToolsTraceroutePayload{Code: code}) },
		func(rs *session.Session) {
			res, code, msg := runTraceroute(rs, req.Address, req.MaxHops)
			EvToolsTraceroute.Send(cn.srv.hub, cn.c, ToolsTraceroutePayload{Result: res, Code: code, Message: msg})
		})
}

// runPing runs one bounded ping on a router and folds its reply.
//
// Returns the result, or a code and the router's own message. The caller has
// already checked permission; this checks only what the request itself can get
// wrong.
func runPing(rs *session.Session, address string, count int) (*diag.PingResult, string, string) {
	cmd, err := diag.PingCommand(address, count)
	if err != nil {
		return nil, "address", err.Error()
	}
	rows, code, msg := runDiag(rs, cmd)
	if code != "" {
		return nil, code, msg
	}
	r := diag.FoldPing(address, rows)
	return &r, "", ""
}

// runTraceroute runs one bounded traceroute on a router and folds its reply.
func runTraceroute(rs *session.Session, address string, maxHops int) (*diag.TracerouteResult, string, string) {
	cmd, err := diag.TracerouteCommand(address, maxHops)
	if err != nil {
		return nil, "address", err.Error()
	}
	rows, code, msg := runDiag(rs, cmd)
	if code != "" {
		return nil, code, msg
	}
	r := diag.FoldTraceroute(address, rows)
	return &r, "", ""
}

// runDiag sends one diagnostic command and returns its rows, or "failed" with
// the reason.
//
// A name that does not resolve, or an API user without the `test` policy, is a
// trap with the router's own words in it — which is what the operator needs to
// read, without this client's prefix on it. SANITISED, as every outbound error
// is: a transport failure carries the router's address.
func runDiag(rs *session.Session, cmd routeros.Cmd) ([]routeros.Reply, string, string) {
	rows, err := rs.Exec(cmd)
	if err == nil {
		return rows, "", ""
	}
	var trap *routeros.Trap
	if errors.As(err, &trap) {
		return nil, "failed", "the router said: " + safe.Message(trap.Message)
	}
	return nil, "failed", safe.Message(err.Error())
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
		res, code, msg := runPing(rs, req.Address, req.Count)
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
		res, code, msg := runTraceroute(rs, req.Address, req.MaxHops)
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
func (cn *conn) runDiagTool(t aitools.Tool, tc aiprovider.ToolCall) string {
	run, ok := diagRunners[t.Diagnostic]
	if !ok {
		return "That tool is not available on this build."
	}
	// THE TOOL'S OWN ACCESS, which for a diagnostic is not always read: the
	// caller checked read, and torch and bandwidth test will need write.
	if !cn.canPage(t.Page, t.Access) {
		return "You do not have access to that tool, so nothing was run."
	}
	if cn.rsession == nil {
		return "No device is selected, so nothing was run."
	}
	// THE SAME ONE-AT-A-TIME RULE as the page, and on the same flag: the page
	// and the assistant on one connection are one person asking twice.
	if !cn.toolBusy.CompareAndSwap(false, true) {
		return "Another diagnostic is still running for this operator; try again when it has finished."
	}
	defer cn.toolBusy.Store(false)
	res, why := run(cn.rsession, []byte(tc.Function.Arguments))
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
			EvToolsTorch.Send(cn.srv.hub, cn.c, ToolsTorchPayload{Code: code})
		},
		func(rs *session.Session) {
			res, code, msg := cn.runTorch(rs, req.Interface, req.Seconds, "")
			EvToolsTorch.Send(cn.srv.hub, cn.c, ToolsTorchPayload{Result: res, Code: code, Message: msg})
		})
}

// runTorch watches one interface for a bounded time and folds what it saw.
//
// The interface must be one the router has: RouterOS answers a wrong name with
// "input does not match any value of interface", which is true and unhelpful,
// and a name read fresh here makes the refusal say what was wrong. The audit
// row is written BEFORE the command is sent, as the wifi scan's is: a run that
// starts and then fails to be recorded still loaded the router.
func (cn *conn) runTorch(rs *session.Session, iface string, seconds int, via string) (*diag.TorchResult, string, string) {
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
	rows, code, msg := runDiag(rs, cmd)
	if code != "" {
		return nil, code, msg
	}
	r := diag.FoldTorch(iface, rows)
	r.Seconds = diag.TorchSeconds(seconds)
	return &r, "", ""
}

// runTorchAction is the approved `torch` action: the page's run, for the
// page's fixed default duration, on the page's one run slot.
func (cn *conn) runTorchAction(iface string) writeOutcome {
	if !cn.toolBusy.CompareAndSwap(false, true) {
		return writeOutcome{Code: "busy"}
	}
	defer cn.toolBusy.Store(false)
	res, code, msg := cn.runTorch(cn.rsession, iface, diag.TorchDefaultSeconds, "agent")
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
