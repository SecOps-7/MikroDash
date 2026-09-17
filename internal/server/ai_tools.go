package server

// Running a tool the model asked for (#98, slice 3).
//
// Reads only. `change_row` lives in ai_write.go, because a write is a different
// question at every step: which permission, which pipeline, and whether a human
// is asked first.
//
// ── THE MODEL PICKS THE MENU; THIS DECIDES WHETHER IT MAY HAVE IT ───────────
//
// `aitools.Permitted` already filtered the catalogue before it was advertised,
// so a viewer denied the Firewall page was never told a firewall tool exists.
// This checks AGAIN anyway, and the second check is not belt and braces: the
// advertisement was made when the question was asked, a role can be edited while
// an answer is being composed, and the loop can run for several seconds. The
// filter decides what is OFFERED. This decides what is READ.
//
// ── A FRESH READ, NOT THE COLLECTOR'S LAST PAYLOAD ──────────────────────────
//
// The prompt's initial context is built from `Last()` — what the collectors
// already hold, costing no router channel. A tool is the opposite case: the
// model asked because the context did not answer, and handing it the same cached
// payload under a new name would answer the wrong question convincingly. So this
// goes to the device, through `readMenu`, exactly as the write path does.
//
// That is also why the caller caps the loop. Every other read in this app is
// demand-driven and coalesced by `roscache`; this one is chosen by a model, and
// the bottleneck this app is organised around is concurrent API channels on the
// router.

import (
	"encoding/json"
	"math"
	"time"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/resource"
	"mikrodash/internal/safe"
)

// aiToolMaxRows and aiToolMaxBytes bound one tool result.
//
// A connection table can hold thousands of rows, and a model handed all of them
// spends the operator's entire token budget on one read and then answers from a
// truncated middle without knowing it was truncated. Bounded HERE, and the
// result says it was bounded, so the model can say so too.
const (
	aiToolMaxRows  = 200
	aiToolMaxBytes = 24 << 10
)

// runAITool executes one call and returns the text of the `tool` message.
//
// ── IT NEVER RETURNS AN ERROR ───────────────────────────────────────────────
//
// A refusal is an ANSWER to the model, not a failure of the conversation. If a
// tool is unknown, or denied, or the router did not reply, the loop must carry
// on and let the model tell the operator what it could not read — an aborted
// exchange gives them a blank page and no reason.
func (cn *conn) runAITool(tc aiprovider.ToolCall) string {
	// THE WRITE TOOL IS ITS OWN PATH. It is not bound to a resource — the model
	// names one in its arguments — so everything below, which resolves a tool to
	// a menu and reads it, does not apply. See ai_write.go.
	if tc.Function.Name == aitools.WriteToolName {
		return cn.runAIWriteTool(tc)
	}
	t, ok := aitools.ByName(tc.Function.Name)
	if !ok {
		// THE NAME IS NOT ECHOED. It was chosen by the model, and repeating it
		// into the transcript is how invented vocabulary becomes established
		// vocabulary. There is nothing for the operator in it either.
		return "That tool does not exist. Only the tools you were given can be called."
	}
	if t.Page != "" && !cn.canPage(t.Page, "read") {
		return "You do not have access to that data, so it was not read."
	}
	// A LIVE TOOL reads a collector's measurements rather than a menu's rows.
	// Checked after the permission, which applies to both kinds identically.
	if t.Collector != "" {
		return cn.runLiveTool(t)
	}
	res := resource.ByKey(t.Resource)
	if res == nil {
		return "That tool is not available on this build."
	}
	if cn.rsession == nil {
		return "No device is selected, so nothing was read."
	}

	rows, err := cn.readMenu(res)
	if err != nil {
		// SANITISED, like every other outbound error here: a transport failure
		// carries the router's address.
		return "The device could not be read: " + safe.Message(err.Error())
	}

	type outRow struct {
		ID       string         `json:"id"`
		Identity string         `json:"identity"`
		Values   map[string]any `json:"values"`
	}
	// SHAPED AS `resRow` SHAPES IT. `RowValues` drops secrets and does not carry
	// `.id`; the form path supplies the id and identity beside it, and a tool
	// result that omitted them would describe rows the model cannot name.
	out := make([]outRow, 0, len(rows))
	bytesUsed := 0
	truncated := false
	for _, r := range rows {
		if len(out) >= aiToolMaxRows {
			truncated = true
			break
		}
		row := outRow{ID: r[".id"], Identity: res.IdentityOf(r), Values: res.RowValues(r)}
		b, err := json.Marshal(row)
		if err != nil {
			continue
		}
		// MEASURED ROW BY ROW rather than trimmed after the fact, so the budget
		// is a budget and not a guess that happens to hold for small menus.
		if bytesUsed+len(b) > aiToolMaxBytes {
			truncated = true
			break
		}
		bytesUsed += len(b)
		out = append(out, row)
	}

	body, err := json.Marshal(struct {
		Tool      string   `json:"tool"`
		Menu      string   `json:"menu"`
		Rows      []outRow `json:"rows"`
		Total     int      `json:"totalRows"`
		Truncated bool     `json:"truncated"`
	}{Tool: t.Name, Menu: res.Menu, Rows: out, Total: len(rows), Truncated: truncated})
	if err != nil {
		return "That data could not be encoded."
	}
	// WRAPPED IN THE SAME UNTRUSTED BLOCK AS THE INITIAL CONTEXT. These rows are
	// exactly the kind of text the block exists for: comments, device names and
	// DHCP host names chosen by whoever controls those devices.
	return aicontext.Wrap(string(body))
}

