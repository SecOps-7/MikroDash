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
	"mikrodash/internal/session"
	"strings"

	"mikrodash/internal/aicontext"
	"mikrodash/internal/aiprovider"
	"mikrodash/internal/aitools"
	"mikrodash/internal/collect"
	"mikrodash/internal/resource"
	"mikrodash/internal/routeros"
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
//
// ── PINNED TO THE ROUTER THE QUESTION WAS ASKED ABOUT ──────────────────────
//
// `sc` is the connection's router state captured when the question was asked.
// This runs on the exchange's goroutine, not the connection's loop, and it read
// cn.rsession, cn.routerID and cn.canPage live: a router:select mid-answer
// left the context describing router A while list_* and change_row read and
// wrote router B, and the answer was saved under A (review 2026-09-19). Reads
// now use the snapshot. The tools that change or propose a change run ON THE
// LOOP, where the write path lives, and refuse if the router has moved.
func (cn *conn) runAITool(sc connScope, tc aiprovider.ToolCall) string {
	switch tc.Function.Name {
	// THE WRITE TOOL IS ITS OWN PATH. It is not bound to a resource — the model
	// names one in its arguments — so everything below, which resolves a tool to
	// a menu and reads it, does not apply. See ai_write.go.
	case aitools.WriteToolName:
		return cn.onLoopFor(sc, func() string { return cn.runAIWriteTool(tc) })
	// THE ACTION TOOL IS ITS OWN PATH TOO, and for the same reason: it names a
	// declared action rather than a menu. See ai_action.go.
	case aitools.ActionToolName:
		return cn.onLoopFor(sc, func() string { return cn.runAIActionTool(tc) })
	// A PLAN is change_row several times over, approved once. See ai_plan.go.
	case aitools.PlanToolName:
		return cn.onLoopFor(sc, func() string { return cn.runAIPlanTool(tc) })
	// THE RAW COMMAND TOOLS ARE NOT ADVERTISED, and are still answered by name.
	// A model can invent a name, and this one must meet the gates rather than
	// "no such tool", which would read as the feature being merely hidden. See
	// ai_raw.go.
	case aitools.RawCommandToolName:
		return cn.onLoopFor(sc, func() string { return cn.runAIRawCommandTool(tc) })
	case aitools.BulkToolName:
		return cn.onLoopFor(sc, func() string { return cn.runAIBulkTool(tc) })
	}
	t, ok := aitools.ByName(tc.Function.Name)
	if !ok {
		// THE NAME IS NOT ECHOED. It was chosen by the model, and repeating it
		// into the transcript is how invented vocabulary becomes established
		// vocabulary. There is nothing for the operator in it either.
		return "That tool does not exist. Only the tools you were given can be called."
	}
	if t.Page != "" && !cn.canPageIn(sc, t.Page, "read") {
		return "You do not have access to that data, so it was not read."
	}
	// A LIVE TOOL reads a collector's measurements rather than a menu's rows.
	// Checked after the permission, which applies to both kinds identically.
	if t.Collector != "" {
		return runLiveTool(sc.rs, t)
	}
	// A DIAGNOSTIC runs on the router, with the model's arguments, through the
	// same code the Tools page does. See tools.go.
	if t.Diagnostic != "" {
		return cn.runDiagTool(sc, t, tc)
	}
	res := resource.ByKey(t.Resource)
	if res == nil {
		return "That tool is not available on this build."
	}
	if sc.rs == nil {
		return "No device is selected, so nothing was read."
	}

	// A GROUPED MENU is read a group at a time: see runGroupedListTool.
	if t.GroupBy != "" {
		return runGroupedListTool(sc.rs, t, res, tc)
	}
	rows, err := readMenuOn(sc.rs, res)
	if err != nil {
		// SANITISED, like every other outbound error here: a transport failure
		// carries the router's address.
		return "The device could not be read: " + safe.Message(err.Error())
	}
	return shapeToolRows(t, res, rows)
}

// shapeToolRows is a list tool's answer: the rows, capped by count and size,
// with the true total, inside the untrusted block.
func shapeToolRows(t aitools.Tool, res *resource.Resource, rows []routeros.Reply) string {
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

// aiGroupArgMax bounds the group name the model may pass.
const aiGroupArgMax = 200

// runGroupedListTool reads a grouped menu (Address Lists, by list) as its page
// does. Without the grouping argument it answers each group and its counts,
// from a read of three small fields; with it, that group's rows, filtered ON
// THE ROUTER (`?list=<name>`), then capped as any list tool's are.
//
// ── NOT THE WHOLE MENU ──────────────────────────────────────────────────────
//
// It read every row: 37,111 address-list entries, 6 s and 4.4 MB on the
// operator's router (2026-09-18), to hand the model the first 200, which were
// whichever list came first.
func runGroupedListTool(rs *session.Session, t aitools.Tool, res *resource.Resource, tc aiprovider.ToolCall) string {
	var args map[string]any
	if raw := strings.TrimSpace(tc.Function.Arguments); raw != "" {
		if json.Unmarshal([]byte(raw), &args) != nil {
			return "The arguments were not valid JSON."
		}
	}
	group, _ := args[t.GroupBy].(string)
	if len(group) > aiGroupArgMax {
		return "That " + t.GroupBy + " name is too long."
	}
	if group == "" {
		rows, err := rs.Exec(collect.AreaGroupCmd(res, t.GroupBy))
		if err != nil {
			return "The device could not be read: " + safe.Message(err.Error())
		}
		table := collect.BuildAreaGroups(res, "", nil, t.GroupBy, rows)
		total := 0
		for _, g := range table.Groups {
			total += g.Count
		}
		body, err := json.Marshal(struct {
			Tool    string              `json:"tool"`
			Menu    string              `json:"menu"`
			GroupBy string              `json:"groupBy"`
			Groups  []collect.AreaGroup `json:"groups"`
			Total   int                 `json:"totalRows"`
			Next    string              `json:"next"`
		}{Tool: t.Name, Menu: res.Menu, GroupBy: t.GroupBy, Groups: table.Groups, Total: total,
			Next: "Call " + t.Name + " again with `" + t.GroupBy + "` set to one of these names to read its entries."})
		if err != nil {
			return "That data could not be encoded."
		}
		return aicontext.Wrap(string(body))
	}
	rows, err := rs.Exec(collect.AreaGroupRowsCmd(res, t.GroupBy, group))
	if err != nil {
		return "The device could not be read: " + safe.Message(err.Error())
	}
	out := make([]routeros.Reply, 0, len(rows))
	for _, r := range rows {
		if r[".id"] != "" {
			out = append(out, r)
		}
	}
	return shapeToolRows(t, res, out)
}

// onLoopFor runs one of the assistant's writing tools on the connection's loop,
// where the write path, the undo stacks and the proposals live, provided the
// connection is still on the router the question was asked about.
func (cn *conn) onLoopFor(sc connScope, f func() string) string {
	out := "The connection to the operator closed, so nothing was changed."
	cn.onLoop(func() {
		if cn.routerID != sc.routerID {
			out = "The operator switched to another router while you were answering, so nothing " +
				"was changed or proposed. Tell them, and ask again if they still want it."
			return
		}
		out = f()
	})
	return out
}
