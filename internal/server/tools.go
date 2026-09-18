package server

import (
	"encoding/json"
	"errors"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
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
// ── NOT AUDITED ─────────────────────────────────────────────────────────────
//
// A ping changes nothing, and the audit trail records changes and refusals to
// change. Torch and bandwidth test, which need write access, are the ones that
// will be.

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

type toolsPingReq struct {
	Address string `json:"address"`
	Count   int    `json:"count"`
}

// toolsPing answers `tools:ping` from the Tools page.
func (cn *conn) toolsPing(raw json.RawMessage) {
	var req toolsPingReq
	_ = json.Unmarshal(raw, &req)
	if cn.routerID == "" || cn.rsession == nil {
		EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Code: "unavailable"})
		return
	}
	if !cn.canPage("tools", "read") {
		EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Code: "denied"})
		return
	}
	if !cn.toolBusy.CompareAndSwap(false, true) {
		EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Code: "busy"})
		return
	}
	// PINNED: the run goes to the router selected when it was asked for. The
	// page drops a result that lands after a router switch; this goroutine does
	// not read `cn.rsession` again, which the read loop writes.
	rs := cn.rsession
	// OFF THE READ LOOP. A run lasts up to fifteen seconds, and this socket's
	// page focus, blur and every other message would wait behind it.
	go func() {
		defer cn.toolBusy.Store(false)
		res, code, msg := runPing(rs, req.Address, req.Count)
		EvToolsPing.Send(cn.srv.hub, cn.c, ToolsPingPayload{Result: res, Code: code, Message: msg})
	}()
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
	rows, err := rs.Exec(cmd)
	if err != nil {
		// A name that does not resolve, or an API user without the `test`
		// policy, is a trap with the router's own words in it — which is what
		// the operator needs to read, without this client's prefix on it.
		// SANITISED, as every outbound error is: a transport failure carries the
		// router's address.
		var trap *routeros.Trap
		if errors.As(err, &trap) {
			return nil, "failed", "the router said: " + safe.Message(trap.Message)
		}
		return nil, "failed", safe.Message(err.Error())
	}
	r := diag.FoldPing(address, rows)
	return &r, "", ""
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