// aiLiveMaxAge is how old a FreshLive reading may be before a tool re-reads it.
//
// ── SECONDS, NOT THE STALENESS RULE ─────────────────────────────────────────
//
// The context summary calls a reading stale only after its interval plus a grace,
// which suits "is this still roughly true". A throughput question is "what is it
// doing NOW", and a rate from a minute ago answers a different one. With the
// Interfaces page open the collector reads every second, so this almost never
// costs a read; with nothing open it costs one refresh per question.
const aiLiveMaxAge = 5 * time.Second

// liveToolReaders answers each live tool, keyed by the collector it names.
//
// A LEDGER IN BOTH DIRECTIONS (`TestEveryLiveToolHasAReader`): a live tool with
// no reader would be advertised and then answer "not available", and a reader no
// tool names is code nothing can reach.
var liveToolReaders = map[string]func(cn *conn, t aitools.Tool) string{
	"ifStatus": (*conn).liveInterfaceTraffic,
}

// liveReadingDue decides whether a live tool must re-read its collector before
// answering. Pure, so the rule is tested without a router.
//
// ── NO READING, OR NO TIME ON IT, IS ALWAYS DUE ─────────────────────────────
//
// `aicontext.IsStale` calls a zero timestamp "unknown, not stale", which is the
// honest thing for a summary to say about a reading. A tool is asked to ANSWER,
// and a reading it cannot date is not one it should hand over as current.
func liveReadingDue(freshness string, present bool, now, ts int64, pollMs int) bool {
	if !present || ts <= 0 {
		return true
	}
	if freshness == aitools.FreshMetadata {
		return aicontext.IsStale(now, ts, pollMs)
	}
	// FreshLive, and anything undeclared: the stricter bound.
	// TestEveryLiveToolDeclaresItsFreshness keeps "undeclared" from happening.
	return now-ts > aiLiveMaxAge.Milliseconds()
}

func (cn *conn) runLiveTool(t aitools.Tool) string {
	read, ok := liveToolReaders[t.Collector]
	if !ok {
		return "That tool is not available on this build."
	}
	if cn.rsession == nil {
		return "No device is selected, so nothing was read."
	}
	return read(cn, t)
}

// liveInterfaceTraffic is `list_interface_traffic`: every interface with its live
// rates, from the payload the Interfaces page draws.
func (cn *conn) liveInterfaceTraffic(t aitools.Tool) string {
	col := cn.rsession.IfStatus()
	p := col.Last()
	var ts int64
	if p != nil {
		ts = p.TS
	}
	if liveReadingDue(t.Freshness, p != nil, time.Now().UnixMilli(), ts, 0) {
		col.RefreshNow()
		p = col.Last()
	}
	if p == nil {
		return "The interface readings are not available from this router yet."
	}

	type ifRow struct {
		Name        string   `json:"name"`
		Type        string   `json:"type"`
		Comment     string   `json:"comment,omitempty"`
		Running     bool     `json:"running"`
		Disabled    bool     `json:"disabled"`
		RxMbps      float64  `json:"rxMbps"`
		TxMbps      float64  `json:"txMbps"`
		IPs         []string `json:"addresses,omitempty"`
		RxBytes     *float64 `json:"rxBytes,omitempty"`
		TxBytes     *float64 `json:"txBytes,omitempty"`
		ErrorsDelta *float64 `json:"errorsSinceLastReading,omitempty"`
		DropsDelta  *float64 `json:"dropsSinceLastReading,omitempty"`
	}
	out := make([]ifRow, 0, len(p.Interfaces))
	bytesUsed, truncated := 0, false
	for _, i := range p.Interfaces {
		if len(out) >= aiToolMaxRows {
			truncated = true
			break
		}
		row := ifRow{Name: i.Name, Type: i.Type, Comment: i.Comment, Running: i.Running,
			Disabled: i.Disabled, RxMbps: i.RxMbps, TxMbps: i.TxMbps, IPs: i.IPs,
			RxBytes: i.RxBytes, TxBytes: i.TxBytes, ErrorsDelta: i.ErrorsDelta, DropsDelta: i.DropsDelta}
		b, err := json.Marshal(row)
		if err != nil {
			continue
		}
		// The same row-by-row budget as a resource tool, so a router with
		// hundreds of VLAN interfaces cannot blow the model's context.
		if bytesUsed+len(b) > aiToolMaxBytes {
			truncated = true
			break
		}
		bytesUsed += len(b)
		out = append(out, row)
	}

	body, err := json.Marshal(struct {
		Tool       string  `json:"tool"`
		Menu       string  `json:"menu"`
		AgeSeconds float64 `json:"readingAgeSeconds"`
		Interfaces []ifRow `json:"interfaces"`
		Total      int     `json:"totalInterfaces"`
		Truncated  bool    `json:"truncated"`
	}{
		Tool: t.Name, Menu: "/interface/monitor-traffic",
		AgeSeconds: math.Round(time.Since(time.UnixMilli(p.TS)).Seconds()*10) / 10,
		Interfaces: out, Total: len(p.Interfaces), Truncated: truncated,
	})
	if err != nil {
		return "That data could not be encoded."
	}
	// UNTRUSTED like every tool result: interface names and comments are chosen by
	// whoever configured the router.
	return aicontext.Wrap(string(body))
}
